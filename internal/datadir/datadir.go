// Package datadir 负责创建并校验 sandboxd 的受管数据目录。
//
// 本模块只操作已通过配置校验的绝对路径,负责 data 根目录、runner socket
// 根目录和数据库父目录的幂等创建与基本安全检查。它不创建单个 sandbox 的
// runtime 目录,不解析配置,也不打开数据库。
package datadir

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths 保存受管目录准备完成后的确定路径,供装配层直接使用。
type Paths struct {
	// DataDirectory 是受管数据根目录的绝对路径。
	DataDirectory string
	// DatabasePath 是 SQLite 数据库文件的绝对路径,本包只保证其父目录存在。
	DatabasePath string
	// RunRoot 是各 sandbox runner socket 目录的绝对根路径。
	RunRoot string
}

// Ensure 幂等创建 data 根目录、数据库父目录和 run 根目录并返回确定路径。
//
// 三个输入必须是绝对路径;目录以 0700 创建并收敛权限,拒绝 symlink 或
// 非目录占位。配置校验使用 Linux 宿主机的词法规则,而本函数操作真实
// 文件系统,因此使用当前操作系统的路径语义。重复调用返回相同结果。
func Ensure(dataDirectory, databasePath, runRoot string) (Paths, error) {
	// 按固定顺序校验,保证多处违规时报错结果确定。
	inputs := []struct {
		name  string
		value string
	}{
		{name: "data directory", value: dataDirectory},
		{name: "database path", value: databasePath},
		{name: "run root", value: runRoot},
	}
	for _, input := range inputs {
		if !filepath.IsAbs(input.value) {
			return Paths{}, fmt.Errorf(
				"%s must be an absolute path",
				input.name,
			)
		}
	}

	paths := Paths{
		DataDirectory: filepath.Clean(dataDirectory),
		DatabasePath:  filepath.Clean(databasePath),
		RunRoot:       filepath.Clean(runRoot),
	}

	for _, directory := range []string{
		paths.DataDirectory,
		filepath.Dir(paths.DatabasePath),
		paths.RunRoot,
	} {
		if err := ensureDirectory(directory); err != nil {
			return Paths{}, err
		}
	}
	return paths, nil
}

// ensureDirectory 幂等创建单个受管目录并校验其安全形态。
//
// 目录已存在时要求它是真实目录:symlink 会把受管数据引导到不受控位置,
// 普通文件说明路径被占用,二者都必须拒绝而不是继续使用。
func ensureDirectory(directory string) error {
	info, err := os.Lstat(directory)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf(
				"managed directory %s is a symlink",
				directory,
			)
		}
		if !info.IsDir() {
			return fmt.Errorf(
				"managed directory %s is not a directory",
				directory,
			)
		}
	case os.IsNotExist(err):
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create managed directory: %w", err)
		}
	default:
		return fmt.Errorf("inspect managed directory: %w", err)
	}

	// 收敛权限,保证重复调用后受管目录始终保持 0700。
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("restrict managed directory mode: %w", err)
	}
	return nil
}
