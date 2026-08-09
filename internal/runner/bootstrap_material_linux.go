//go:build linux

package runner

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
)

const maxBootstrapConfigBytes = 1 << 20

var errBootstrapMaterialMissing = errors.New("runner bootstrap material is not visible")

// LoadBootstrapMaterial 严格读取并删除固定配置文件和一次性 token 文件。
func LoadBootstrapMaterial(runtimeDirectory string) (runnerbootstrap.Config, runnerauth.Token, error) {
	path := filepath.Join(runtimeDirectory, runnerbootstrap.ConfigFileName)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return runnerbootstrap.Config{}, runnerauth.Token{}, errBootstrapMaterialMissing
		}
		return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("inspect runner bootstrap file failed")
	}
	// 两个文件都可见后才消费 config，避免 bind 传播只完成一半时留下不可重试状态。
	if _, err := os.Lstat(filepath.Join(runtimeDirectory, runnerauth.CredentialFileName)); err != nil {
		if os.IsNotExist(err) {
			return runnerbootstrap.Config{}, runnerauth.Token{}, errBootstrapMaterialMissing
		}
		return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("inspect runner credential failed")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("runner bootstrap file type is invalid")
	}
	if info.Size() <= 0 || info.Size() > maxBootstrapConfigBytes {
		return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("runner bootstrap file size is invalid")
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
	token, err := runnerauth.ConsumeTokenFile(runtimeDirectory, 0, 0)
	if err != nil {
		return runnerbootstrap.Config{}, runnerauth.Token{}, err
	}
	return value, token, nil
}

// WaitLoadBootstrapMaterial 只为 Docker bind 的短暂不可见窗口执行有界重试；
// 一旦材料可见，任何类型、权限、owner、内容或消费错误都会立即 fail closed。
func WaitLoadBootstrapMaterial(runtimeDirectory string, timeout time.Duration) (runnerbootstrap.Config, runnerauth.Token, error) {
	if timeout <= 0 {
		return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("runner bootstrap visibility timeout must be positive")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		ownerUID, ownerGID, err := acquireBootstrapMaterial(runtimeDirectory)
		if errors.Is(err, errBootstrapMaterialMissing) {
			select {
			case <-deadline.C:
				return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("runner bootstrap material visibility timed out")
			case <-ticker.C:
				continue
			}
		}
		if err != nil {
			return runnerbootstrap.Config{}, runnerauth.Token{}, err
		}
		value, token, err := LoadBootstrapMaterial(runtimeDirectory)
		if err != nil {
			restoreBootstrapOwnership(runtimeDirectory, ownerUID, ownerGID)
		}
		return value, token, err
	}
}

// acquireBootstrapMaterial 按已批准的最小 capability 模型，先把固定 runtime
// 目录与两份启动材料临时收敛为 root:root，再交给不含 DAC_OVERRIDE 的 runner 消费。
func acquireBootstrapMaterial(runtimeDirectory string) (uint32, uint32, error) {
	info, err := os.Lstat(runtimeDirectory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return 0, 0, errors.New("runner bootstrap directory is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("runner bootstrap directory owner is unavailable")
	}
	ownerUID, ownerGID := stat.Uid, stat.Gid
	if err := os.Chown(runtimeDirectory, 0, 0); err != nil {
		return 0, 0, errors.New("acquire runner bootstrap directory failed")
	}
	acquired := make([]string, 0, 2)
	restore := func() {
		for _, path := range acquired {
			_ = os.Chown(path, int(ownerUID), int(ownerGID))
		}
		_ = os.Chown(runtimeDirectory, int(ownerUID), int(ownerGID))
	}
	for _, name := range []string{runnerbootstrap.ConfigFileName, runnerauth.CredentialFileName} {
		path := filepath.Join(runtimeDirectory, name)
		entry, err := os.Lstat(path)
		if os.IsNotExist(err) {
			restore()
			return 0, 0, errBootstrapMaterialMissing
		}
		entryStat, validOwner := entry.Sys().(*syscall.Stat_t)
		if err != nil || entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() || entry.Mode().Perm() != 0o600 || !validOwner || entryStat.Uid != ownerUID || entryStat.Gid != ownerGID {
			restore()
			return 0, 0, errors.New("runner bootstrap material is unsafe")
		}
		if err := os.Chown(path, 0, 0); err != nil {
			restore()
			return 0, 0, errors.New("acquire runner bootstrap material failed")
		}
		acquired = append(acquired, path)
	}
	return ownerUID, ownerGID, nil
}

func restoreBootstrapOwnership(runtimeDirectory string, uid, gid uint32) {
	for _, name := range []string{runnerbootstrap.ConfigFileName, runnerauth.CredentialFileName} {
		_ = os.Chown(filepath.Join(runtimeDirectory, name), int(uid), int(gid))
	}
	_ = os.Chown(runtimeDirectory, int(uid), int(gid))
}

// RestoreBootstrapDirectoryOwner 在 bootstrap 中途失败时把固定 runtime 目录归还给 sandboxd。
func RestoreBootstrapDirectoryOwner(bootstrap runnerbootstrap.Config) error {
	return secureDirectory(bootstrap.Paths.RuntimeDirectory, bootstrap.Identity.SocketOwnerUID, bootstrap.Identity.SocketOwnerGID, runtimeDirectoryMode)
}
