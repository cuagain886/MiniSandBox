//go:build linux

// Package runnerstage 从受信控制面配置生成单 sandbox runner 的一次性启动材料。
//
// 本包只写入 runtime 管理目录，不通过环境变量、命令行或 Docker labels 传递秘密。
package runnerstage

import (
	"errors"
	"os"
	"path/filepath"

	"minisandbox/internal/config"
	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
)

// Stager 持有仅用于派生单 sandbox token 的主密钥副本。
type Stager struct {
	config config.Config
	key    runnerauth.MasterKey
	uid    uint32
	gid    uint32
}

// New 从安全文件加载主密钥并绑定 sandboxd 当前有效数字身份。
func New(control config.Config) (*Stager, error) {
	key, err := runnerauth.LoadMasterKey(control.Security.RunnerMasterKeyFile)
	if err != nil {
		return nil, err
	}
	return &Stager{config: control, key: key, uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())}, nil
}

// Stage 原子发布非秘密 bootstrap JSON，再发布会被 runnerd 读取即删除的派生 token。
func (s *Stager) Stage(runtimeDirectory, sandboxID string) error {
	if s == nil {
		return errors.New("runner bootstrap stager is unavailable")
	}
	value, err := runnerbootstrap.FromConfig(s.config, sandboxID, s.uid, s.gid)
	if err != nil {
		return err
	}
	encoded, err := runnerbootstrap.Marshal(value)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(runtimeDirectory, ".bootstrap.tmp-")
	if err != nil {
		return errors.New("create runner bootstrap file failed")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return errors.New("secure runner bootstrap file failed")
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return errors.New("write runner bootstrap file failed")
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return errors.New("sync runner bootstrap file failed")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close runner bootstrap file failed")
	}
	if err := os.Rename(temporaryPath, filepath.Join(runtimeDirectory, runnerbootstrap.ConfigFileName)); err != nil {
		return errors.New("publish runner bootstrap file failed")
	}
	token, err := runnerauth.DeriveToken(&s.key, sandboxID)
	if err != nil {
		return err
	}
	return runnerauth.StageTokenFile(runtimeDirectory, s.uid, s.gid, &token)
}

// Close 清零 stager 持有的主密钥副本。
func (s *Stager) Close() error {
	if s != nil {
		s.key.Clear()
	}
	return nil
}
