package egressnft

import (
	"net/netip"
	"testing"
)

// TestReadbackPrefixMarker 验证 nft 文本回读对 host prefix 省略 /32 或 /128 时，
// 校验器使用同一规范表示，同时保留网络前缀的掩码。
func TestReadbackPrefixMarker(t *testing.T) {
	tests := []struct {
		prefix string
		want   string
	}{
		{prefix: "192.0.2.1/32", want: "192.0.2.1"},
		{prefix: "::/128", want: "::"},
		{prefix: "2001:db8::/64", want: "2001:db8::/64"},
	}
	for _, test := range tests {
		prefix := netip.MustParsePrefix(test.prefix)
		if got := readbackPrefixMarker(prefix); got != test.want {
			t.Fatalf("readback marker for %s: got %q, want %q", test.prefix, got, test.want)
		}
	}
}
