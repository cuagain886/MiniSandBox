package domain

import "time"

type Sandbox struct {
	ID            string
	Image         string
	DesiredState  DesiredState
	ObservedState SandboxState
	Workspace     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ExpiresAt     *time.Time
	Revision      uint64
	FailureReason string
}

func (s Sandbox) Expired(now time.Time) bool {
	return s.ExpiresAt != nil && !now.Before(*s.ExpiresAt)
}
