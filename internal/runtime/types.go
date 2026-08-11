package runtime

// ActualState 表示容器运行时中资源的粗粒度实际状态。
type ActualState string

const (
	// ActualMissing 表示找不到受管容器。
	ActualMissing ActualState = "Missing"
	// ActualCreated 表示容器已创建但尚未运行。
	ActualCreated ActualState = "Created"
	// ActualRunning 表示容器进程正在运行。
	ActualRunning ActualState = "Running"
	// ActualStopped 表示容器存在但主进程已经退出。
	ActualStopped ActualState = "Stopped"
)

const (
	// DiscoveryLabelsInvalid 表示受管容器 labels 缺失、格式错误或身份不一致。
	DiscoveryLabelsInvalid = "LABELS_INVALID"
	// DiscoverySchemaUnsupported 表示受管容器使用当前控制面不认识的 label schema。
	DiscoverySchemaUnsupported = "LABEL_SCHEMA_UNSUPPORTED"
	// DiscoveryStateUnsupported 表示 Docker 状态无法安全映射为稳定 ActualState。
	DiscoveryStateUnsupported = "STATE_UNSUPPORTED"
	// DiscoveryRoleUnsupported 表示容器 resource role 未知或与 labels 形态冲突。
	DiscoveryRoleUnsupported = "RESOURCE_ROLE_UNSUPPORTED"
	// DiscoveryInspectUnavailable 表示 list 后 inspect 失败但并非资源已消失。
	DiscoveryInspectUnavailable = "INSPECT_UNAVAILABLE"
	// DiscoveryProfileInvalid 表示容器名称、网络、mount 或安全 profile 无法安全解析。
	DiscoveryProfileInvalid = "PROFILE_INVALID"
)

// ContainerResourceRole 是受管容器在 sandbox bundle 中的唯一职责。
type ContainerResourceRole string

const (
	// ContainerRoleMain 表示运行 sandbox-init/runnerd 的主容器。
	ContainerRoleMain ContainerResourceRole = "main"
	// ContainerRoleEgress 表示仅持有 outbound 网络命名空间的 sidecar。
	ContainerRoleEgress ContainerResourceRole = "egress-sidecar"
)

// ManagedContainerObservation 是启动恢复可使用的安全容器事实，不含 raw inspect。
type ManagedContainerObservation struct {
	// ContainerID 是 Docker 分配的内部 ID，只用于受管资源关联。
	ContainerID string
	// SandboxID 是经过 labels 校验的规范 sandbox ID；损坏项可为空。
	SandboxID string
	// Role 区分主容器与 egress sidecar。
	Role ContainerResourceRole
	// Name 是经过确定性命名规则校验的容器名。
	Name string
	// State 是与 Docker 细节解耦的粗粒度状态。
	State ActualState
	// SchemaVersion 是已接受的 v1 或 v2 label schema。
	SchemaVersion int
	// SpecHash 是主容器的安全规格摘要；sidecar 为空。
	SpecHash string
	// RunnerProtocolVersion 是主容器声明的 runner 协议版本。
	RunnerProtocolVersion int
	// EgressProtocolVersion 是 sidecar 声明的 egress 协议版本。
	EgressProtocolVersion int
	// EgressPolicyHash 是 sidecar 规范化 nft policy 的安全摘要。
	EgressPolicyHash string
	// NetworkMode 是 none、container、managed-egress 或 other 的安全分类。
	NetworkMode string
	// NetworkPeerContainerID 是 container network mode 引用的内部 peer ID。
	NetworkPeerContainerID string
	// Workspace 是主容器挂载的受管 named volume 名；不包含宿主机 source。
	Workspace string
	// WorkspaceDestination 是容器内固定 workspace 目标路径。
	WorkspaceDestination string
	// RestartPolicy 是 Docker restart policy 的稳定名称。
	RestartPolicy string
	// Privileged 表示容器是否扩大为 privileged。
	Privileged bool
	// ReadonlyRootfs 表示根文件系统是否只读。
	ReadonlyRootfs bool
	// NoNewPrivileges 表示安全选项是否禁止获得新权限。
	NoNewPrivileges bool
	// CapAdd 是容器显式增加的 capability 名称副本。
	CapAdd []string
	// CapDrop 是容器显式移除的 capability 名称副本。
	CapDrop []string
	// DiscoveryIssue 是单项损坏时的稳定安全诊断码。
	DiscoveryIssue string
}

// ActualSandbox 汇总 reconciler 决策所需的 runtime 观测结果。
type ActualSandbox struct {
	// ID 是从受管 labels 验证得到的 sandbox ID；Missing 时为请求 ID。
	ID string
	// RuntimeID 是容器运行时分配的内部资源 ID，不直接暴露给公共 API。
	RuntimeID string
	// State 是与具体 Docker 状态解耦的粗粒度实际状态。
	State ActualState
	// SpecHash 是安全 resolved spec 摘要，用于识别资源漂移。
	SpecHash string
	// Workspace 是受管 named volume 的确定性名称，不包含宿主机路径。
	Workspace string
	// RunnerProtocolVersion 是受管容器 label 声明并经 adapter 验证的整数版本。
	RunnerProtocolVersion int
	// DiscoveryIssue 是启动扫描发现损坏资源时使用的安全诊断代码。
	//
	// 普通 Inspect 成功或 Missing 时为空；内容不得包含原始 labels 或路径。
	DiscoveryIssue string
}
