package api

import "sync"

// Readiness 并发安全地记录 sandboxd 启动所需组件是否已经就绪。
//
// 零值可直接使用且默认全部未就绪。对象只保存布尔状态，不保存错误 cause、
// Docker socket、文件路径或凭据，避免健康接口意外泄露内部信息。
type Readiness struct {
	mu sync.RWMutex

	store    bool
	docker   bool
	artifact bool
	recovery bool
	worker   bool
}

// ReadinessSnapshot 是某一时刻五个必要组件状态的不可变值快照。
type ReadinessSnapshot struct {
	// Store 表示持久化存储已经打开并可供生命周期请求使用。
	Store bool
	// Docker 表示控制面已经确认 Docker daemon 可访问。
	Docker bool
	// Artifact 表示嵌入式 runner/init 产物已通过启动校验。
	Artifact bool
	// Recovery 表示启动恢复和首次资源对账已经完成。
	Recovery bool
	// Worker 表示 reconcile worker 已启动并可消费唤醒通知。
	Worker bool
}

// Ready 仅在全部必要组件均已就绪时返回 true。
func (s ReadinessSnapshot) Ready() bool {
	return s.Store &&
		s.Docker &&
		s.Artifact &&
		s.Recovery &&
		s.Worker
}

// SetStore 只更新 Store 就绪状态。
func (r *Readiness) SetStore(ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = ready
}

// SetDocker 只更新 Docker daemon 就绪状态。
func (r *Readiness) SetDocker(ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docker = ready
}

// SetArtifact 只更新嵌入式 artifact 校验状态。
func (r *Readiness) SetArtifact(ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.artifact = ready
}

// SetRecovery 只更新启动恢复完成状态。
func (r *Readiness) SetRecovery(ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recovery = ready
}

// SetWorker 只更新 reconcile worker 运行状态。
func (r *Readiness) SetWorker(ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.worker = ready
}

// Snapshot 返回不含内部 cause 和敏感配置的独立状态快照。
func (r *Readiness) Snapshot() ReadinessSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return ReadinessSnapshot{
		Store:    r.store,
		Docker:   r.docker,
		Artifact: r.artifact,
		Recovery: r.recovery,
		Worker:   r.worker,
	}
}
