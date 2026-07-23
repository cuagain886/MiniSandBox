package protocol

import "time"

type SandboxState string

const (
	SandboxStatePending    SandboxState = "Pending"
	SandboxStateCreating   SandboxState = "Creating"
	SandboxStateRunning    SandboxState = "Running"
	SandboxStateStopping   SandboxState = "Stopping"
	SandboxStateTerminated SandboxState = "Terminated"
	SandboxStateFailed     SandboxState = "Failed"
)

type CreateSandboxRequest struct {
	Image      string            `json:"image"`
	Command    []string          `json:"command,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	TTLSeconds int64             `json:"ttl_seconds,omitempty"`
}

type Sandbox struct {
	ID            string       `json:"id"`
	State         SandboxState `json:"state"`
	Image         string       `json:"image"`
	CreatedAt     time.Time    `json:"created_at"`
	ExpiresAt     *time.Time   `json:"expires_at,omitempty"`
	FailureReason string       `json:"failure_reason,omitempty"`
}
