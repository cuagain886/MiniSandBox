package protocol

import "time"

type ExecuteRequest struct {
	Argv       []string          `json:"argv,omitempty"`
	Shell      string            `json:"shell,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Timeout    time.Duration     `json:"timeout,omitempty"`
}

type ExecuteAccepted struct {
	ExecutionID string `json:"execution_id"`
}

type ExitResult struct {
	ExitCode int  `json:"exit_code"`
	TimedOut bool `json:"timed_out,omitempty"`
	Canceled bool `json:"canceled,omitempty"`
}
