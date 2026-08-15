package domain

import "errors"

var (
	// ErrNotFound 表示请求的领域对象不存在。
	ErrNotFound = errors.New("not found")
	// ErrConflict 表示请求与当前资源状态或幂等记录冲突。
	ErrConflict = errors.New("conflict")
	// ErrInvalid 表示请求违反领域不变量。
	ErrInvalid = errors.New("invalid request")
	// ErrNotImplemented 表示初始化骨架尚未实现对应能力。
	ErrNotImplemented = errors.New("not implemented")
	// ErrSandboxNotRunning 表示 sandbox 当前状态不允许接受 execution。
	ErrSandboxNotRunning = errors.New("sandbox not running")
	// ErrInvalidExecutionRequest 表示 execution 请求违反协议或领域不变量。
	ErrInvalidExecutionRequest = errors.New("invalid execution request")
	// ErrExecutionNotFound 表示 execution 不存在或已超过保留期。
	ErrExecutionNotFound = errors.New("execution not found")
	// ErrExecutionLimitReached 表示 sandbox 当前 execution 并发额度已满。
	ErrExecutionLimitReached = errors.New("execution limit reached")
	// ErrShellNotFound 表示显式 shell 请求无法解析到受支持的 shell。
	ErrShellNotFound = errors.New("shell not found")
	// ErrInvalidCWD 表示 cwd 无效或逃逸出 workspace 根目录。
	ErrInvalidCWD = errors.New("invalid cwd")
	// ErrRunnerUnhealthy 表示 runner 未通过当前请求所需的健康验证。
	ErrRunnerUnhealthy = errors.New("runner unhealthy")
	// ErrRunnerProtocolMismatch 表示 runner 与控制面的协议版本不兼容。
	ErrRunnerProtocolMismatch = errors.New("runner protocol mismatch")
	// ErrOutboundNotAllowed 表示服务端策略不允许创建 outbound sandbox。
	ErrOutboundNotAllowed = errors.New("outbound not allowed")
	// ErrEgressImageUnavailable 表示固定 egress 镜像暂时不可取得或验证。
	ErrEgressImageUnavailable = errors.New("egress image unavailable")
	// ErrEgressPolicyInvalid 表示服务端 egress 策略不能安全编译或验证。
	ErrEgressPolicyInvalid = errors.New("egress policy invalid")
	// ErrEgressNotReady 表示 egress sidecar 尚未完成启动证明。
	ErrEgressNotReady = errors.New("egress not ready")
	// ErrEgressUnhealthy 表示既有 egress sidecar 的安全证明已失效。
	ErrEgressUnhealthy = errors.New("egress unhealthy")
	// ErrInvalidTTL 表示 create TTL 缺失默认之外的显式值不满足服务端边界。
	ErrInvalidTTL = errors.New("invalid sandbox TTL")
	// ErrInvalidExpiration 表示 renew 的绝对到期时间格式或服务端边界非法。
	ErrInvalidExpiration = errors.New("invalid sandbox expiration")
	// ErrLeaseConflict 表示 renew 试图缩短租约或并发记录已经包含更晚到期时间。
	ErrLeaseConflict = errors.New("sandbox lease conflict")
	// ErrSandboxExpiring 表示 sandbox 已过期或终止意图已经提交，不能再续期。
	ErrSandboxExpiring = errors.New("sandbox is expiring")
	// ErrIdempotencyConflict 表示同一幂等 key 已经绑定到不同的创建请求。
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	// ErrSandboxLimitReached 表示 active sandbox 数量达到准入上限，稍后重试可能成功。
	ErrSandboxLimitReached = errors.New("sandbox limit reached")
	// ErrAdminDisabled 表示管理面未启用；HTTP 层必须用 404 隐藏该 surface。
	ErrAdminDisabled = errors.New("admin API disabled")
	// ErrFilesUnavailable 表示当前 sandbox 未提供文件能力。
	ErrFilesUnavailable = errors.New("files capability unavailable")
	// ErrInvalidFilePath 表示路径违反 workspace 相对路径规则。
	ErrInvalidFilePath = errors.New("invalid workspace file path")
	// ErrFileNotFound 表示目标文件或目录不存在。
	ErrFileNotFound = errors.New("workspace file not found")
	// ErrFileTypeMismatch 表示操作目标类型不符。
	ErrFileTypeMismatch = errors.New("workspace file type mismatch")
	// ErrFileConflict 表示非覆盖写入或移动遇到已存在目标等冲突。
	ErrFileConflict = errors.New("workspace file conflict")
	// ErrFileTooLarge 表示上传或下载超过配置上限。
	ErrFileTooLarge = errors.New("workspace file too large")
	// ErrPTYUnavailable 表示当前 sandbox 未提供 PTY 能力。
	ErrPTYUnavailable = errors.New("pty capability unavailable")
	// ErrPTYLimitReached 表示当前 sandbox 的 PTY 并发上限已满。
	ErrPTYLimitReached = errors.New("pty session limit reached")
)
