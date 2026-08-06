package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const backgroundLogFileSuffix = ".ndjson"

// ErrBackgroundLogWrite 表示后台事件日志无法安全、完整地继续追加。
var ErrBackgroundLogWrite = errors.New("background execution log write failed")

type backgroundLogFile interface {
	io.Writer
	Sync() error
	Close() error
}

type backgroundLogOpen func(string, int, os.FileMode) (backgroundLogFile, error)

// BackgroundLogWriter 把 EventStore 的有序快照追加为逐行独立 JSON；该文件不是防篡改审计记录。
type BackgroundLogWriter struct {
	done chan struct{}

	mu  sync.RWMutex
	err error
}

// NewBackgroundLogWriter 以 O_CREATE|O_EXCL 和 0600 创建固定 execution 日志并立即开始追加。
func NewBackgroundLogWriter(
	directory string,
	id ExecutionID,
	store *EventStore,
	arbiter *TerminalArbiter,
) (*BackgroundLogWriter, error) {
	return newBackgroundLogWriter(directory, id, store, arbiter, func(path string, flags int, mode os.FileMode) (backgroundLogFile, error) {
		return os.OpenFile(path, flags, mode)
	})
}

func newBackgroundLogWriter(
	directory string,
	id ExecutionID,
	store *EventStore,
	arbiter *TerminalArbiter,
	open backgroundLogOpen,
) (*BackgroundLogWriter, error) {
	path, err := BackgroundLogPath(directory, id)
	if err != nil || store == nil || arbiter == nil || open == nil {
		return nil, ErrBackgroundLogWrite
	}
	file, err := open(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, ErrBackgroundLogWrite
	}
	writer := &BackgroundLogWriter{done: make(chan struct{})}
	go writer.run(id, store, arbiter, file)
	return writer, nil
}

// BackgroundLogPath 从固定目录和已校验 execution ID 构造内部日志路径，不接受请求提供的文件名。
func BackgroundLogPath(directory string, id ExecutionID) (string, error) {
	if !filepath.IsAbs(directory) || !validStoredExecutionID(id) {
		return "", ErrBackgroundLogWrite
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", ErrBackgroundLogWrite
	}
	return filepath.Join(directory, string(id)+backgroundLogFileSuffix), nil
}

// Wait 等待 terminal 已完整写入并完成 sync/close，或返回受控日志错误。
func (w *BackgroundLogWriter) Wait(ctx context.Context) error {
	if w == nil || w.done == nil || ctx == nil {
		return ErrBackgroundLogWrite
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		w.mu.RLock()
		defer w.mu.RUnlock()
		return w.err
	}
}

func (w *BackgroundLogWriter) run(id ExecutionID, store *EventStore, arbiter *TerminalArbiter, file backgroundLogFile) {
	defer close(w.done)
	cursor := uint64(0)
	expected := uint64(1)
	for {
		events, terminal, changed := store.EventsAfter(cursor)
		for _, event := range events {
			if event.ExecutionID != string(id) || event.Sequence != expected || event.Validate() != nil {
				w.fail(arbiter, file, ErrBackgroundLogWrite)
				return
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				w.fail(arbiter, file, ErrBackgroundLogWrite)
				return
			}
			encoded = append(encoded, '\n')
			if err := writeAll(file, encoded); err != nil {
				w.fail(arbiter, file, ErrBackgroundLogWrite)
				return
			}
			cursor = event.Sequence
			expected++
			if event.Terminal() {
				if err := file.Sync(); err != nil {
					w.fail(arbiter, file, ErrBackgroundLogWrite)
					return
				}
				if err := file.Close(); err != nil {
					w.submitFailure(arbiter)
					w.setError(ErrBackgroundLogWrite)
				}
				return
			}
		}
		if terminal {
			w.fail(arbiter, file, ErrBackgroundLogWrite)
			return
		}
		<-changed
	}
}

func (w *BackgroundLogWriter) fail(arbiter *TerminalArbiter, file backgroundLogFile, err error) {
	w.submitFailure(arbiter)
	_ = file.Close()
	w.setError(err)
}

func (w *BackgroundLogWriter) submitFailure(arbiter *TerminalArbiter) {
	_, _ = arbiter.Submit(context.Background(), internalFailureCandidate(0))
}

func (w *BackgroundLogWriter) setError(err error) {
	w.mu.Lock()
	w.err = err
	w.mu.Unlock()
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		count, err := writer.Write(data)
		if err != nil {
			return err
		}
		if count <= 0 || count > len(data) {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}

func validStoredExecutionID(id ExecutionID) bool {
	value := string(id)
	if !strings.HasPrefix(value, executionIDPrefix) || len(value) <= len(executionIDPrefix) || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
