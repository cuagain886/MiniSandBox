package application

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

// IDGenerator 为创建用例生成不可预测、全局唯一概率足够高的 sandbox ID。
type IDGenerator interface {
	// NewID 返回 UUID v4 字符串；随机源不可用时必须返回错误，不能降级为时间戳。
	NewID() (string, error)
}

// Clock 为应用用例提供可注入的当前时间。
type Clock interface {
	// Now 返回当前时刻；生产实现保证 UTC，调用方仍应在持久化边界归一化。
	Now() time.Time
}

// RandomIDGenerator 使用密码学安全随机源生成 UUID v4 sandbox ID。
type RandomIDGenerator struct {
	reader io.Reader
}

// NewRandomIDGenerator 创建使用 crypto/rand.Reader 的生产 ID generator。
func NewRandomIDGenerator() *RandomIDGenerator {
	return &RandomIDGenerator{reader: rand.Reader}
}

// newRandomIDGenerator 使用指定随机源创建 generator，仅供确定性测试失败路径。
func newRandomIDGenerator(reader io.Reader) *RandomIDGenerator {
	return &RandomIDGenerator{reader: reader}
}

// NewID 读取 128 位随机数并编码为小写连字符 UUID v4。
//
// 第 6 字节高四位固定为版本 4，第 8 字节高两位固定为 RFC 4122 variant；
// 随机读取不足 16 字节时返回错误，不生成可预测或部分填充的 ID。
func (g *RandomIDGenerator) NewID() (string, error) {
	reader := g.reader
	if reader == nil {
		// 保持导出类型零值可用，避免手工装配遗漏 reader 时触发 nil panic；
		// 测试失败注入仍通过显式非 nil reader 完成。
		reader = rand.Reader
	}
	var raw [16]byte
	if _, err := io.ReadFull(reader, raw[:]); err != nil {
		return "", fmt.Errorf("read UUID random bytes: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	var encoded [32]byte
	hex.Encode(encoded[:], raw[:])
	return string(encoded[0:8]) + "-" +
		string(encoded[8:12]) + "-" +
		string(encoded[12:16]) + "-" +
		string(encoded[16:20]) + "-" +
		string(encoded[20:32]), nil
}

// SystemClock 使用系统时钟提供生产环境当前时间。
type SystemClock struct{}

// Now 返回 UTC 系统时间，避免应用层生成依赖宿主机本地时区的记录。
func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

var (
	_ IDGenerator = (*RandomIDGenerator)(nil)
	_ Clock       = SystemClock{}
)
