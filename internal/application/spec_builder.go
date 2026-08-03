package application

import "minisandbox/internal/domain"

// SandboxSpecBuilder 把最小创建命令与服务端默认值合并为 resolved spec。
//
// Builder 保存值副本且 Build 不修改内部状态，因此相同输入总是得到相同结果，
// 可安全地在并发创建请求之间复用。
type SandboxSpecBuilder struct {
	defaults domain.SandboxSpec
	bounds   domain.ResourceBounds
}

// NewSandboxSpecBuilder 使用完整默认规格和服务端资源上限创建 builder。
//
// defaults 通常来自 config.DefaultSandboxSpec；构造函数不提前校验，使启动配置
// 校验和每次 Build 的领域校验保持各自明确的错误边界。
func NewSandboxSpecBuilder(
	defaults domain.SandboxSpec,
	bounds domain.ResourceBounds,
) SandboxSpecBuilder {
	return SandboxSpecBuilder{
		defaults: defaults,
		bounds:   bounds,
	}
}

// Build 生成并校验完整 resolved spec。
//
// 请求 image 无条件覆盖配置默认 image，包括空字符串；这样缺失的必填字段
// 会被领域校验拒绝，而不是静默回退为另一个镜像。
func (b SandboxSpecBuilder) Build(command CreateSandbox) (domain.SandboxSpec, error) {
	spec := b.defaults
	spec.Image = command.Image
	spec.Network.Outbound = command.Outbound
	if err := spec.Validate(b.bounds); err != nil {
		return domain.SandboxSpec{}, err
	}
	return spec, nil
}
