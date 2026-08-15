package config

import (
	"net"
	"net/netip"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/internal/egresspolicy"
)

const (
	maxRunnerTimeout                       = 24 * time.Hour
	maxRunnerTerminationGrace              = time.Minute
	maxRunnerConcurrentExecutions          = 256
	maxRunnerRequestBytes            int64 = 16 << 20
	maxRunnerOutputBytes             int64 = 1 << 30
	maxRunnerEnvVars                       = 4096
	maxRunnerEnvKeyBytes                   = 1024
	maxRunnerEnvValueBytes                 = 1 << 20
	maxRunnerEnvTotalBytes           int64 = 16 << 20
	maxRunnerLogPageEvents                 = 4096
	maxRunnerLogPageBytes            int64 = 64 << 20
	maxRunnerRetention                     = 7 * 24 * time.Hour
	maxRunnerRetainedExecutions            = 10_000
	maxRunnerSSEWriteTimeout               = time.Minute
	maxEgressReadyTimeout                  = 2 * time.Minute
	maxEgressCPUQuotaMillis          int64 = 1000
	maxEgressMemoryMiB               int64 = 512
	maxEgressPIDs                    int64 = 64
	maxSandboxes                           = 10_000
	maxLifecycleOperationConcurrency       = 256
	maxReconcilePageSize                   = 10_000
	maxReconcileWorkers                    = 256
	maxRunnerUnhealthyThreshold            = 100
	maxIdempotencyKeyBytes                 = 128
	maxIdempotencyTerminalRetention        = 30 * 24 * time.Hour
)

var egressImageDigestPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[0-9a-f]{64}$`)

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
// TTL、reconcile/retry、runner 身份、路径和无界 operation limit。无效配置必须使启动失败，
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

	if c.Limits.MinimumTTL <= 0 {
		return &FieldError{
			Field:   "limits.minimum_ttl",
			Message: "must be a positive duration",
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
	if c.Limits.MinimumTTL > c.Limits.DefaultTTL {
		return &FieldError{
			Field:   "limits.minimum_ttl",
			Message: "must not exceed limits.default_ttl",
		}
	}
	if c.Limits.DefaultTTL > c.Limits.MaximumTTL {
		return &FieldError{
			Field:   "limits.default_ttl",
			Message: "must not exceed limits.maximum_ttl",
		}
	}
	if err := validateConfigIntRange("limits.max_sandboxes", c.Limits.MaxSandboxes, maxSandboxes); err != nil {
		return err
	}
	if err := validateConfigIntRange("limits.max_concurrent_creates", c.Limits.MaxConcurrentCreates, maxLifecycleOperationConcurrency); err != nil {
		return err
	}
	if err := validateConfigIntRange("limits.max_concurrent_image_pulls", c.Limits.MaxConcurrentImagePulls, maxLifecycleOperationConcurrency); err != nil {
		return err
	}
	if err := validateConfigIntRange("limits.max_concurrent_deletes", c.Limits.MaxConcurrentDeletes, maxLifecycleOperationConcurrency); err != nil {
		return err
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
	if err := validateEgress(c.Egress, c.Security.AllowOutbound); err != nil {
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

	if err := validateReconcile(c.Reconcile); err != nil {
		return err
	}
	if err := validateIdempotency(c.Idempotency); err != nil {
		return err
	}
	if c.Admin.Enabled {
		if err := validateAbsolutePath("admin.token_file", c.Admin.TokenFile); err != nil {
			return err
		}
	}
	if err := validateFiles(c.Files); err != nil {
		return err
	}
	if err := validatePTY(c.PTY); err != nil {
		return err
	}
	if err := validatePortProxy(c.PortProxy); err != nil {
		return err
	}
	if err := validatePrePullImages(c.Runtime.PrePullImages); err != nil {
		return err
	}

	return nil
}

// validateFiles 校验文件能力上限有界。
func validateFiles(f FilesConfig) error {
	if f.MaxUploadBytes <= 0 {
		return &FieldError{Field: "files.max_upload_bytes", Message: "must be positive"}
	}
	if f.MaxDownloadBytes <= 0 {
		return &FieldError{Field: "files.max_download_bytes", Message: "must be positive"}
	}
	return nil
}

// validatePTY 校验 PTY 会话上限与默认时长有界。
func validatePTY(p PTYConfig) error {
	if p.MaxConcurrentSessions <= 0 {
		return &FieldError{
			Field:   "pty.max_concurrent_sessions",
			Message: "must be positive",
		}
	}
	if p.DefaultTimeout <= 0 {
		return &FieldError{Field: "pty.default_timeout", Message: "must be positive"}
	}
	return nil
}

// validatePortProxy 校验端口范围合法且自洽。
func validatePortProxy(p PortProxyConfig) error {
	if p.MinPort < 1 || p.MaxPort > 65535 || p.MinPort > p.MaxPort {
		return &FieldError{
			Field:   "port_proxy.min_port/max_port",
			Message: "port range must satisfy 1 <= min <= max <= 65535",
		}
	}
	return nil
}

// validatePrePullImages 校验预拉取镜像列表有界且字段完整。
func validatePrePullImages(images []PrePullImage) error {
	if len(images) > 16 {
		return &FieldError{
			Field:   "runtime.prepull_images",
			Message: "too many entries",
		}
	}
	for index, entry := range images {
		if entry.Image == "" {
			return &FieldError{
				Field:   "runtime.prepull_images",
				Message: "image reference must not be empty",
			}
		}
		if len(entry.Image) > domain.MaxImageReferenceLength {
			return &FieldError{
				Field:   "runtime.prepull_images",
				Message: "image reference is too long",
			}
		}
		os, arch, found := strings.Cut(entry.Platform, "/")
		if !found || os == "" || arch == "" || strings.Contains(arch, "/") {
			return &FieldError{
				Field:   "runtime.prepull_images",
				Message: "platform must look like linux/amd64",
			}
		}
		_ = index
	}
	return nil
}

// validateReconcile 校验 scanner、retry、health 和 worker 配置有界且自洽。
func validateReconcile(r ReconcileConfig) error {
	positiveDurations := []struct {
		field string
		value time.Duration
	}{
		{"reconcile.interval", r.Interval},
		{"reconcile.timeout", r.Timeout},
		{"reconcile.retry_min", r.RetryMin},
		{"reconcile.retry_max", r.RetryMax},
		{"reconcile.running_check_interval", r.RunningCheckInterval},
		{"reconcile.docker_freshness", r.DockerFreshness},
		{"reconcile.runner_ready_timeout", r.RunnerReadyTimeout},
		{"reconcile.deletion_timeout", r.DeletionTimeout},
	}
	for _, item := range positiveDurations {
		if item.value <= 0 {
			return &FieldError{Field: item.field, Message: "must be a positive duration"}
		}
	}
	if r.Jitter < 0 || r.Jitter > r.Interval {
		return &FieldError{Field: "reconcile.jitter", Message: "must be non-negative and not exceed reconcile.interval"}
	}
	if r.RetryMin > r.RetryMax {
		return &FieldError{Field: "reconcile.retry_min", Message: "must not exceed reconcile.retry_max"}
	}
	if err := validateConfigIntRange("reconcile.page_size", r.PageSize, maxReconcilePageSize); err != nil {
		return err
	}
	if err := validateConfigIntRange("reconcile.max_concurrent", r.MaxConcurrent, maxReconcileWorkers); err != nil {
		return err
	}
	return validateConfigIntRange("reconcile.runner_unhealthy_threshold", r.RunnerUnhealthyThreshold, maxRunnerUnhealthyThreshold)
}

// validateIdempotency 校验 key 和 GC retention 均有界，且 GC 周期不会超过保留期。
func validateIdempotency(i IdempotencyConfig) error {
	if err := validateConfigIntRange("idempotency.max_key_bytes", i.MaxKeyBytes, maxIdempotencyKeyBytes); err != nil {
		return err
	}
	if i.TerminalRetention <= 0 || i.TerminalRetention > maxIdempotencyTerminalRetention {
		return &FieldError{Field: "idempotency.terminal_retention", Message: "must be within the safe duration bounds"}
	}
	if i.GCInterval <= 0 || i.GCInterval > i.TerminalRetention {
		return &FieldError{Field: "idempotency.gc_interval", Message: "must be positive and not exceed idempotency.terminal_retention"}
	}
	return nil
}

// validateConfigIntRange 要求服务级 count 大于零且不超过固定安全上限。
func validateConfigIntRange(field string, value, maximum int) error {
	if value < 1 || value > maximum {
		return &FieldError{Field: field, Message: "must be within the safe integer bounds"}
	}
	return nil
}

func validateEgress(egress EgressConfig, enabled bool) error {
	if enabled && egress.Image == "" {
		return &FieldError{Field: "egress.image", Message: "must be configured when outbound is enabled"}
	}
	if egress.Image != "" && !egressImageDigestPattern.MatchString(egress.Image) {
		return &FieldError{Field: "egress.image", Message: "must be an OCI sha256 digest reference"}
	}
	if egress.ProtocolVersion != egresspolicy.CurrentProtocolVersion {
		return &FieldError{Field: "egress.protocol_version", Message: "must equal the supported protocol version"}
	}
	if egress.ReadyTimeout < time.Second || egress.ReadyTimeout > maxEgressReadyTimeout {
		return &FieldError{Field: "egress.ready_timeout", Message: "must be within the safe duration bounds"}
	}
	if egress.AnchorUID == 0 {
		return &FieldError{Field: "egress.anchor_uid", Message: "must be a non-root UID"}
	}
	if egress.AnchorGID == 0 {
		return &FieldError{Field: "egress.anchor_gid", Message: "must be a non-root GID"}
	}
	for _, cidr := range egress.DeniedCIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || prefix.Addr().Is4In6() {
			return &FieldError{Field: "egress.egress_denied_cidrs", Message: "must contain only valid IPv4 or IPv6 CIDR values"}
		}
	}
	if egress.Limits.CPUQuotaMillis <= 0 || egress.Limits.CPUQuotaMillis > maxEgressCPUQuotaMillis {
		return &FieldError{Field: "egress.limits.cpu_quota_millis", Message: "must be within the safe sidecar bounds"}
	}
	if egress.Limits.MemoryMiB <= 0 || egress.Limits.MemoryMiB > maxEgressMemoryMiB {
		return &FieldError{Field: "egress.limits.memory_mib", Message: "must be within the safe sidecar bounds"}
	}
	if egress.Limits.PIDs <= 0 || egress.Limits.PIDs > maxEgressPIDs {
		return &FieldError{Field: "egress.limits.pids", Message: "must be within the safe sidecar bounds"}
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
