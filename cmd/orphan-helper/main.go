// Package main 提供真实容器 PID 1 验收使用的确定性 double-fork helper。
//
// 它只生成短生命周期后代并发布就绪文件，不属于生产二进制或 sandbox API。
package main

import (
	"os"
	"os/exec"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "middle" {
		command := exec.Command(os.Args[0], "leaf")
		if err := command.Start(); err != nil {
			os.Exit(21)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "leaf" {
		os.Exit(0)
	}
	command := exec.Command(os.Args[0], "middle")
	if err := command.Start(); err != nil {
		os.Exit(11)
	}
	// 给两级后代足够时间退出；父进程保持存活，便于从同一 PID namespace 检查 zombie。
	time.Sleep(500 * time.Millisecond)
	if err := os.WriteFile("/tmp/orphan-ready", []byte("ready\n"), 0o600); err != nil {
		os.Exit(12)
	}
	time.Sleep(30 * time.Second)
}
