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
)
