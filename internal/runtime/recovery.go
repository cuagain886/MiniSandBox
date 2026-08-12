package runtime

import "context"

// RecoveryInventory 提供启动恢复所需的三类完整只读事实源。
type RecoveryInventory interface {
	// InventoryManagedContainers 全量枚举并安全投影受管 main/egress 容器。
	InventoryManagedContainers(context.Context) ([]ManagedContainerObservation, error)
	// InventoryManagedVolumes 全量枚举并安全投影受管 workspace 卷。
	InventoryManagedVolumes(context.Context) ([]ManagedVolumeObservation, error)
	// InventoryRuntimeDirectories 全量枚举受管目录及 lease.json。
	InventoryRuntimeDirectories(context.Context) ([]RuntimeDirectoryObservation, error)
}

// EgressRecoveryBootstrap 是启动 inventory 前的服务级 outbound 网络门禁。
type EgressRecoveryBootstrap interface {
	// EnsureRecoveryEgressNetwork 在 outbound 关闭时幂等 no-op；开启时只创建缺失网络，漂移则失败关闭。
	EnsureRecoveryEgressNetwork(context.Context) error
}

// RecoveryExpectation 是服务端配置和受管网络共同导出的恢复校验值。
type RecoveryExpectation struct {
	// EgressPolicyHash 是 outbound orphan 必须精确匹配的规范策略摘要。
	EgressPolicyHash string
}

// RecoveryExpectationProvider 提供不能由 sandbox 请求或实际 sidecar 自证的恢复期望。
type RecoveryExpectationProvider interface {
	// RecoveryExpectation 只读取已验证服务网络并从受信配置计算期望值。
	RecoveryExpectation(context.Context) (RecoveryExpectation, error)
}
