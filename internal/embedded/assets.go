package embedded

import "embed"

// Artifacts are replaced with static Linux binaries before sandboxd is built.
//
//go:embed artifacts/linux_amd64/*
var artifacts embed.FS

func ReadLinuxAMD64(name string) ([]byte, error) {
	return artifacts.ReadFile("artifacts/linux_amd64/" + name)
}
