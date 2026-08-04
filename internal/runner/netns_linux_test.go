//go:build linux

package runner

import (
	"testing"

	"minisandbox/pkg/protocol"
)

// TestCurrentNetNSIdentityReadsProc 在真实 Linux 上验证 `/proc/self/ns/net`
// stat 可读取并生成协议认可的 device/inode 身份。
func TestCurrentNetNSIdentityReadsProc(t *testing.T) {
	identity, err := currentNetNSIdentity()
	if err != nil {
		t.Fatalf("read current netns identity: %v", err)
	}
	if err := protocol.ValidateRunnerNetNSIdentity(identity); err != nil {
		t.Fatalf("invalid current netns identity %q: %v", identity, err)
	}
}
