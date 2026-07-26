package config

import (
	"net"
	"path"
	"strconv"

	"minisandbox/internal/domain"
)

// FieldError 表示配置中单个字段违反启动前的安全校验规则。
//
// 与领域校验的错误约定一致:Field 使用稳定的配置字段路径定位问题,
// Message 只描述规则本身,不回显字段取值。
type FieldError struct {
	// Field 是违规配置字段的稳定路径,与 YAML 键名一致。
	Field string
	// Message 是不含字段取值的安全规则说明。
	Message string
}

// Error 返回 "字段路径: 规则说明" 形式的诊断文本。
func (e *FieldError) Error() string {
	return e.Field + ": " + e.Message
}

// Validate 在启动前一次性校验配置的安全边界,返回第一处违规。
//
// Phase 1 拒绝:非 loopback 监听、非 linux/amd64 平台、非 none 网络、
// 持久 workspace、超限或非正资源、非绝对宿主机路径,以及互相矛盾的
// TTL 与非正时长。无效配置必须使启动失败,不得静默降级为宽松取值。
// 本方法只做判定,不监听端口、不访问文件系统。
func (c Config) Validate() error {
	if err := validateLoopbackAddress(
		"server.listen_address",
		c.Server.ListenAddress,
	); err != nil {
		return err
	}
	if c.Server.ShutdownTimeout <= 0 {
		return &FieldError{
			Field:   "server.shutdown_timeout",
			Message: "must be a positive duration",
		}
	}

	if err := validateAbsolutePath("data.directory", c.Data.Directory); err != nil {
		return err
	}
	if err := validateAbsolutePath("data.sqlite_path", c.Data.SQLitePath); err != nil {
		return err
	}

	if c.Runtime.Type != "docker" {
		return &FieldError{
			Field:   "runtime.type",
			Message: "only docker is supported in Phase 1",
		}
	}
	if c.Runtime.DockerHost == "" {
		return &FieldError{
			Field:   "runtime.docker_host",
			Message: "must not be empty",
		}
	}
	if c.Runtime.DefaultImage == "" {
		return &FieldError{
			Field:   "runtime.default_image",
			Message: "must not be empty",
		}
	}
	if len(c.Runtime.DefaultImage) > domain.MaxImageReferenceLength {
		return &FieldError{
			Field:   "runtime.default_image",
			Message: "image reference is too long",
		}
	}
	if err := validateAbsolutePath(
		"runtime.runner_socket_directory",
		c.Runtime.RunnerSocketDirectory,
	); err != nil {
		return err
	}
	if err := validateAbsolutePath(
		"runtime.workspace_directory",
		c.Runtime.WorkspaceDirectory,
	); err != nil {
		return err
	}
	if c.Runtime.NetworkMode != NetworkModeNone {
		return &FieldError{
			Field:   "runtime.network_mode",
			Message: "only none is supported in Phase 1",
		}
	}
	if c.Runtime.WorkspacePersistent {
		return &FieldError{
			Field:   "runtime.workspace_persistent",
			Message: "persistent workspace is not supported in Phase 1",
		}
	}
	if c.Runtime.Platform.OS != "linux" {
		return &FieldError{
			Field:   "runtime.platform.os",
			Message: "only linux is supported in Phase 1",
		}
	}
	if c.Runtime.Platform.Arch != "amd64" {
		return &FieldError{
			Field:   "runtime.platform.arch",
			Message: "only amd64 is supported in Phase 1",
		}
	}

	if c.Limits.DefaultTTL <= 0 {
		return &FieldError{
			Field:   "limits.default_ttl",
			Message: "must be a positive duration",
		}
	}
	if c.Limits.MaximumTTL <= 0 {
		return &FieldError{
			Field:   "limits.maximum_ttl",
			Message: "must be a positive duration",
		}
	}
	if c.Limits.DefaultTTL > c.Limits.MaximumTTL {
		return &FieldError{
			Field:   "limits.default_ttl",
			Message: "must not exceed limits.maximum_ttl",
		}
	}
	if err := validateResourceRange(
		"limits.max_resources.cpu_quota_millis",
		c.Limits.MaxResources.MaxCPUQuotaMillis,
		c.Limits.MaxResources.MaxCPUQuotaMillis,
	); err != nil {
		return err
	}
	if err := validateResourceRange(
		"limits.max_resources.memory_mib",
		c.Limits.MaxResources.MaxMemoryMiB,
		c.Limits.MaxResources.MaxMemoryMiB,
	); err != nil {
		return err
	}
	if err := validateResourceRange(
		"limits.max_resources.pids",
		c.Limits.MaxResources.MaxPIDs,
		c.Limits.MaxResources.MaxPIDs,
	); err != nil {
		return err
	}
	if err := validateResourceRange(
		"limits.default_resources.cpu_quota_millis",
		c.Limits.DefaultResources.CPUQuotaMillis,
		c.Limits.MaxResources.MaxCPUQuotaMillis,
	); err != nil {
		return err
	}
	if err := validateResourceRange(
		"limits.default_resources.memory_mib",
		c.Limits.DefaultResources.MemoryMiB,
		c.Limits.MaxResources.MaxMemoryMiB,
	); err != nil {
		return err
	}
	if err := validateResourceRange(
		"limits.default_resources.pids",
		c.Limits.DefaultResources.PIDs,
		c.Limits.MaxResources.MaxPIDs,
	); err != nil {
		return err
	}

	if c.Reconcile.Interval <= 0 {
		return &FieldError{
			Field:   "reconcile.interval",
			Message: "must be a positive duration",
		}
	}
	if c.Reconcile.RunnerReadyTimeout <= 0 {
		return &FieldError{
			Field:   "reconcile.runner_ready_timeout",
			Message: "must be a positive duration",
		}
	}
	if c.Reconcile.DeletionTimeout <= 0 {
		return &FieldError{
			Field:   "reconcile.deletion_timeout",
			Message: "must be a positive duration",
		}
	}

	return nil
}

// validateLoopbackAddress 要求地址为 host:port 且 host 为 loopback。
//
// Phase 1 的控制面不得作为公网服务发布,只接受 localhost 或 loopback IP。
func validateLoopbackAddress(field, address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return &FieldError{
			Field:   field,
			Message: "must be a host:port address",
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return &FieldError{
			Field:   field,
			Message: "port must be between 1 and 65535",
		}
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return &FieldError{
			Field:   field,
			Message: "host must be a loopback address in Phase 1",
		}
	}
	return nil
}

// validateAbsolutePath 要求宿主机路径为非空的绝对路径。
//
// 配置描述的是 Linux 宿主机路径,因此使用 POSIX 语义判断绝对路径,
// 不受开发机操作系统影响。
func validateAbsolutePath(field, value string) error {
	if !path.IsAbs(value) {
		return &FieldError{
			Field:   field,
			Message: "must be an absolute path",
		}
	}
	return nil
}

// validateResourceRange 要求资源取值为正数且不超过服务端上限。
//
// 上限字段自身校验时 value 与 max 相同,等价于只要求正数。
func validateResourceRange(field string, value, max int64) error {
	if value < 1 || value > max {
		return &FieldError{
			Field:   field,
			Message: "must be positive and within the server maximum",
		}
	}
	return nil
}
