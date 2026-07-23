package main

import (
	"log/slog"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		slog.Error("usage: sandbox-init -- command [args...]")
		os.Exit(2)
	}
	args := os.Args[1:]
	if args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		slog.Error("sandbox-init requires a child command")
		os.Exit(2)
	}
	os.Exit(run(args))
}
