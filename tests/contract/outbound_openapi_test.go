package contract_test

import (
	"strings"
	"testing"
)

// TestOutboundLifecycleOpenAPISchema 固定 outbound 的唯一公共请求字段。
func TestOutboundLifecycleOpenAPISchema(t *testing.T) {
	document := readLifecycleOpenAPI(t)
	for _, fragment := range []string{
		"        network:",
		"    SandboxNetworkRequest:",
		"        outbound:",
		"          default: false",
	} {
		if !strings.Contains(document, fragment) {
			t.Errorf("outbound lifecycle schema is missing %q", fragment)
		}
	}
}
