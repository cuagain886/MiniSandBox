package application

import (
	"time"

	"minisandbox/internal/domain"
)

type CreateSandbox struct {
	Image      string
	Command    []string
	Env        map[string]string
	TTL        time.Duration
	RequestKey string
}

type DeleteSandbox struct {
	SandboxID string
}

type Execute struct {
	SandboxID string
	Spec      domain.ExecutionSpec
}
