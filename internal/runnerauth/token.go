package runnerauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
)

const (
	tokenBytes            = sha256.Size
	tokenDerivationDomain = "minisandbox/runner-token/v1\x00"
	// CredentialFileName 是受管 runtime 目录内一次性 token 的固定文件名。
	CredentialFileName = "runner-token"
)

// Token 是单个 sandbox runner 内部 HTTP 认证使用的 256-bit 派生凭据。
type Token [tokenBytes]byte

// Clear 清零当前 Token 实例；已经产生的显式副本仍由副本持有者负责清理。
func (t *Token) Clear() {
	if t != nil {
		clear(t[:])
	}
}

// DeriveToken 使用 HMAC-SHA256 和版本化 domain separation，从主密钥与
// sandbox ID 确定性派生 token。
//
// 主密钥不得为空或全零；sandbox ID 必须是规范小写 UUID v4。函数不修改
// master key，调用方在全部派生完成后负责 Clear。
func DeriveToken(masterKey *MasterKey, sandboxID string) (Token, error) {
	if masterKey == nil || allZero(masterKey[:]) {
		return Token{}, errors.New("runner token master key is invalid")
	}
	if !validSandboxID(sandboxID) {
		return Token{}, errors.New("runner token sandbox ID is invalid")
	}
	message := make([]byte, 0, len(tokenDerivationDomain)+len(sandboxID))
	message = append(message, tokenDerivationDomain...)
	message = append(message, sandboxID...)
	defer clear(message)

	mac := hmac.New(sha256.New, masterKey[:])
	_, _ = mac.Write(message)
	digest := mac.Sum(nil)
	defer clear(digest)
	var token Token
	copy(token[:], digest)
	return token, nil
}

func validSandboxID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id[14] != '4' {
		return false
	}
	for index := range id {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		value := id[index]
		if value < '0' || value > '9' {
			if value < 'a' || value > 'f' {
				return false
			}
		}
	}
	switch id[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}
