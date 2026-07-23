//go:build windows

package runnerclient

import (
	"context"
	"errors"
	"net"
	"net/http"
)

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
