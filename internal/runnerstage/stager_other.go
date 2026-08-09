//go:build !linux

// Package runnerstage 在非 Linux 平台拒绝生成依赖 UID/GID 与 no-follow 文件语义的启动材料。
package runnerstage

import (
	"errors"

	"minisandbox/internal/config"
)

// Stager 是非 Linux 平台的显式拒绝占位类型。
type Stager struct{}

// New 明确拒绝在非 Linux 宿主机装配生产 runner 凭据。
func New(config.Config) (*Stager, error) {
	return nil, errors.New("runner bootstrap staging requires Linux")
}

// Stage 明确拒绝生成启动材料。
func (*Stager) Stage(string, string) error {
	return errors.New("runner bootstrap staging requires Linux")
}

// Close 在非 Linux 占位类型上保持幂等。
func (*Stager) Close() error { return nil }
