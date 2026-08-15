package runnerpty

import (
	"io"
	"sync"
	"time"
)

// stdinWriteMu 序列化并发 stdin 写入，避免 PTY 主设备交错写坏输入流。
// 每个会话一个锁，通过 Manager 注入的全局表按会话 ID 索引。
var stdinWriteLocks sync.Map

// WriteStdin 把调用方提供的字节写入 PTY stdin。
//
// 进程已退出时写入会以 EIO 失败，调用方应忽略该错误并等待终态。写操作
// 串行化执行，不缓存无界输入。
func (s *Session) WriteStdin(p []byte) error {
	lock, _ := stdinWriteLocks.LoadOrStore(s.id, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()
	if s.isFinished() {
		return ErrAlreadyFinished
	}
	_, err := s.process.WriteStdin(p)
	return err
}

// Resize 调整 PTY 窗口；终态后是安全 no-op。
func (s *Session) Resize(cols, rows uint16) error {
	if s.isFinished() {
		return nil
	}
	return s.process.Resize(cols, rows)
}

// isFinished 报告会话是否已发布终态。
func (s *Session) isFinished() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// supervise 是终态唯一仲裁者：进程退出、取消、超时三路竞争只有一个赢家。
//
// 超时与取消的终态按归因上报且不携带退出码；Terminate 内部完成
// TERM→grace→KILL 和进程回收，返回后 Wait 结果必然可达。
func (s *Session) supervise(options StartOptions) {
	timeout := options.DefaultTimeout
	if options.Request.TimeoutSeconds > 0 {
		timeout = time.Duration(options.Request.TimeoutSeconds) * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	exitCode := make(chan int, 1)
	go func() {
		code, _ := s.process.Wait()
		exitCode <- code
	}()

	select {
	case code := <-exitCode:
		s.finishWith(TerminalResult{Cause: TerminalCauseExited, ExitCode: &code})
	case <-timer.C:
		s.setCause(TerminalCauseTimedOut)
		s.process.Terminate(options.TerminationGrace)
		<-exitCode
		s.finishWith(TerminalResult{Cause: TerminalCauseTimedOut})
	case <-s.ctx.Done():
		cause := s.takeCause()
		if cause == "" {
			cause = TerminalCauseCancelled
		}
		s.process.Terminate(options.TerminationGrace)
		<-exitCode
		s.finishWith(TerminalResult{Cause: cause})
	}
}

// setCause 在取消前记录归因；只允许从空值设置一次。
func (s *Session) setCause(cause TerminalCause) {
	s.causeMu.Lock()
	if s.cause == "" {
		s.cause = cause
	}
	s.causeMu.Unlock()
}

// takeCause 读取并清空归因。
func (s *Session) takeCause() TerminalCause {
	s.causeMu.Lock()
	defer s.causeMu.Unlock()
	cause := s.cause
	s.cause = ""
	return cause
}

// pumpOutput 持续读取 PTY 合并输出并复制到有界通道。
//
// 通道缓冲限制在内存上限内；会话结束后剩余缓冲仍可被消费，读尽后由
// 关闭 done 信号通知消费方停止。
func (s *Session) pumpOutput() {
	buffer := make([]byte, 32*1024)
	for {
		read, err := s.process.ReadOutput(buffer)
		if read > 0 {
			chunk := make([]byte, read)
			copy(chunk, buffer[:read])
			select {
			case s.output <- chunk:
			case <-s.done:
				return
			}
		}
		if err != nil {
			// EOF 或 EIO 都表示 PTY 主设备不再产生输出；终态由 supervise 决定。
			if err == io.EOF {
				return
			}
			select {
			case <-s.done:
				return
			default:
				// 短暂错误继续读取；进程退出后的持久错误会在下次返回 EOF/EIO。
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
}
