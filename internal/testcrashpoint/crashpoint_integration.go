//go:build integration

// Package testcrashpoint 为专用可靠性构建提供受控 Unix Socket 崩溃边界。
package testcrashpoint

import (
	"bufio"
	"net"
	"os"
	"sync"
)

const (
	crashpointSocketEnv = "MINISANDBOX_TEST_CRASHPOINT_SOCKET"
	crashpointNameEnv   = "MINISANDBOX_TEST_CRASHPOINT"
	dropPointNameEnv    = "MINISANDBOX_TEST_DROP_POINT"
)

var hitOnce sync.Once

// Hit 仅在 integration 构建且名称精确匹配时通知受控 Unix Socket，然后阻塞等待测试进程强杀。
func Hit(name string) {
	if name == "" || os.Getenv(crashpointNameEnv) != name {
		return
	}
	hitOnce.Do(func() {
		connection, err := net.Dial("unix", os.Getenv(crashpointSocketEnv))
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = connection.Write([]byte(name + "\n"))
		_, _ = bufio.NewReader(connection).ReadByte()
	})
}

// Drop 仅在 integration 构建中丢弃名称精确匹配的一次测试事件。
func Drop(name string) bool {
	if name == "" || os.Getenv(dropPointNameEnv) != name {
		return false
	}
	os.Unsetenv(dropPointNameEnv)
	return true
}
