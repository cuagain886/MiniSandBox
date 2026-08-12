package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	reconcileOperations = enumSet("create", "delete", "expire", "recover", "health", "cleanup")
	reconcileResults    = enumSet("converged", "retry_scheduled", "blocked", "error")
	retryErrorCodes     = enumSet("runtime_unavailable", "runner_unhealthy", "cleanup_pending", "internal_error")
	orphanClasses       = enumSet("incomplete_bundle", "unknown_schema", "identity_mismatch", "spec_hash_mismatch", "security_profile_mismatch", "network_namespace_mismatch", "lease_untrusted", "duplicate_resource")
	dockerOperations    = enumSet("ping", "inventory", "ensure_network", "pull_image", "ensure_sandbox", "replace_compute", "delete_sandbox")
	dockerResults       = enumSet("success", "retryable_error", "terminal_error")
	createResults       = enumSet("accepted", "rejected", "error")
)

// OperationCounters 聚合 Phase 3 生命周期、收敛、租约、异常与 Docker 操作 counter。
type OperationCounters struct {
	create    *prometheus.CounterVec
	reconcile *prometheus.CounterVec
	retry     *prometheus.CounterVec
	expired   prometheus.Counter
	orphan    *prometheus.CounterVec
	docker    *prometheus.CounterVec
}

// ReliabilityMetrics 组合 P3-083 counters 与 P3-085 timing，使生产装配只注入一个窄对象。
type ReliabilityMetrics struct {
	*OperationCounters
	*TimingMetrics
}

// NewReliabilityMetrics 在同一独立 registry 注册 reliability counter 与 timing collectors。
func NewReliabilityMetrics(registry *Registry) (*ReliabilityMetrics, error) {
	counters, err := NewOperationCounters(registry)
	if err != nil {
		return nil, err
	}
	timing, err := NewTimingMetrics(registry)
	if err != nil {
		return nil, err
	}
	return &ReliabilityMetrics{OperationCounters: counters, TimingMetrics: timing}, nil
}

// NewOperationCounters 构造并一次性注册全部 P3-083 collector；任一冲突都会整体返回错误。
func NewOperationCounters(registry *Registry) (*OperationCounters, error) {
	counters := &OperationCounters{
		create:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "minisandbox_sandbox_create_requests_total", Help: "Sandbox create requests observed by the control plane."}, []string{"result"}),
		reconcile: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "minisandbox_reconcile_total", Help: "Reconcile attempts completed inside the keyed lock."}, []string{"operation", "result"}),
		retry:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "minisandbox_retry_scheduled_total", Help: "Retries successfully persisted by operation and stable error code."}, []string{"operation", "error_code"}),
		expired:   prometheus.NewCounter(prometheus.CounterOpts{Name: "minisandbox_lease_expired_total", Help: "Lease expiry intents successfully committed."}),
		orphan:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "minisandbox_orphan_observations_total", Help: "Runtime anomaly observations successfully persisted."}, []string{"classification"}),
		docker:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "minisandbox_runtime_docker_operations_total", Help: "Docker Engine operations by stable operation and result."}, []string{"operation", "result"}),
	}
	collectors := []prometheus.Collector{counters.create, counters.reconcile, counters.retry, counters.expired, counters.orphan, counters.docker}
	for _, collector := range collectors {
		if err := registry.Register(collector); err != nil {
			return nil, err
		}
	}
	return counters, nil
}

// ObserveCreate 记录一次控制面 create 请求结果。
func (c *OperationCounters) ObserveCreate(result string) {
	c.create.WithLabelValues(normalize(result, createResults)).Inc()
}

// ObserveReconcile 记录一次实际 reconcile attempt；调用方负责排除未获得锁、shutdown 与 CAS 冲突。
func (c *OperationCounters) ObserveReconcile(operation, result string) {
	c.reconcile.WithLabelValues(normalize(operation, reconcileOperations), normalize(result, reconcileResults)).Inc()
}

// ObserveRetryScheduled 仅在 retry CAS 成功持久化后记录一次。
func (c *OperationCounters) ObserveRetryScheduled(operation, errorCode string) {
	c.retry.WithLabelValues(normalize(operation, reconcileOperations), normalize(errorCode, retryErrorCodes)).Inc()
}

// ObserveLeaseExpired 仅在 expiry intent 首次成功提交后记录一次。
func (c *OperationCounters) ObserveLeaseExpired() { c.expired.Inc() }

// ObserveOrphan 记录一次已成功持久化的 anomaly observation。
func (c *OperationCounters) ObserveOrphan(classification string) {
	c.orphan.WithLabelValues(normalize(classification, orphanClasses)).Inc()
}

// ObserveDocker 记录一次真实 Docker Engine 操作返回结果。
func (c *OperationCounters) ObserveDocker(operation, result string) {
	c.docker.WithLabelValues(normalize(operation, dockerOperations), normalize(result, dockerResults)).Inc()
}

func enumSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func normalize(value string, allowed map[string]struct{}) string {
	if _, ok := allowed[value]; ok {
		return value
	}
	return "unknown"
}
