package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"minisandbox/internal/config"
	"minisandbox/internal/domain"
	"minisandbox/internal/testutil"
)

// stubIDGenerator 返回测试指定的 ID 或错误并记录调用次数。
type stubIDGenerator struct {
	id    string
	err   error
	calls int
}

// NewID 返回预先配置的测试结果。
func (g *stubIDGenerator) NewID() (string, error) {
	g.calls++
	return g.id, g.err
}

// recordingClock 返回固定时间并记录调用次数。
type recordingClock struct {
	now   time.Time
	calls int
}

// storeObservingWaker 在 Wake 时检查 Store.Create 是否已经完成调用。
type storeObservingWaker struct {
	store            *testutil.FakeStore
	storedBeforeWake bool
}

// Wake 记录通知发生时是否已经存在一次 Store.Create 调用。
func (w *storeObservingWaker) Wake(string) {
	w.storedBeforeWake = len(w.store.CreateCalls()) == 1
}

// Now 返回预先配置的测试时间。
func (c *recordingClock) Now() time.Time {
	c.calls++
	return c.now
}

// newSandboxServiceTestDependencies 创建使用安全默认配置的确定性 service 依赖。
func newSandboxServiceTestDependencies() (
	*testutil.FakeStore,
	*stubIDGenerator,
	*recordingClock,
	SandboxSpecBuilder,
	*testutil.FakeWaker,
) {
	cfg := config.Default()
	storeFake := testutil.NewFakeStore()
	idGenerator := &stubIDGenerator{
		id: "00010203-0405-4607-8809-0a0b0c0d0e0f",
	}
	clock := &recordingClock{
		now: time.Date(
			2027,
			6,
			7,
			8,
			9,
			10,
			123456789,
			time.FixedZone("UTC+8", 8*60*60),
		),
	}
	builder := NewSandboxSpecBuilder(
		cfg.DefaultSandboxSpec(),
		cfg.Limits.MaxResources,
	)
	return storeFake, idGenerator, clock, builder, testutil.NewFakeWaker()
}

// TestSandboxServiceCreate 验证创建用例只持久化一次完整 Pending 记录。
func TestSandboxServiceCreate(t *testing.T) {
	storeFake, idGenerator, clock, builder, waker :=
		newSandboxServiceTestDependencies()
	service := NewSandboxService(storeFake, idGenerator, clock, builder, waker)

	got, err := service.Create(
		context.Background(),
		CreateSandbox{Image: "alpine:3.22"},
	)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	wantTime := clock.now.UTC()
	wantSpec, err := builder.Build(CreateSandbox{Image: "alpine:3.22"})
	if err != nil {
		t.Fatalf("build expected spec: %v", err)
	}
	want := domain.Sandbox{
		ID:               idGenerator.id,
		Spec:             wantSpec,
		DesiredState:     domain.DesiredRunning,
		ObservedState:    domain.StatePending,
		Reason:           "CREATE_ACCEPTED",
		Message:          "Sandbox creation has been accepted.",
		SpecHash:         wantSpec.Hash(),
		Revision:         0,
		CreatedAt:        wantTime,
		UpdatedAt:        wantTime,
		LastTransitionAt: wantTime,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("created sandbox mismatch:\n got: %#v\nwant: %#v", got, want)
	}
	if idGenerator.calls != 1 || clock.calls != 1 {
		t.Fatalf("dependency calls: ID=%d clock=%d, want 1/1", idGenerator.calls, clock.calls)
	}
	if calls := storeFake.CreateCalls(); !reflect.DeepEqual(calls, []domain.Sandbox{want}) {
		t.Fatalf("Store.Create calls: %#v", calls)
	}
	if got := waker.WakeCalls(); !reflect.DeepEqual(got, []string{want.ID}) {
		t.Fatalf("Wake calls: got %v, want [%s]", got, want.ID)
	}
}

// TestSandboxServiceCreateWakesAfterStore 验证 Wake 严格发生在持久化成功之后。
func TestSandboxServiceCreateWakesAfterStore(t *testing.T) {
	storeFake, idGenerator, clock, builder, _ :=
		newSandboxServiceTestDependencies()
	waker := &storeObservingWaker{store: storeFake}
	service := NewSandboxService(storeFake, idGenerator, clock, builder, waker)

	if _, err := service.Create(
		context.Background(),
		CreateSandbox{Image: "alpine:3.22"},
	); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if !waker.storedBeforeWake {
		t.Fatal("Wake happened before Store.Create")
	}
}

// TestSandboxServiceCreateValidationError 验证校验失败不消耗 ID、时间或 Store。
func TestSandboxServiceCreateValidationError(t *testing.T) {
	storeFake, idGenerator, clock, builder, waker :=
		newSandboxServiceTestDependencies()
	service := NewSandboxService(storeFake, idGenerator, clock, builder, waker)

	got, err := service.Create(context.Background(), CreateSandbox{})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("create invalid sandbox: got %v, want ErrInvalid", err)
	}
	if !reflect.DeepEqual(got, domain.Sandbox{}) {
		t.Fatalf("validation error returned partial sandbox: %#v", got)
	}
	if idGenerator.calls != 0 || clock.calls != 0 || len(storeFake.CreateCalls()) != 0 {
		t.Fatalf(
			"validation error triggered dependencies: ID=%d clock=%d store=%d",
			idGenerator.calls,
			clock.calls,
			len(storeFake.CreateCalls()),
		)
	}
	if len(waker.WakeCalls()) != 0 {
		t.Fatalf("validation error called Wake: %v", waker.WakeCalls())
	}
}

// TestSandboxServiceCreateIDError 验证随机 ID 失败不会读取时间或持久化。
func TestSandboxServiceCreateIDError(t *testing.T) {
	storeFake, idGenerator, clock, builder, waker :=
		newSandboxServiceTestDependencies()
	injected := errors.New("random unavailable")
	idGenerator.err = injected
	service := NewSandboxService(storeFake, idGenerator, clock, builder, waker)

	got, err := service.Create(
		context.Background(),
		CreateSandbox{Image: "alpine:3.22"},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("create with ID error: got %v, want injected", err)
	}
	if !reflect.DeepEqual(got, domain.Sandbox{}) {
		t.Fatalf("ID error returned partial sandbox: %#v", got)
	}
	if idGenerator.calls != 1 || clock.calls != 0 || len(storeFake.CreateCalls()) != 0 {
		t.Fatalf(
			"ID error dependency calls: ID=%d clock=%d store=%d",
			idGenerator.calls,
			clock.calls,
			len(storeFake.CreateCalls()),
		)
	}
	if len(waker.WakeCalls()) != 0 {
		t.Fatalf("ID error called Wake: %v", waker.WakeCalls())
	}
}

// TestSandboxServiceCreateStoreErrors 验证 Store 分类保持且不返回未持久化对象。
func TestSandboxServiceCreateStoreErrors(t *testing.T) {
	unavailable := errors.New("store unavailable")
	tests := []struct {
		name string
		err  error
	}{
		{name: "conflict", err: domain.ErrConflict},
		{name: "unavailable", err: unavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storeFake, idGenerator, clock, builder, waker :=
				newSandboxServiceTestDependencies()
			storeFake.SetCreateError(tt.err)
			service := NewSandboxService(
				storeFake,
				idGenerator,
				clock,
				builder,
				waker,
			)

			got, err := service.Create(
				context.Background(),
				CreateSandbox{Image: "alpine:3.22"},
			)
			if !errors.Is(err, tt.err) {
				t.Fatalf("create with Store error: got %v, want %v", err, tt.err)
			}
			if !reflect.DeepEqual(got, domain.Sandbox{}) {
				t.Fatalf("Store error returned partial sandbox: %#v", got)
			}
			if len(storeFake.CreateCalls()) != 1 {
				t.Fatalf("Store.Create calls: got %d, want 1", len(storeFake.CreateCalls()))
			}
			if len(waker.WakeCalls()) != 0 {
				t.Fatalf("Store error called Wake: %v", waker.WakeCalls())
			}
		})
	}
}

// TestSandboxServiceCreateAcceptsMissedWake 验证队列关闭不改写已落库创建结果。
func TestSandboxServiceCreateAcceptsMissedWake(t *testing.T) {
	storeFake, idGenerator, clock, builder, waker :=
		newSandboxServiceTestDependencies()
	waker.SetDeliver(false)
	service := NewSandboxService(storeFake, idGenerator, clock, builder, waker)

	got, err := service.Create(
		context.Background(),
		CreateSandbox{Image: "alpine:3.22"},
	)
	if err != nil {
		t.Fatalf("create with missed wake: %v", err)
	}
	if got.ID != idGenerator.id {
		t.Fatalf("created ID: got %q, want %q", got.ID, idGenerator.id)
	}
	if len(storeFake.CreateCalls()) != 1 {
		t.Fatalf("Store.Create calls: got %d, want 1", len(storeFake.CreateCalls()))
	}
	if wakeCalls := waker.WakeCalls(); !reflect.DeepEqual(
		wakeCalls,
		[]string{idGenerator.id},
	) {
		t.Fatalf("Wake attempts: %v", wakeCalls)
	}
	if delivered := waker.Delivered(); len(delivered) != 0 {
		t.Fatalf("closed fake delivered Wake: %v", delivered)
	}
}
