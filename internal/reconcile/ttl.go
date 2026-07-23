package reconcile

import (
	"time"

	"minisandbox/internal/domain"
)

func Expired(sandbox domain.Sandbox, now time.Time) bool {
	return sandbox.Expired(now)
}
