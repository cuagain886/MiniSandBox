package runnerfiles

import "minisandbox/pkg/protocol"

// workspaceRoot 持有打开的 workspace 根目录句柄；具体类型按平台拆分。
type workspaceRoot interface {
	// close 释放根目录 fd；重复调用安全。
	close() error
	// rootFD 返回根目录 fd；不支持 fd 操作的平台返回 -1。
	rootFD() int
}

// Service 是 runner 内 workspace 文件服务的入口。
//
// Service 在构造时打开 workspace 根目录并长期持有 fd；所有操作都从该
// fd 出发解析路径，构造后不再信任任何宿主机绝对路径。Service 可被并发
// 使用；本阶段不为路径建立锁，跨操作一致性由 fd-relative syscall 与
// 原子 rename 保证。
type Service struct {
	root workspaceRoot
}

// Open 打开 workspace 根目录并返回文件服务。
//
// 平台缺少所需 fd-relative syscall（openat2 等）时返回 ErrUnavailable，
// 调用方必须把 files 能力报告为不可用，而不是降级为字符串路径实现。
func Open(workspaceRootPath string) (*Service, error) {
	root, err := openWorkspaceRoot(workspaceRootPath)
	if err != nil {
		return nil, err
	}
	return &Service{root: root}, nil
}

// Close 释放服务持有的根目录 fd。
func (s *Service) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.close()
}

// validatePath 在 syscall 前执行公共路径规则预检。
func validatePath(path string) error {
	if err := protocol.ValidateFilePath(path); err != nil {
		return ErrInvalidPath
	}
	return nil
}
