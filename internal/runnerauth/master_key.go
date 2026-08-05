// Package runnerauth 负责 runner 内部认证主密钥、派生 token 与一次性凭据文件。
//
// 本包不处理公共 HTTP 鉴权，不把秘密写入配置、日志、labels、command 或
// environment；调用方必须在使用结束后尽快清零可变缓冲区。
package runnerauth

const masterKeyBytes = 32

// MasterKey 是 sandboxd 用于派生 runner token 的固定 256-bit 主密钥。
//
// 数组形态避免隐式字符串复制；不得记录、格式化或序列化该值。
type MasterKey [masterKeyBytes]byte

// Clear 清零当前 MasterKey 实例；已经产生的显式副本仍由副本持有者负责清理。
func (k *MasterKey) Clear() {
	if k != nil {
		clear(k[:])
	}
}

func allZero(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined == 0
}
