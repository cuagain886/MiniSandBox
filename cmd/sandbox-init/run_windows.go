//go:build windows

package main

import "log/slog"

func run([]string) int {
	slog.Error("sandbox-init is supported only in Linux sandbox containers")
	return 2
}
