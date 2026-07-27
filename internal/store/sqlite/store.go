// Package sqlite 承载 store 端口的 SQLite 持久化 adapter。
//
// 本模块设计上负责连接管理、schema 迁移和事务存取；当前已实现基于纯 Go
// driver 的连接、迁移、sandbox 创建与读取、期望/观测状态 CAS 更新，以及
// reconcile candidate 查询；全量恢复查询由后续任务实现。它不决定生命周期
// 状态转换、鉴权或 runtime 行为，也不负责创建数据库父目录。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	// 注册 CGo-free 的 "sqlite" database/sql driver（ADR-0001），并用于识别结构化错误码。
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// busyTimeoutMillis 是等待数据库锁的毫秒数，超时前重试而不是立即报错。
const busyTimeoutMillis = 5000

// cleanupPendingReason 标识仍有受管资源需要重试清理的失败记录。
const cleanupPendingReason = "CLEANUP_PENDING"

// Store 是 store 端口的 SQLite 实现。
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// storedSandboxSpec 是 SQLite 中 resolved spec 的稳定 JSON 表示。
//
// 持久化结构与领域结构显式转换，避免 Go 字段重命名或默认 JSON 字段名变化
// 破坏已存在数据库的重启恢复能力。
type storedSandboxSpec struct {
	Image     string               `json:"image"`
	Resources storedResourceLimits `json:"resources"`
	Workspace storedWorkspaceSpec  `json:"workspace"`
	Network   storedNetworkSpec    `json:"network"`
	Platform  storedPlatform       `json:"platform"`
}

// storedResourceLimits 保存资源限制及其稳定字段名。
type storedResourceLimits struct {
	CPUQuotaMillis int64 `json:"cpu_quota_millis"`
	MemoryMiB      int64 `json:"memory_mib"`
	PIDs           int64 `json:"pids"`
}

// storedWorkspaceSpec 保存 workspace 的容器内语义。
type storedWorkspaceSpec struct {
	MountPath  string `json:"mount_path"`
	Persistent bool   `json:"persistent"`
}

// storedNetworkSpec 保存 sandbox 的出站网络能力。
type storedNetworkSpec struct {
	Outbound bool `json:"outbound"`
}

// storedPlatform 保存嵌入式产物必须匹配的目标平台。
type storedPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Open 打开指定路径的 SQLite 数据库并验证连接可用。
//
// 连接固定启用 WAL、外键约束和 busy timeout；SQLite 同一时刻只有单个
// writer，连接池固定为 1 个连接，避免池内连接互相触发 SQLITE_BUSY。
// 父目录必须已由受管目录 helper 创建；本函数不创建目录，失败时不留下
// 打开的连接。
func Open(path string) (*Store, error) {
	// DSN 使用 file: URI 语法，路径中的 #、% 和 ? 会被当作 URI 结构解析，
	// 导致数据库被静默创建到错误位置。受管路径不应包含这些字符，出现时
	// 直接拒绝，避免对错误文件通过连接校验。
	if strings.ContainsAny(path, "#%?") {
		return nil, fmt.Errorf(
			"database path must not contain '#', '%%' or '?'",
		)
	}
	dsn := "file:" + path +
		"?_pragma=busy_timeout(" + fmt.Sprint(busyTimeoutMillis) + ")" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &Store{
		db:  db,
		now: time.Now,
	}
	if err := store.verifyConnection(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close 关闭数据库连接，重复调用保持幂等。
func (s *Store) Close() error {
	return s.db.Close()
}

// verifyConnection 确认连接可 Ping 且关键 PRAGMA 实际生效。
//
// WAL 等 PRAGMA 在部分文件系统上可能静默回退，显式读取结果可以在启动时
// 立即暴露问题，而不是等到运行期出现数据一致性风险。
func (s *Store) verifyConnection() error {
	if err := s.db.Ping(); err != nil {
		return fmt.Errorf("ping sqlite database: %w", err)
	}

	var journalMode string
	if err := s.db.QueryRow(
		"PRAGMA journal_mode",
	).Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if journalMode != "wal" {
		return fmt.Errorf(
			"journal_mode is %q, want wal",
			journalMode,
		)
	}

	var foreignKeys int64
	if err := s.db.QueryRow(
		"PRAGMA foreign_keys",
	).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign_keys is %d, want 1", foreignKeys)
	}

	var busyTimeout int64
	if err := s.db.QueryRow(
		"PRAGMA busy_timeout",
	).Scan(&busyTimeout); err != nil {
		return fmt.Errorf("read busy_timeout: %w", err)
	}
	if busyTimeout != busyTimeoutMillis {
		return fmt.Errorf(
			"busy_timeout is %d, want %d",
			busyTimeout,
			busyTimeoutMillis,
		)
	}
	return nil
}

// Create 原子持久化一条新的 Pending/DesiredRunning sandbox 记录。
//
// 调用方负责提供已经校验的 resolved spec 和初始状态；Create 把首个持久化
// revision 固定为 1。ID 已存在时返回 domain.ErrConflict，调用被取消时保留
// context 的错误语义，便于上层区分冲突与未完成写入。
func (s *Store) Create(ctx context.Context, sandbox domain.Sandbox) error {
	specJSON, err := json.Marshal(storedSpecFromDomain(sandbox.Spec))
	if err != nil {
		return fmt.Errorf("encode sandbox spec: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO sandboxes (
			id,
			spec_json,
			desired_state,
			observed_state,
			reason,
			message,
			runtime_id,
			spec_hash,
			revision,
			created_at,
			updated_at,
			last_transition_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sandbox.ID,
		specJSON,
		sandbox.DesiredState,
		sandbox.ObservedState,
		sandbox.Reason,
		sandbox.Message,
		sandbox.RuntimeID,
		sandbox.SpecHash,
		uint64(1),
		sandbox.CreatedAt.UTC().Format(time.RFC3339Nano),
		sandbox.UpdatedAt.UTC().Format(time.RFC3339Nano),
		sandbox.LastTransitionAt.UTC().Format(time.RFC3339Nano),
	)
	if err == nil {
		return nil
	}
	if isDuplicateConstraint(err) {
		return fmt.Errorf("create sandbox: %w", domain.ErrConflict)
	}
	return fmt.Errorf("create sandbox: %w", err)
}

// storedSpecFromDomain 把领域规格转换为不依赖领域字段名的持久化结构。
func storedSpecFromDomain(spec domain.SandboxSpec) storedSandboxSpec {
	return storedSandboxSpec{
		Image: spec.Image,
		Resources: storedResourceLimits{
			CPUQuotaMillis: spec.Resources.CPUQuotaMillis,
			MemoryMiB:      spec.Resources.MemoryMiB,
			PIDs:           spec.Resources.PIDs,
		},
		Workspace: storedWorkspaceSpec{
			MountPath:  spec.Workspace.MountPath,
			Persistent: spec.Workspace.Persistent,
		},
		Network: storedNetworkSpec{
			Outbound: spec.Network.Outbound,
		},
		Platform: storedPlatform{
			OS:   spec.Platform.OS,
			Arch: spec.Platform.Arch,
		},
	}
}

// isDuplicateConstraint 只识别主键或唯一键冲突。
//
// 其他约束错误通常表示 schema 或写入逻辑缺陷，不能伪装成客户端可重试的
// ID 冲突。使用 driver 的结构化扩展错误码，避免依赖可能变化的错误文本。
func isDuplicateConstraint(err error) bool {
	var sqliteErr *sqliteDriver.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY ||
		sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

// Get 按 ID 完整还原 sandbox 记录。
//
// ID 不存在时返回 domain.ErrNotFound；记录不能安全解析时返回
// store.ErrCorrupt。Get 只读取 SQLite，不查询或修正 Docker 实际状态。
func (s *Store) Get(ctx context.Context, id string) (domain.Sandbox, error) {
	sandbox, err := scanSandbox(s.db.QueryRowContext(
		ctx,
		selectSandboxByIDQuery,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Sandbox{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("get sandbox: %w", err)
	}
	return sandbox, nil
}

// UpdateDesired 幂等提交 DesiredTerminated 并返回完整的新记录。
//
// Running 到 Terminated 的实际转换要求 expectedRevision 匹配，成功时 revision
// 加一并更新 updated time，但绝不直接修改 observed state 或 transition time。
// 已经 DesiredTerminated 时返回当前记录作为 no-op，即使调用方因响应丢失仍
// 携带旧 revision；不存在返回 domain.ErrNotFound，旧 revision 返回
// domain.ErrConflict。
func (s *Store) UpdateDesired(
	ctx context.Context,
	id string,
	desired domain.DesiredState,
	expectedRevision uint64,
) (domain.Sandbox, error) {
	if desired != domain.DesiredTerminated {
		return domain.Sandbox{}, fmt.Errorf(
			"update desired state: %w",
			domain.ErrInvalid,
		)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"begin desired update transaction: %w",
			err,
		)
	}
	// 条件 UPDATE 之后的分类读取和成功回读必须与写入共用事务；否则并发
	// 更新可能让 no-op、NotFound 与 Conflict 的判断基于不同快照。
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(
		ctx,
		`UPDATE sandboxes
		SET
			desired_state = ?,
			revision = revision + 1,
			updated_at = ?
		WHERE
			id = ? AND
			revision = ? AND
			desired_state = ?`,
		desired,
		now,
		id,
		expectedRevision,
		domain.DesiredRunning,
	)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"update desired sandbox: %w",
			err,
		)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"read desired update result: %w",
			err,
		)
	}
	if affected > 1 {
		return domain.Sandbox{}, fmt.Errorf(
			"update desired sandbox: expected at most one affected row, got %d",
			affected,
		)
	}

	current, err := scanSandbox(tx.QueryRowContext(
		ctx,
		selectSandboxByIDQuery,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Sandbox{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"read desired sandbox: %w",
			err,
		)
	}
	if affected == 0 && current.DesiredState != desired {
		return domain.Sandbox{}, domain.ErrConflict
	}

	if err := tx.Commit(); err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"commit desired update: %w",
			err,
		)
	}
	return current, nil
}

// UpdateObserved 以 expected revision 为条件更新观测状态并返回完整新记录。
//
// 更新、影响行数检查和回读处于同一事务；CAS 未命中统一返回
// domain.ErrConflict。状态发生变化时同步更新 last transition，相同状态则
// 保留原 transition 时间。任何回读或提交失败都会回滚，不暴露部分更新。
func (s *Store) UpdateObserved(
	ctx context.Context,
	update storeport.ObservedUpdate,
) (domain.Sandbox, error) {
	if _, err := parseObservedState(string(update.State)); err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"update observed state: %w",
			domain.ErrInvalid,
		)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"begin observed update transaction: %w",
			err,
		)
	}
	// Commit 成功后 Rollback 返回 sql.ErrTxDone，是安全的空操作；其他失败
	// 路径依靠这里撤销可能已经执行的 UPDATE。
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(
		ctx,
		`UPDATE sandboxes
		SET
			observed_state = ?,
			reason = ?,
			message = ?,
			runtime_id = ?,
			revision = revision + 1,
			updated_at = ?,
			last_transition_at = CASE
				WHEN observed_state <> ? THEN ?
				ELSE last_transition_at
			END
		WHERE id = ? AND revision = ?`,
		update.State,
		update.Reason,
		update.Message,
		update.RuntimeID,
		now,
		update.State,
		now,
		update.ID,
		update.ExpectedRevision,
	)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"update observed sandbox: %w",
			err,
		)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"read observed update result: %w",
			err,
		)
	}
	if affected == 0 {
		return domain.Sandbox{}, domain.ErrConflict
	}
	if affected != 1 {
		return domain.Sandbox{}, fmt.Errorf(
			"update observed sandbox: expected one affected row, got %d",
			affected,
		)
	}

	updated, err := scanSandbox(tx.QueryRowContext(
		ctx,
		selectSandboxByIDQuery,
		update.ID,
	))
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"read updated sandbox: %w",
			err,
		)
	}
	if err := tx.Commit(); err != nil {
		return domain.Sandbox{}, fmt.Errorf(
			"commit observed update: %w",
			err,
		)
	}
	return updated, nil
}

// ListReconcileCandidates 按稳定顺序返回最多 limit 条仍需收敛的记录。
//
// Pending、Creating 和 Stopping 始终需要继续处理；稳定的 Running/Terminated
// 只在 desired 不一致时处理；Failed 仅在 CLEANUP_PENDING 或已提交终止意图
// 时处理，避免普通不可重试创建失败形成忙循环。limit 必须为正数。
func (s *Store) ListReconcileCandidates(
	ctx context.Context,
	limit int,
) ([]domain.Sandbox, error) {
	if limit < 1 {
		return nil, fmt.Errorf(
			"list reconcile candidates: %w",
			domain.ErrInvalid,
		)
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT `+sandboxSelectColumns+`
		FROM sandboxes
		WHERE
			(
				desired_state <> observed_state AND
				observed_state IN (?, ?)
			) OR
			observed_state IN (?, ?, ?) OR
			(
				observed_state = ? AND
				(desired_state = ? OR reason = ?)
			)
		ORDER BY created_at ASC, id ASC
		LIMIT ?`,
		domain.StateRunning,
		domain.StateTerminated,
		domain.StatePending,
		domain.StateCreating,
		domain.StateStopping,
		domain.StateFailed,
		domain.DesiredTerminated,
		cleanupPendingReason,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"query reconcile candidates: %w",
			err,
		)
	}
	defer rows.Close()

	candidates := make([]domain.Sandbox, 0)
	for rows.Next() {
		sandbox, err := scanSandbox(rows)
		if err != nil {
			return nil, fmt.Errorf(
				"scan reconcile candidate: %w",
				err,
			)
		}
		candidates = append(candidates, sandbox)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate reconcile candidates: %w",
			err,
		)
	}
	return candidates, nil
}

// ListAll 返回全部持久化记录。
func (s *Store) ListAll(context.Context) ([]domain.Sandbox, error) {
	return nil, domain.ErrNotImplemented
}

var _ storeport.Store = (*Store)(nil)
