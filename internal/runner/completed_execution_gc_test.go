package runner

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCompletedExecutionGCEvictsExpiredAndExcessInStableOrder 验证时间到期、数量驱逐和完成时间/ID 稳定排序。
func TestCompletedExecutionGCEvictsExpiredAndExcessInStableOrder(t *testing.T) {
	directory := t.TempDir()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	manager := gcManager(t, 8)
	ids := []ExecutionID{"exec_gc_c", "exec_gc_a", "exec_gc_b", "exec_gc_new"}
	completed := []time.Time{now.Add(-2 * time.Hour), now.Add(-30 * time.Minute), now.Add(-30 * time.Minute), now.Add(-time.Minute)}
	for index, id := range ids {
		gcAddCompleted(t, manager, directory, id, completed[index])
	}
	var removed []ExecutionID
	gc, err := newCompletedExecutionGC(manager, directory, time.Hour, 2, func(directory string, id ExecutionID) error {
		removed = append(removed, id)
		return removeCompletedExecutionLog(directory, id)
	})
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	if err := gc.Run(now); err != nil {
		t.Fatalf("run GC: %v", err)
	}
	if len(removed) != 2 || removed[0] != "exec_gc_c" || removed[1] != "exec_gc_a" {
		t.Fatalf("removed order: %v", removed)
	}
	for _, id := range removed {
		if _, err := manager.Descriptor(id); !errors.Is(err, ErrExecutionNotFound) {
			t.Fatalf("evicted ID still queryable %s: %v", id, err)
		}
		path, _ := BackgroundLogPath(directory, id)
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("log still exists %s: %v", id, err)
		}
	}
	for _, id := range []ExecutionID{"exec_gc_b", "exec_gc_new"} {
		if _, err := manager.Descriptor(id); err != nil {
			t.Fatalf("retained ID missing %s: %v", id, err)
		}
	}
}

// TestCompletedExecutionGCNeverEvictsRunning 验证运行中 execution 不受时间和数量策略影响。
func TestCompletedExecutionGCNeverEvictsRunning(t *testing.T) {
	directory := t.TempDir()
	manager := gcManager(t, 2)
	running := gcAddRunning(t, manager, "exec_gc_running")
	gcAddCompleted(t, manager, directory, "exec_gc_terminal", time.Now().Add(-time.Hour))
	gc, _ := NewCompletedExecutionGC(manager, directory, time.Millisecond, 1)
	if err := gc.Run(time.Now()); err != nil {
		t.Fatalf("run GC: %v", err)
	}
	if descriptor, err := manager.Descriptor(running.Descriptor().ID); err != nil || descriptor.State != ExecutionRunning {
		t.Fatalf("running execution evicted: descriptor=%+v err=%v", descriptor, err)
	}
}

// TestCompletedExecutionGCRetriesDeletionAfterManagerEviction 验证删除失败时对象已不可查询，下一轮仍会重试日志。
func TestCompletedExecutionGCRetriesDeletionAfterManagerEviction(t *testing.T) {
	directory := t.TempDir()
	manager := gcManager(t, 1)
	id := ExecutionID("exec_gc_retry")
	gcAddCompleted(t, manager, directory, id, time.Now().Add(-time.Hour))
	var calls atomic.Int64
	gc, _ := newCompletedExecutionGC(manager, directory, time.Millisecond, 1, func(directory string, id ExecutionID) error {
		if calls.Add(1) == 1 {
			return errors.New("temporary delete failure")
		}
		return removeCompletedExecutionLog(directory, id)
	})
	if err := gc.Run(time.Now()); !errors.Is(err, ErrCompletedExecutionCleanup) {
		t.Fatalf("first run: %v", err)
	}
	if _, err := manager.Descriptor(id); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("manager entry retained after delete failure: %v", err)
	}
	if err := gc.Run(time.Now()); err != nil || calls.Load() != 2 {
		t.Fatalf("retry: err=%v calls=%d", err, calls.Load())
	}
}

// TestCompletedExecutionGCUsesTrustedDirectoryHandle 验证降权组合根可经受信 FD 删除日志而不重新穿越父目录。
func TestCompletedExecutionGCUsesTrustedDirectoryHandle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("trusted /proc/self/fd directory requires Linux")
	}
	directory := t.TempDir()
	manager := gcManager(t, 1)
	id := ExecutionID("exec_gc_trusted_fd")
	gcAddCompleted(t, manager, directory, id, time.Now().Add(-time.Hour))
	handle, err := os.Open(directory)
	if err != nil {
		t.Fatalf("open execution directory: %v", err)
	}
	defer handle.Close()
	gc, err := NewCompletedExecutionGCFromDirectory(manager, handle, time.Millisecond, 1)
	if err != nil {
		t.Fatalf("new trusted FD GC: %v", err)
	}
	if err := gc.Run(time.Now()); err != nil {
		t.Fatalf("run trusted FD GC: %v", err)
	}
	path, _ := BackgroundLogPath(directory, id)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("trusted FD log remains: %v", err)
	}
}

// TestCompletedExecutionGCRefusesSymlinkLog 验证 GC 不跟随或删除预置 symlink，目标文件保持不变。
func TestCompletedExecutionGCRefusesSymlinkLog(t *testing.T) {
	directory := t.TempDir()
	manager := gcManager(t, 1)
	id := ExecutionID("exec_gc_symlink")
	execution := gcAddRunning(t, manager, id)
	gcFinishExecution(t, manager, execution, time.Now().Add(-time.Hour))
	path, _ := BackgroundLogPath(directory, id)
	target := filepath.Join(t.TempDir(), "target")
	_ = os.WriteFile(target, []byte("sentinel"), 0o600)
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	gc, _ := NewCompletedExecutionGC(manager, directory, time.Millisecond, 1)
	if err := gc.Run(time.Now()); !errors.Is(err, ErrCompletedExecutionCleanup) {
		t.Fatalf("symlink cleanup: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "sentinel" {
		t.Fatalf("symlink target changed: %q", data)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("symlink unexpectedly removed: %v", err)
	}
}

// TestCompletedExecutionGCConcurrentQueriesRemainSafe 验证并发查询只观察完整 descriptor 或 not found。
func TestCompletedExecutionGCConcurrentQueriesRemainSafe(t *testing.T) {
	directory := t.TempDir()
	manager := gcManager(t, 64)
	ids := make([]ExecutionID, 32)
	for index := range ids {
		ids[index] = ExecutionID("exec_gc_race_" + strconv.Itoa(index))
		gcAddCompleted(t, manager, directory, ids[index], time.Now().Add(-time.Hour))
	}
	gc, _ := NewCompletedExecutionGC(manager, directory, time.Millisecond, 1)
	stop := make(chan struct{})
	var bad atomic.Int64
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, id := range ids {
					descriptor, err := manager.Descriptor(id)
					if err == nil && !terminalExecutionState(descriptor.State) || err != nil && !errors.Is(err, ErrExecutionNotFound) {
						bad.Add(1)
					}
				}
			}
		}()
	}
	if err := gc.Run(time.Now()); err != nil {
		t.Fatalf("run GC: %v", err)
	}
	close(stop)
	wait.Wait()
	if bad.Load() != 0 {
		t.Fatalf("inconsistent query results: %d", bad.Load())
	}
}

func gcManager(t *testing.T, limit int) *Manager {
	t.Helper()
	var sequence atomic.Int64
	manager, err := newManager(limit, creatorFunc(func() (*Execution, error) {
		return newPendingExecution(ExecutionID("exec_gc_generated_"+strconv.FormatInt(sequence.Add(1), 10)), time.Now()), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func gcAddRunning(t *testing.T, manager *Manager, id ExecutionID) *Execution {
	t.Helper()
	manager.mu.Lock()
	original := manager.factory
	execution := newPendingExecution(id, time.Now())
	manager.factory = creatorFunc(func() (*Execution, error) { return execution, nil })
	manager.mu.Unlock()
	created, err := manager.CreateExecution()
	manager.mu.Lock()
	manager.factory = original
	manager.mu.Unlock()
	if err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	if err := created.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
	return created
}

func gcAddCompleted(t *testing.T, manager *Manager, directory string, id ExecutionID, completedAt time.Time) {
	t.Helper()
	execution := gcAddRunning(t, manager, id)
	gcFinishExecution(t, manager, execution, completedAt)
	path, err := BackgroundLogPath(directory, id)
	if err != nil {
		t.Fatalf("log path: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

func gcFinishExecution(t *testing.T, manager *Manager, execution *Execution, completedAt time.Time) {
	t.Helper()
	exitCode := 0
	if err := execution.Transition(ExecutionExited, TerminationProcessExited, &exitCode); err != nil {
		t.Fatalf("finish %s: %v", execution.Descriptor().ID, err)
	}
	if err := manager.completeAt(execution.Descriptor().ID, completedAt); err != nil {
		t.Fatalf("complete %s: %v", execution.Descriptor().ID, err)
	}
}
