package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// canonicalSpec 是 spec hash 专用的规范化编码结构。
//
// 字段顺序与 JSON 字段名一经使用即被持久化的 SpecHash 和 Docker labels
// 依赖,不得调整;SandboxSpec 新增字段时必须同步扩展本结构,并接受由此
// 带来的 hash 变化。本结构只覆盖 resolved spec,不包含状态、时间、
// runtime ID 或 message。
type canonicalSpec struct {
	Image     string             `json:"image"`
	Resources canonicalResources `json:"resources"`
	Workspace canonicalWorkspace `json:"workspace"`
	Network   canonicalNetwork   `json:"network"`
	Platform  canonicalPlatform  `json:"platform"`
}

// canonicalResources 是资源上限的规范化编码。
type canonicalResources struct {
	CPUQuotaMillis int64 `json:"cpu_quota_millis"`
	MemoryMiB      int64 `json:"memory_mib"`
	PIDs           int64 `json:"pids"`
}

// canonicalWorkspace 是工作目录语义的规范化编码。
type canonicalWorkspace struct {
	MountPath  string `json:"mount_path"`
	Persistent bool   `json:"persistent"`
}

// canonicalNetwork 是网络能力的规范化编码。
type canonicalNetwork struct {
	Outbound bool `json:"outbound"`
}

// canonicalPlatform 是目标平台的规范化编码。
type canonicalPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Hash 返回 resolved spec 的稳定 SHA-256,输出为 64 位小写十六进制。
//
// 同一 spec 在任意进程、任意时间重复计算的结果必须一致:恢复流程依赖
// 该值与持久化记录及 Docker label 中的 spec-hash 比较来识别资源漂移。
// 规范化编码使用固定字段顺序的专用结构,当前不含 map 字段;encoding/json
// 对 map key 的排序保证也使未来引入 map 字段时结果与遍历顺序无关。
func (s SandboxSpec) Hash() string {
	encoded, err := json.Marshal(canonicalSpec{
		Image: s.Image,
		Resources: canonicalResources{
			CPUQuotaMillis: s.Resources.CPUQuotaMillis,
			MemoryMiB:      s.Resources.MemoryMiB,
			PIDs:           s.Resources.PIDs,
		},
		Workspace: canonicalWorkspace{
			MountPath:  s.Workspace.MountPath,
			Persistent: s.Workspace.Persistent,
		},
		Network: canonicalNetwork{
			Outbound: s.Network.Outbound,
		},
		Platform: canonicalPlatform{
			OS:   s.Platform.OS,
			Arch: s.Platform.Arch,
		},
	})
	if err != nil {
		// canonicalSpec 只含字符串、整数和布尔字段,编码失败意味着本文件
		// 被错误扩展;这是程序缺陷,立即暴露而不是返回可被忽略的错误。
		panic("domain: canonical spec encoding failed: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
