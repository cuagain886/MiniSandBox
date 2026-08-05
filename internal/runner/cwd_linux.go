//go:build linux

package runner

import (
	"os"
	"path/filepath"
	"strings"
)

// ValidateCWD 把请求 cwd 解析为 workspace 内已存在且每层均为真实目录的绝对路径。
// requested 为空时使用 workspaceRoot；任何 symlink 即使仍指向 workspace 内也会被拒绝。
func ValidateCWD(workspaceRoot, requested string) (string, error) {
	if !filepath.IsAbs(workspaceRoot) {
		return "", ErrInvalidCWD
	}
	root := filepath.Clean(workspaceRoot)
	if requested == "" {
		requested = root
	}
	if !filepath.IsAbs(requested) {
		return "", ErrInvalidCWD
	}
	target := filepath.Clean(requested)
	relative, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrInvalidCWD
	}
	if err := requireCWDDirectory(root); err != nil {
		return "", err
	}
	if relative == "." {
		return root, nil
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return "", ErrInvalidCWD
		}
		current = filepath.Join(current, component)
		if err := requireCWDDirectory(current); err != nil {
			return "", err
		}
	}
	return target, nil
}

func requireCWDDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrInvalidCWD
	}
	return nil
}
