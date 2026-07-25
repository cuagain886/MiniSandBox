package protocol

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
