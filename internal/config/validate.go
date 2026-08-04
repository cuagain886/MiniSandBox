package config

import (
	"net"
	"path"
	"strconv"
	"time"

	"minisandbox/internal/domain"
)

const (
	maxRunnerTimeout                    = 24 * time.Hour
	maxRunnerTerminationGrace           = time.Minute
	maxRunnerConcurrentExecutions       = 256
	maxRunnerRequestBytes         int64 = 16 << 20
	maxRunnerOutputBytes          int64 = 1 << 30
	maxRunnerEnvVars                    = 4096
	maxRunnerEnvKeyBytes                = 1024
	maxRunnerEnvValueBytes              = 1 << 20
	maxRunnerEnvTotalBytes        int64 = 16 << 20
	maxRunnerLogPageEvents              = 4096
	maxRunnerLogPageBytes         int64 = 64 << 20
	maxRunnerRetention                  = 7 * 24 * time.Hour
	maxRunnerRetainedExecutions         = 10_000
	maxRunnerSSEWriteTimeout            = time.Minute
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
// 本方法拒绝:非 loopback 监听、非 linux/amd64 平台、非 none 网络、
// 持久 workspace、超限或非正资源、非绝对宿主机路径,以及互相矛盾的
// TTL、runner 身份、路径和无界 execution limit。无效配置必须使启动失败，
// 不得静默降级为宽松取值。
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

	if err := validateRunner(c.Runner); err != nil {
		return err
	}
	if err := validateAbsolutePath(
		"security.runner_master_key_file",
		c.Security.RunnerMasterKeyFile,
	); err != nil {
		return err
	}
	// egress typed policy 与 sidecar 尚未由 P2-065 及后续任务实现；在此之前
	// 即使配置显式打开也必须启动失败，避免产生“已允许但未隔离”的假象。
	if c.Security.AllowOutbound {
		return &FieldError{
			Field:   "security.allow_outbound",
			Message: "must remain false until managed egress is configured",
		}
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

// ValidateRunnerSocketOwner 校验 runner execution 身份与 Unix Socket 所有者
// 的数字 UID/GID 均不相同。
//
// socket owner 来自 sandboxd 的宿主机有效身份而非 YAML，因此由获得该身份
// 的 bootstrap 调用方显式传入。本方法不访问操作系统，也不切换进程身份。
func (c Config) ValidateRunnerSocketOwner(uid, gid uint32) error {
	if c.Runner.ExecutionUID == uid {
		return &FieldError{
			Field:   "runner.execution_uid",
			Message: "must differ from the runner socket owner UID",
		}
	}
	if c.Runner.ExecutionGID == gid {
		return &FieldError{
			Field:   "runner.execution_gid",
			Message: "must differ from the runner socket owner GID",
		}
	}
	return nil
}

// validateRunner 校验所有 execution 配置均为固定、安全且有界的值。
func validateRunner(r RunnerConfig) error {
	if r.ExecutionUID == 0 {
		return runnerFieldError("execution_uid", "must be a non-root UID")
	}
	if r.ExecutionGID == 0 {
		return runnerFieldError("execution_gid", "must be a non-root GID")
	}
	if r.DefaultCWD != domain.WorkspaceMountPath {
		return runnerFieldError("default_cwd", "must be the fixed workspace mount path")
	}
	if err := validateDurationRange("default_timeout", r.DefaultTimeout, time.Second, maxRunnerTimeout); err != nil {
		return err
	}
	if err := validateDurationRange("max_timeout", r.MaxTimeout, time.Second, maxRunnerTimeout); err != nil {
		return err
	}
	if r.DefaultTimeout > r.MaxTimeout {
		return runnerFieldError("default_timeout", "must not exceed runner.max_timeout")
	}
	if err := validateDurationRange("termination_grace", r.TerminationGrace, time.Millisecond, maxRunnerTerminationGrace); err != nil {
		return err
	}
	if err := validateIntRange("max_concurrent_executions", r.MaxConcurrentExecutions, 1, maxRunnerConcurrentExecutions); err != nil {
		return err
	}
	if err := validateInt64Range("max_request_bytes", r.MaxRequestBytes, 1, maxRunnerRequestBytes); err != nil {
		return err
	}
	if err := validateInt64Range("max_output_bytes", r.MaxOutputBytes, 1, maxRunnerOutputBytes); err != nil {
		return err
	}
	if err := validateIntRange("max_env_vars", r.MaxEnvVars, 1, maxRunnerEnvVars); err != nil {
		return err
	}
	if err := validateIntRange("max_env_key_bytes", r.MaxEnvKeyBytes, 1, maxRunnerEnvKeyBytes); err != nil {
		return err
	}
	if err := validateIntRange("max_env_value_bytes", r.MaxEnvValueBytes, 1, maxRunnerEnvValueBytes); err != nil {
		return err
	}
	if err := validateInt64Range("max_env_total_bytes", r.MaxEnvTotalBytes, 1, maxRunnerEnvTotalBytes); err != nil {
		return err
	}
	if int64(r.MaxEnvKeyBytes)+int64(r.MaxEnvValueBytes) > r.MaxEnvTotalBytes {
		return runnerFieldError("max_env_total_bytes", "must hold at least one maximum-size environment entry")
	}
	if r.MaxEnvTotalBytes > r.MaxRequestBytes {
		return runnerFieldError("max_env_total_bytes", "must not exceed runner.max_request_bytes")
	}
	if err := validateIntRange("max_log_page_events", r.MaxLogPageEvents, 1, maxRunnerLogPageEvents); err != nil {
		return err
	}
	if err := validateInt64Range("max_log_page_bytes", r.MaxLogPageBytes, 1, maxRunnerLogPageBytes); err != nil {
		return err
	}
	if r.MaxLogPageBytes > r.MaxOutputBytes {
		return runnerFieldError("max_log_page_bytes", "must not exceed runner.max_output_bytes")
	}
	if err := validateDurationRange("completed_retention", r.CompletedRetention, time.Second, maxRunnerRetention); err != nil {
		return err
	}
	if err := validateIntRange("max_retained_executions", r.MaxRetainedExecutions, 1, maxRunnerRetainedExecutions); err != nil {
		return err
	}
	if err := validateDurationRange("sse_write_timeout", r.SSEWriteTimeout, time.Millisecond, maxRunnerSSEWriteTimeout); err != nil {
		return err
	}
	return nil
}

func validateDurationRange(field string, value, minimum, maximum time.Duration) error {
	if value < minimum || value > maximum {
		return runnerFieldError(field, "must be within the safe duration bounds")
	}
	return nil
}

func validateIntRange(field string, value, minimum, maximum int) error {
	if value < minimum || value > maximum {
		return runnerFieldError(field, "must be within the safe integer bounds")
	}
	return nil
}

func validateInt64Range(field string, value, minimum, maximum int64) error {
	if value < minimum || value > maximum {
		return runnerFieldError(field, "must be within the safe byte bounds")
	}
	return nil
}

func runnerFieldError(field, message string) error {
	return &FieldError{Field: "runner." + field, Message: message}
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
