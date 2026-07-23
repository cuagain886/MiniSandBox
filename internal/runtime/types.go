package runtime

import "time"

type ActualState string

const (
	ActualMissing ActualState = "Missing"
	ActualCreated ActualState = "Created"
	ActualRunning ActualState = "Running"
	ActualStopped ActualState = "Stopped"
)

type ActualSandbox struct {
	ID           string
	ContainerID  string
	State        ActualState
	RunnerReady  bool
	DiscoveredAt time.Time
}
