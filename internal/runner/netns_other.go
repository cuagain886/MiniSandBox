//go:build !linux

package runner

import "errors"

// currentNetNSIdentity 在非 Linux 开发机上 fail closed；生产 runner 只支持
// Linux，测试通过注入 reader 验证 HTTP 协议而不伪造真实 namespace 证据。
func currentNetNSIdentity() (string, error) {
	return "", errors.New("Linux network namespace identity is unavailable")
}
