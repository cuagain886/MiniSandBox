package protocol

// ErrorCode 是公共 HTTP API 的稳定机器可读错误码。
type ErrorCode string

const (
	// ErrorCodeSandboxNotRunning 表示目标 sandbox 当前不能接受 execution。
	ErrorCodeSandboxNotRunning ErrorCode = "SANDBOX_NOT_RUNNING"
	// ErrorCodeInvalidExecutionRequest 表示 execution 请求违反公共契约。
	ErrorCodeInvalidExecutionRequest ErrorCode = "INVALID_EXECUTION_REQUEST"
	// ErrorCodeExecutionNotFound 表示目标 execution 不存在或已不再保留。
	ErrorCodeExecutionNotFound ErrorCode = "EXECUTION_NOT_FOUND"
	// ErrorCodeExecutionLimitReached 表示当前 sandbox 的 execution 并发额度已满。
	ErrorCodeExecutionLimitReached ErrorCode = "EXECUTION_LIMIT_REACHED"
	// ErrorCodeShellNotFound 表示显式 shell 请求无法解析到受支持的 shell。
	ErrorCodeShellNotFound ErrorCode = "SHELL_NOT_FOUND"
	// ErrorCodeInvalidCWD 表示 cwd 无效或逃逸出 /workspace。
	ErrorCodeInvalidCWD ErrorCode = "INVALID_CWD"
	// ErrorCodeRunnerUnhealthy 表示当前 sandbox 的 runner 未通过健康验证。
	ErrorCodeRunnerUnhealthy ErrorCode = "RUNNER_UNHEALTHY"
	// ErrorCodeRunnerProtocolMismatch 表示控制面与 runner 的协议版本不兼容。
	ErrorCodeRunnerProtocolMismatch ErrorCode = "RUNNER_PROTOCOL_MISMATCH"
	// ErrorCodeOutboundNotAllowed 表示服务端策略未允许创建 outbound sandbox。
	ErrorCodeOutboundNotAllowed ErrorCode = "OUTBOUND_NOT_ALLOWED"
	// ErrorCodeEgressImageUnavailable 表示固定 digest 的 egress 镜像暂时不可取得。
	ErrorCodeEgressImageUnavailable ErrorCode = "EGRESS_IMAGE_UNAVAILABLE"
	// ErrorCodeEgressPolicyInvalid 表示服务端 egress 策略无法安全编译或验证。
	ErrorCodeEgressPolicyInvalid ErrorCode = "EGRESS_POLICY_INVALID"
	// ErrorCodeEgressNotReady 表示 egress sidecar 尚未完成启动证明。
	ErrorCodeEgressNotReady ErrorCode = "EGRESS_NOT_READY"
	// ErrorCodeEgressUnhealthy 表示已创建 sandbox 的 egress 安全证明失效。
	ErrorCodeEgressUnhealthy ErrorCode = "EGRESS_UNHEALTHY"
	// ErrorCodeInvalidExpiration 表示 renew 到期时间格式或服务端边界非法。
	ErrorCodeInvalidExpiration ErrorCode = "INVALID_EXPIRATION"
	// ErrorCodeLeaseConflict 表示 renew 试图缩短租约或与更晚的并发续期冲突。
	ErrorCodeLeaseConflict ErrorCode = "LEASE_CONFLICT"
	// ErrorCodeSandboxExpiring 表示 sandbox 已过期或终止意图已经提交，不能续期。
	ErrorCodeSandboxExpiring ErrorCode = "SANDBOX_EXPIRING"
	// ErrorCodeIdempotencyConflict 表示同一幂等 key 已绑定到不同创建请求。
	ErrorCodeIdempotencyConflict ErrorCode = "IDEMPOTENCY_CONFLICT"
	// ErrorCodeInvalidTTL 表示创建租约时长不在服务端允许范围内。
	ErrorCodeInvalidTTL ErrorCode = "INVALID_TTL"
	// ErrorCodeSandboxLimitReached 表示 active sandbox 数量已经达到准入上限。
	ErrorCodeSandboxLimitReached ErrorCode = "SANDBOX_LIMIT_REACHED"
	// ErrorCodeAdminDisabled 表示只读管理面未启用，并以 404 隐藏其存在性。
	ErrorCodeAdminDisabled ErrorCode = "ADMIN_DISABLED"
	// ErrorCodeInvalidFilePath 表示文件路径违反 workspace 相对路径规则。
	ErrorCodeInvalidFilePath ErrorCode = "INVALID_FILE_PATH"
	// ErrorCodeFileNotFound 表示目标文件或目录不存在。
	ErrorCodeFileNotFound ErrorCode = "FILE_NOT_FOUND"
	// ErrorCodeFileTypeMismatch 表示操作目标类型不符，例如对目录下载内容。
	ErrorCodeFileTypeMismatch ErrorCode = "FILE_TYPE_MISMATCH"
	// ErrorCodeFileConflict 表示非覆盖写入遇到已存在目标等冲突。
	ErrorCodeFileConflict ErrorCode = "FILE_CONFLICT"
	// ErrorCodeFileTooLarge 表示上传或下载超过配置的大小上限。
	ErrorCodeFileTooLarge ErrorCode = "FILE_TOO_LARGE"
	// ErrorCodeFilesUnavailable 表示当前 sandbox 未提供文件能力。
	ErrorCodeFilesUnavailable ErrorCode = "FILES_UNAVAILABLE"
	// ErrorCodePTYUnavailable 表示当前 sandbox 未提供 PTY 能力。
	ErrorCodePTYUnavailable ErrorCode = "PTY_UNAVAILABLE"
	// ErrorCodePTYLimitReached 表示当前 sandbox 的 PTY 并发上限已满。
	ErrorCodePTYLimitReached ErrorCode = "PTY_LIMIT_REACHED"
)

// ErrorResponse 是所有公共 HTTP 错误共用的 JSON envelope。
type ErrorResponse struct {
	// Error 保存稳定错误码、安全消息、请求标识和重试语义。
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 描述调用方可以安全读取的公共错误。
type ErrorDetail struct {
	// Code 是稳定、机器可读且可向后兼容扩展的错误码。
	Code string `json:"code"`
	// Message 是安全的人类可读说明，不得包含秘密或内部 cause。
	Message string `json:"message"`
	// RequestID 是关联响应头、服务端日志和诊断记录的请求标识。
	RequestID string `json:"request_id"`
	// Retryable 表示保持请求不变并稍后重试是否可能成功。
	Retryable bool `json:"retryable"`
}
