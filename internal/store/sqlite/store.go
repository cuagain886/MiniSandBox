// Package sqlite 承载 store 端口的 SQLite 持久化 adapter。
//
// 本模块设计上负责连接管理、schema 迁移和事务存取；当前已实现基于纯 Go
// driver 的连接打开与关闭，CRUD 与迁移由后续任务实现。它不决定生命周期
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

// Store 是 store 端口的 SQLite 实现。
type Store struct {
	db *sql.DB
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

	store := &Store{db: db}
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

// Get 按 ID 读取 sandbox 记录。
func (s *Store) Get(context.Context, string) (domain.Sandbox, error) {
	return domain.Sandbox{}, domain.ErrNotImplemented
}

// UpdateDesired 以 CAS 方式更新期望状态。
func (s *Store) UpdateDesired(
	context.Context,
	string,
	domain.DesiredState,
	uint64,
) (domain.Sandbox, error) {
	return domain.Sandbox{}, domain.ErrNotImplemented
}

// UpdateObserved 以 CAS 方式更新观测状态。
func (s *Store) UpdateObserved(
	context.Context,
	storeport.ObservedUpdate,
) (domain.Sandbox, error) {
	return domain.Sandbox{}, domain.ErrNotImplemented
}

// ListReconcileCandidates 返回仍需收敛的记录。
func (s *Store) ListReconcileCandidates(
	context.Context,
	int,
) ([]domain.Sandbox, error) {
	return nil, domain.ErrNotImplemented
}

// ListAll 返回全部持久化记录。
func (s *Store) ListAll(context.Context) ([]domain.Sandbox, error) {
	return nil, domain.ErrNotImplemented
}

var _ storeport.Store = (*Store)(nil)
