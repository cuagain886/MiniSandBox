package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	executionModes   = enumSet("foreground", "background")
	executionResults = enumSet("accepted", "rejected", "error")
	terminalResults  = enumSet("exited", "failed", "cancelled", "timed_out")
)

// ExecutionCounters 只描述当前控制面收到的 execution 请求与实际观察到的前台终态。
type ExecutionCounters struct {
	requests *prometheus.CounterVec
	terminal *prometheus.CounterVec
}

// NewExecutionCounters 构造并注册控制面 execution counters。
func NewExecutionCounters(registry *Registry) (*ExecutionCounters, error) {
	counters := &ExecutionCounters{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "minisandbox_execution_requests_total", Help: "Execution requests received by this control-plane process."}, []string{"mode", "result"}),
		terminal: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "minisandbox_execution_foreground_terminal_observed_total", Help: "Unique valid foreground terminal events observed by this control-plane process."}, []string{"result"}),
	}
	if err := registry.Register(counters.requests); err != nil {
		return nil, err
	}
	if err := registry.Register(counters.terminal); err != nil {
		return nil, err
	}
	return counters, nil
}

// ObserveExecutionRequest 记录一次控制面 execution 请求完成结果。
func (c *ExecutionCounters) ObserveExecutionRequest(mode, result string) {
	c.requests.WithLabelValues(normalize(mode, executionModes), normalize(result, executionResults)).Inc()
}

// ObserveForegroundTerminal 记录一次前台代理实际观察到的合法终态。
func (c *ExecutionCounters) ObserveForegroundTerminal(result string) {
	c.terminal.WithLabelValues(normalize(result, terminalResults)).Inc()
}
