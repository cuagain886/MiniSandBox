//go:build unix

package runner

// 孤儿进程由 sandbox-init 统一回收。runnerd 只等待自己直接启动的命令，
// 避免通用 wait4(-1) 与 exec.Cmd.Wait 竞争同一个子进程。
