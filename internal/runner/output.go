package runner

import (
	"bytes"
	"sync"
)

type SynchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *SynchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *SynchronizedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.b.Bytes()...)
}
