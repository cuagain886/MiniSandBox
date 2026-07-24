package runner

import (
	"bytes"
	"sync"
)

// SynchronizedBuffer 提供可被 stdout/stderr 写入协程安全访问的内存缓冲区。
type SynchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write 以互斥方式追加输出，实现 io.Writer。
func (b *SynchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

// Bytes 返回当前缓冲内容的副本，调用方可以安全修改返回值。
func (b *SynchronizedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.b.Bytes()...)
}
