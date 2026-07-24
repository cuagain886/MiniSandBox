package reconcile

import (
	"time"

	"minisandbox/internal/domain"
)

// Expired 判断 sandbox 是否应因 TTL 到期而转入清理流程。
func Expired(sandbox domain.Sandbox, now time.Time) bool {
	return sandbox.Expired(now)
}
