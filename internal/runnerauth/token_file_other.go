//go:build !linux

package runnerauth

import "errors"

// StageTokenFile 在非 Linux 平台清零 token 并明确失败。
func StageTokenFile(_ string, _, _ uint32, token *Token) error {
	if token != nil {
		token.Clear()
	}
	return errors.New("runner credential staging requires Linux")
}

// ConsumeTokenFile 在非 Linux 平台明确失败。
func ConsumeTokenFile(string, uint32, uint32) (Token, error) {
	return Token{}, errors.New("runner credential consumption requires Linux")
}
