// Package egressanchor 实现 egress sidecar 在 nft 回验后的 attestation、永久降权
// 和无监听 namespace anchor 生命周期。
//
// 本包不安装或更新网络规则、不创建 socket、不启动健康检查子进程，也不提供任何管理端点。
package egressanchor

import (
	"context"
	"errors"
	"io"
	"time"

	"minisandbox/internal/egressnft"
)

// DefaultAttestationPath 是 sidecar 受限 tmpfs 内的固定 readiness 文件路径。
const DefaultAttestationPath = "/run/minisandbox-egress/attestation.json"

// Snapshot 是降权回验所需的进程身份与 capability 视图。
type Snapshot struct {
	// UID 是当前进程的有效 UID。
	UID uint32
	// GID 是当前进程的有效 GID。
	GID uint32
	// SupplementaryGroups 是当前进程仍持有的附加组。
	SupplementaryGroups []uint32
	// CapEffective 是 Linux CapEff 位图。
	CapEffective uint64
	// CapPermitted 是 Linux CapPrm 位图。
	CapPermitted uint64
	// CapAmbient 是 Linux CapAmb 位图。
	CapAmbient uint64
}

// Platform 隔离 Linux netns、credential 与 capability 系统调用，便于 fail-closed 测试。
type Platform interface {
	// NetworkNamespace 返回当前进程 netns 的稳定 linux-netns:<dev>:<inode> 身份。
	NetworkNamespace() (string, error)
	// DropPrivileges 清空附加组、切换固定身份、清除 capability 并设置 no_new_privs。
	DropPrivileges(uint32, uint32) error
	// Snapshot 回读当前身份与 capability 状态。
	Snapshot() (Snapshot, error)
}

// Options 提供 Activate 的可信进程资源和可测试依赖。
type Options struct {
	// Platform 是 Linux 安全系统调用实现。
	Platform Platform
	// BootstrapInput 是必须在降权前关闭的一次性 bootstrap 输入。
	BootstrapInput io.Closer
	// AttestationPath 是受限 tmpfs 内的固定输出路径。
	AttestationPath string
	// Now 返回 attestation 使用的 UTC 时间。
	Now func() time.Time
}

// Activate 校验 netns、永久降权、回验并原子发布 attestation，随后只等待 ctx
// 结束以维持 namespace；函数本身不会创建任何监听 socket 或子进程。
func Activate(ctx context.Context, bootstrap egressnft.Bootstrap, options Options) error {
	if options.Platform == nil || options.BootstrapInput == nil {
		return errors.New("egress anchor dependencies are incomplete")
	}
	path := options.AttestationPath
	if path == "" {
		path = DefaultAttestationPath
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	if err := options.BootstrapInput.Close(); err != nil {
		return errors.New("close egress bootstrap input")
	}
	networkNamespace, err := options.Platform.NetworkNamespace()
	if err != nil || networkNamespace != bootstrap.NetworkNamespace {
		return errors.New("egress network namespace identity mismatch")
	}
	if err := options.Platform.DropPrivileges(bootstrap.AnchorUID, bootstrap.AnchorGID); err != nil {
		return errors.New("drop egress bootstrap privileges")
	}
	snapshot, err := options.Platform.Snapshot()
	if err != nil {
		return errors.New("read back egress anchor privileges")
	}
	if err := verifySnapshot(snapshot, bootstrap.AnchorUID, bootstrap.AnchorGID); err != nil {
		return err
	}
	attestation := Attestation{
		ProtocolVersion: bootstrap.Policy.ProtocolVersion, RuleSchemaVersion: bootstrap.Policy.RuleSchemaVersion,
		PolicyHash: bootstrap.Policy.Hash, NetworkNamespace: networkNamespace,
		ImageDigest: bootstrap.ImageDigest, CreatedAt: now().UTC(),
	}
	if err := WriteAttestation(path, attestation); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func verifySnapshot(snapshot Snapshot, uid, gid uint32) error {
	if snapshot.UID != uid || snapshot.GID != gid || uid == 0 || gid == 0 {
		return errors.New("egress anchor identity mismatch")
	}
	if len(snapshot.SupplementaryGroups) != 0 {
		return errors.New("egress anchor supplementary groups remain")
	}
	const netAdminMask = uint64(1) << 12
	if snapshot.CapEffective&netAdminMask != 0 || snapshot.CapPermitted&netAdminMask != 0 || snapshot.CapAmbient&netAdminMask != 0 {
		return errors.New("egress anchor retains NET_ADMIN")
	}
	return nil
}
