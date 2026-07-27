package application

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

// failingReader 始终返回指定错误，用于验证随机源失败传播。
type failingReader struct {
	err error
}

// Read 返回预先配置的随机源错误。
func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

// fixedClock 是 service 测试可复用模式的最小固定时钟。
type fixedClock struct {
	now time.Time
}

// Now 返回测试指定的固定时刻。
func (c fixedClock) Now() time.Time {
	return c.now
}

// TestRandomIDGeneratorUUIDV4 验证格式、版本位和 variant 位。
func TestRandomIDGeneratorUUIDV4(t *testing.T) {
	randomBytes := []byte{
		0x00, 0x01, 0x02, 0x03,
		0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b,
		0x0c, 0x0d, 0x0e, 0x0f,
	}
	generator := newRandomIDGenerator(bytes.NewReader(randomBytes))

	got, err := generator.NewID()
	if err != nil {
		t.Fatalf("generate ID: %v", err)
	}
	const want = "00010203-0405-4607-8809-0a0b0c0d0e0f"
	if got != want {
		t.Fatalf("UUID: got %q, want %q", got, want)
	}
}

// TestRandomIDGeneratorFailure 验证随机源错误和短读取不会生成 ID。
func TestRandomIDGeneratorFailure(t *testing.T) {
	injected := errors.New("random unavailable")
	tests := []struct {
		name   string
		reader io.Reader
		want   error
	}{
		{
			name:   "source error",
			reader: failingReader{err: injected},
			want:   injected,
		},
		{
			name:   "short source",
			reader: bytes.NewReader(make([]byte, 15)),
			want:   io.ErrUnexpectedEOF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newRandomIDGenerator(tt.reader).NewID()
			if !errors.Is(err, tt.want) {
				t.Fatalf("generate ID: got %v, want %v", err, tt.want)
			}
			if got != "" {
				t.Fatalf("failed generation returned ID %q", got)
			}
		})
	}
}

// TestSystemClockReturnsUTC 验证生产 Clock 使用当前 UTC 时间。
func TestSystemClockReturnsUTC(t *testing.T) {
	before := time.Now().UTC()
	got := (SystemClock{}).Now()
	after := time.Now().UTC()

	if got.Location() != time.UTC {
		t.Fatalf("clock location: got %v, want UTC", got.Location())
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("clock time %v outside [%v, %v]", got, before, after)
	}
}

// TestClockSupportsFixedImplementation 验证应用接口不依赖全局系统时间。
func TestClockSupportsFixedImplementation(t *testing.T) {
	want := time.Date(2027, 5, 6, 7, 8, 9, 10, time.FixedZone("UTC+8", 8*60*60))
	var clock Clock = fixedClock{now: want}
	if got := clock.Now(); !got.Equal(want) || got.Location() != want.Location() {
		t.Fatalf("fixed clock: got %v, want %v", got, want)
	}
}
