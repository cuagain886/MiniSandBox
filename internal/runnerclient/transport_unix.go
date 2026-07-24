//go:build unix

package runnerclient

import (
	"context"
	"net"
	"net/http"
)

// unixTransport 创建只允许连接指定 Unix Socket 的 HTTP transport。
func unixTransport(socketPath string) http.RoundTripper {
	return &http.Transport{
		DialContext: func(
			ctx context.Context,
			_, _ string,
		) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
}
