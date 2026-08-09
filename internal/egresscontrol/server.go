package egresscontrol

import (
	"context"
	"errors"
	"io"
	"time"

	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egressnft"
)

// ServerOptions 提供 egressd 控制循环唯一允许使用的 nft 与 Linux 安全依赖。
type ServerOptions struct {
	// Executor 仅在首个 bootstrap 请求中执行固定 nft 安装与回读命令。
	Executor egressnft.Executor
	// Platform 执行并回读 netns、credential、capability 与 no_new_privs。
	Platform egressanchor.Platform
	// Now 生成初始 attestation 时间；nil 时使用当前 UTC 时间。
	Now func() time.Time
}

// Serve 在可重连 stdin/stdout 上运行封闭状态机：首帧必须是 bootstrap，成功后只
// 接受 inspect。任一非法帧、重复 bootstrap、权限漂移或输出失败都会返回错误，使
// egressd 退出并关闭其网络命名空间。
func Serve(ctx context.Context, input io.ReadCloser, output io.Writer, options ServerOptions) error {
	if ctx == nil || input == nil || output == nil || options.Executor == nil || options.Platform == nil {
		return errors.New("egress control server dependencies are incomplete")
	}
	stopClosing := make(chan struct{})
	defer close(stopClosing)
	go func() {
		select {
		case <-ctx.Done():
			_ = input.Close()
		case <-stopClosing:
		}
	}()

	var state *serverState
	for {
		if ctx.Err() != nil {
			return nil
		}
		request, err := ReadRequest(input)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("read egress control request")
		}
		switch request.Type {
		case RequestBootstrap:
			if state != nil || request.Bootstrap == nil {
				return errors.New("egress bootstrap request is not allowed")
			}
			if err := egressnft.Install(ctx, options.Executor, request.Bootstrap.Policy); err != nil {
				return err
			}
			attestation, err := egressanchor.Prepare(*request.Bootstrap, options.Platform, options.Now)
			if err != nil {
				return err
			}
			state = &serverState{
				attestation: attestation,
				uid:         request.Bootstrap.AnchorUID,
				gid:         request.Bootstrap.AnchorGID,
			}
		case RequestInspect:
			if state == nil {
				return errors.New("egress inspect before bootstrap")
			}
			if err := egressanchor.Verify(options.Platform, state.attestation, state.uid, state.gid); err != nil {
				return err
			}
		default:
			return errors.New("egress control request type is invalid")
		}
		response, err := EncodeResponse(Response{
			RequestID: request.RequestID, Nonce: request.Nonce, Attestation: state.attestation,
		})
		if err != nil {
			return err
		}
		if err := writeFull(output, response); err != nil {
			return errors.New("write egress control response")
		}
	}
}

type serverState struct {
	attestation egressanchor.Attestation
	uid         uint32
	gid         uint32
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
