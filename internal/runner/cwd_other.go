//go:build !linux

package runner

// ValidateCWD 在非 Linux 构建中 fail closed；production execution 仅支持 Linux。
func ValidateCWD(_, _ string) (string, error) {
	return "", ErrInvalidCWD
}
