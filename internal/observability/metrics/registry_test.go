package metrics

import (
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestRegistryRejectsDuplicateRegistration 验证显式注册成功且重复注册返回错误。
func TestRegistryRejectsDuplicateRegistration(t *testing.T) {
	registry := NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "minisandbox_registry_test_total", Help: "Registry test counter."})
	if err := registry.Register(counter); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(counter); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

// TestRegistriesAreIndependentAndHaveNoDefaultCollectors 验证多实例不共享样本且没有 Go/process 指标。
func TestRegistriesAreIndependentAndHaveNoDefaultCollectors(t *testing.T) {
	first, second := NewRegistry(), NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "minisandbox_registry_independent_total", Help: "Independent registry test."})
	if err := first.Register(counter); err != nil {
		t.Fatal(err)
	}
	counter.Inc()
	firstFamilies, err := first.Gatherer().Gather()
	if err != nil || len(firstFamilies) != 1 {
		t.Fatalf("first registry: families=%d err=%v", len(firstFamilies), err)
	}
	secondFamilies, err := second.Gatherer().Gather()
	if err != nil || len(secondFamilies) != 0 {
		t.Fatalf("second registry: families=%d err=%v", len(secondFamilies), err)
	}
	for _, family := range firstFamilies {
		if family.GetName() == "go_goroutines" || family.GetName() == "process_cpu_seconds_total" {
			t.Fatalf("unexpected default collector: %s", family.GetName())
		}
	}
}

// TestRegistrySupportsConcurrentUpdateAndGather 验证官方 collector 的并发保证适用于独立 registry。
func TestRegistrySupportsConcurrentUpdateAndGather(t *testing.T) {
	registry := NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "minisandbox_registry_concurrent_total", Help: "Concurrent registry test."})
	if err := registry.Register(counter); err != nil {
		t.Fatal(err)
	}
	const workers = 32
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				counter.Inc()
				if _, err := registry.Gatherer().Gather(); err != nil {
					t.Errorf("gather: %v", err)
				}
			}
		}()
	}
	wait.Wait()
}
