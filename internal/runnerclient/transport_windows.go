//go:build windows

package runnerclient

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// unixTransport 在 Windows 开发机上返回明确失败的占位 transport。
//
// runner 通信仅支持 Linux/Unix Socket，此实现只保证控制面包可以在 Windows
// 完成编译和单元测试。
func unixTransport(string) http.RoundTripper {
	return &http.Transport{
		DialContext: func(
			context.Context,
			string,
			string,
		) (net.Conn, error) {
			return nil, errors.New("Unix socket transport is unavailable on Windows")
		},
	}
}
