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
)

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
