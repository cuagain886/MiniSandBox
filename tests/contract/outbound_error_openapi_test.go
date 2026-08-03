package contract_test

import (
	"strings"
	"testing"
)

// TestOutboundErrorOpenAPIEnums 固定 lifecycle API 的五个 outbound 错误码。
func TestOutboundErrorOpenAPIEnums(t *testing.T) {
	document := readLifecycleOpenAPI(t)
	for _, code := range []string{
		"OUTBOUND_NOT_ALLOWED",
		"EGRESS_IMAGE_UNAVAILABLE",
		"EGRESS_POLICY_INVALID",
		"EGRESS_NOT_READY",
		"EGRESS_UNHEALTHY",
	} {
		if !strings.Contains(document, "        - "+code) {
			t.Errorf("outbound error code is missing %s", code)
		}
	}
}
