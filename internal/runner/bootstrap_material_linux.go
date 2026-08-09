//go:build linux

package runner

import (
	"errors"
	"os"
	"path/filepath"

	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
)

const maxBootstrapConfigBytes = 1 << 20

// LoadBootstrapMaterial 严格读取并删除固定配置文件和一次性 token 文件。
func LoadBootstrapMaterial(runtimeDirectory string) (runnerbootstrap.Config, runnerauth.Token, error) {
	path := filepath.Join(runtimeDirectory, runnerbootstrap.ConfigFileName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > maxBootstrapConfigBytes {
		return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("runner bootstrap file is invalid")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("read runner bootstrap file failed")
	}
	value, err := runnerbootstrap.Unmarshal(content)
	clear(content)
	if err != nil {
		return runnerbootstrap.Config{}, runnerauth.Token{}, err
	}
	if err := os.Remove(path); err != nil {
		return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("consume runner bootstrap file failed")
	}
	token, err := runnerauth.ConsumeTokenFile(runtimeDirectory, value.Identity.SocketOwnerUID, value.Identity.SocketOwnerGID)
	if err != nil {
		return runnerbootstrap.Config{}, runnerauth.Token{}, err
	}
	return value, token, nil
}
