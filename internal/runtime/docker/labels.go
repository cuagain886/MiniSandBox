package docker

const (
	// LabelManaged 标识容器由 MiniSandbox 控制面管理。
	LabelManaged = "minisandbox.io/managed"
	// LabelSandboxID 保存可用于重启恢复的 sandbox ID。
	LabelSandboxID = "minisandbox.io/sandbox-id"
	// LabelSchema 标识 labels 恢复协议版本。
	LabelSchema = "minisandbox.io/schema"
	// LabelSpecHash 保存安全规格的摘要，不保存规格明文或秘密。
	LabelSpecHash = "minisandbox.io/spec-hash"
	// LabelExpiresAt 保存用于恢复 TTL 的到期时间。
	LabelExpiresAt = "minisandbox.io/expires-at"
	// LabelWorkspace 保存受管 workspace 的非秘密标识。
	LabelWorkspace = "minisandbox.io/workspace"
)
