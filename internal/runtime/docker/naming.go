package docker

import (
	"errors"
	"path/filepath"
	"strings"
)

const (
	containerNamePrefix        = "minisandbox-"
	workspaceNamePrefix        = "minisandbox-workspace-"
	runtimeRootName            = "run"
	runnerSocketName           = "runner.sock"
	maxDockerResourceNameBytes = 128
	maxManagedPathBytes        = 4096

	// GuestRunnerSocketPath 是 runner 在 sandbox 容器内监听的固定 Unix Socket。
	GuestRunnerSocketPath = "/run/minisandbox/runner.sock"
)

// ResourceNames 保存单个 sandbox 的确定性 Docker 名称和宿主机 runtime 路径。
type ResourceNames struct {
	// Container 是受管 Docker container 的确定性名称。
	Container string
	// Workspace 是受管 Docker named volume 的确定性名称。
	Workspace string
	// RuntimeDirectory 是只供该 sandbox 使用的宿主机 socket 目录。
	RuntimeDirectory string
	// HostRunnerSocket 是 runner Unix Socket 的宿主机路径。
	HostRunnerSocket string
}

// NamesForSandbox 从受管 data directory 和规范 sandbox ID 计算全部资源名称。
//
// 本函数只做纯计算，不读取或修改文件系统。返回前会再次证明 runtime
// directory 位于 `<dataDirectory>/run` 之下，不能用任意字符串拼接路径。
func NamesForSandbox(
	dataDirectory string,
	sandboxID string,
) (ResourceNames, error) {
	if !filepath.IsAbs(dataDirectory) {
		return ResourceNames{}, errors.New("data directory must be absolute")
	}
	if !validSandboxID(sandboxID) {
		return ResourceNames{}, errors.New("sandbox ID is invalid")
	}

	container := containerName(sandboxID)
	workspace := workspaceName(sandboxID)
	if len(container) > maxDockerResourceNameBytes ||
		len(workspace) > maxDockerResourceNameBytes {
		return ResourceNames{}, errors.New("Docker resource name is too long")
	}

	runRoot := filepath.Join(filepath.Clean(dataDirectory), runtimeRootName)
	runtimeDirectory := filepath.Join(runRoot, sandboxID)
	relative, err := filepath.Rel(runRoot, runtimeDirectory)
	if err != nil ||
		relative == "." ||
		filepath.IsAbs(relative) ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ResourceNames{}, errors.New("runtime directory escapes managed run root")
	}
	hostSocket := filepath.Join(runtimeDirectory, runnerSocketName)
	if len(runtimeDirectory) > maxManagedPathBytes ||
		len(hostSocket) > maxManagedPathBytes {
		return ResourceNames{}, errors.New("managed runtime path is too long")
	}

	return ResourceNames{
		Container:        container,
		Workspace:        workspace,
		RuntimeDirectory: runtimeDirectory,
		HostRunnerSocket: hostSocket,
	}, nil
}

// containerName 返回规范 ID 对应的 Docker container 名称。
func containerName(sandboxID string) string {
	return containerNamePrefix + sandboxID
}

// workspaceName 返回规范 ID 对应的 Docker named volume 名称。
func workspaceName(sandboxID string) string {
	return workspaceNamePrefix + sandboxID
}
