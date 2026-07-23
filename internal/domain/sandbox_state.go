package domain

type SandboxState string

const (
	StatePending    SandboxState = "Pending"
	StateCreating   SandboxState = "Creating"
	StateRunning    SandboxState = "Running"
	StateStopping   SandboxState = "Stopping"
	StateTerminated SandboxState = "Terminated"
	StateFailed     SandboxState = "Failed"
)

type DesiredState string

const (
	DesiredRunning    DesiredState = "Running"
	DesiredTerminated DesiredState = "Terminated"
)

func (s SandboxState) Terminal() bool {
	return s == StateTerminated || s == StateFailed
}
