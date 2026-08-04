//go:build !linux

package runnerauth

import "errors"

// LoadMasterKey 在非 Linux 平台明确失败；no-follow 与 Unix mode 证据不能由
// 其他平台文件语义替代。
func LoadMasterKey(string) (MasterKey, error) {
	return MasterKey{}, errors.New("runner master key loading requires Linux")
}
