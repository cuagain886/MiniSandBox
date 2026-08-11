// Package sqlite 承载 store 端口的 SQLite 持久化 adapter。
//
// 本模块设计上负责连接管理、schema 迁移和事务存取；当前已实现基于纯 Go
// driver 的连接、迁移、sandbox 创建与读取、期望/观测状态 CAS 更新，以及
// reconcile candidate、全量恢复查询及 v2 lease/retry row mapping。它不
// 决定生命周期状态转换、鉴权或 runtime 行为，也不负责创建数据库父目录。
package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

const idempotencyResponseSchemaVersion uint32 = 1

const maxIdempotencyResponseBytes = 65_536

// cleanupPendingReason 标识仍有受管资源需要重试清理的失败记录。
const cleanupPendingReason = "CLEANUP_PENDING"

// Store 是 store 端口的 SQLite 实现。
type Store struct {
	db              *sql.DB
	path            string
	now             func() time.Time
	commitImmediate func(context.Context, *sql.Conn) error
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

// storedIdempotencyResponseV1 是 response_schema_version=1 的严格 JSON 形状。
//
// 它只用于重放前完整性校验，返回给调用方的仍是数据库中的原始 bytes。
type storedIdempotencyResponseV1 struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Image     string    `json:"image"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
		db:   db,
		path: path,
		now:  time.Now,
		commitImmediate: func(ctx context.Context, conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, "COMMIT")
			return err
		},
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

	expiresAt := sandbox.ExpiresAt
	if expiresAt == nil {
		// P3-010 必须让升级后的每条记录拥有非空租约；在 lifecycle application
		// 开始显式传入 TTL 前，沿用冻结的 30m 默认值保持滚动开发可运行。
		fallback := sandbox.CreatedAt.UTC().Add(migrationDefaultTTL)
		expiresAt = &fallback
	}
	origin := sandbox.Origin
	if origin == "" {
		origin = domain.SandboxOriginAPI
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
			last_transition_at,
			expires_at,
			retry_attempt,
			next_reconcile_at,
			last_reconcile_at,
			health_failure_count,
			origin
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		expiresAt.UTC().Format(time.RFC3339Nano),
		sandbox.RetryAttempt,
		storedNullableTime(sandbox.NextReconcileAt),
		storedNullableTime(sandbox.LastReconcileAt),
		sandbox.HealthFailureCount,
		origin,
	)
	if err == nil {
		return nil
	}
	if isDuplicateConstraint(err) {
		return fmt.Errorf("create sandbox: %w", domain.ErrConflict)
	}
	return fmt.Errorf("create sandbox: %w", err)
}

// CreateIdempotent 在独占写事务中原子创建 sandbox 与首次 202 响应记录。
//
// 事务先检查 scope/key，避免为已存在身份生成孤立 sandbox；任何 sandbox、
// idempotency 或 commit 失败都会执行 ROLLBACK。P3-019 对已有 key 只返回冲突，
// 精确重放由后续任务扩展同一事务分支。
func (s *Store) CreateIdempotent(
	ctx context.Context,
	request storeport.IdempotentCreateRequest,
) (storeport.IdempotentCreateResult, error) {
	if err := validateIdempotentCreateRequest(request); err != nil {
		return storeport.IdempotentCreateResult{}, err
	}
	specJSON, err := json.Marshal(storedSpecFromDomain(request.Sandbox.Spec))
	if err != nil {
		return storeport.IdempotentCreateResult{}, fmt.Errorf("encode idempotent sandbox spec: %w", err)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return storeport.IdempotentCreateResult{}, fmt.Errorf("acquire idempotent create connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storeport.IdempotentCreateResult{}, fmt.Errorf("begin idempotent create: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var (
		existingHash      string
		existingSandboxID string
		existingStatus    int
		existingLocation  string
		existingBody      []byte
		existingCreatedAt string
	)
	err = conn.QueryRowContext(ctx,
		`SELECT request_hash, sandbox_id, status_code, location, response_json, created_at
		FROM idempotency_records WHERE scope_id = ? AND idempotency_key = ?`,
		request.ScopeID,
		request.Key,
	).Scan(&existingHash, &existingSandboxID, &existingStatus, &existingLocation, &existingBody, &existingCreatedAt)
	if err == nil {
		if existingHash != request.RequestHash {
			return storeport.IdempotentCreateResult{}, domain.ErrConflict
		}
		response, err := decodeIdempotentResponse(
			existingSandboxID,
			existingStatus,
			existingLocation,
			existingBody,
			existingCreatedAt,
		)
		if err != nil {
			return storeport.IdempotentCreateResult{}, err
		}
		return storeport.IdempotentCreateResult{
			Sandbox:  domain.Sandbox{ID: existingSandboxID},
			Response: response,
			Replayed: true,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storeport.IdempotentCreateResult{}, fmt.Errorf("check idempotency record: %w", err)
	}

	sandbox := request.Sandbox
	result, err := conn.ExecContext(ctx, `INSERT INTO sandboxes (
		id, spec_json, desired_state, observed_state, reason, message, runtime_id,
		spec_hash, revision, created_at, updated_at, last_transition_at, expires_at,
		retry_attempt, next_reconcile_at, last_reconcile_at, health_failure_count, origin
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		sandbox.ExpiresAt.UTC().Format(time.RFC3339Nano),
		sandbox.RetryAttempt,
		storedNullableTime(sandbox.NextReconcileAt),
		storedNullableTime(sandbox.LastReconcileAt),
		sandbox.HealthFailureCount,
		sandbox.Origin,
	)
	if err != nil {
		if isDuplicateConstraint(err) {
			return storeport.IdempotentCreateResult{}, domain.ErrConflict
		}
		return storeport.IdempotentCreateResult{}, fmt.Errorf("insert idempotent sandbox: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return storeport.IdempotentCreateResult{}, fmt.Errorf("insert idempotent sandbox result: affected=%d error=%w", affected, err)
	}

	response := request.Response
	if _, err := conn.ExecContext(ctx, `INSERT INTO idempotency_records (
		scope_id, idempotency_key, request_hash, sandbox_id, status_code,
		location, response_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		request.ScopeID,
		request.Key,
		request.RequestHash,
		sandbox.ID,
		response.StatusCode,
		response.Location,
		response.Body,
		response.CreatedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return storeport.IdempotentCreateResult{}, fmt.Errorf("insert idempotency record: %w", err)
	}
	created, err := scanSandbox(conn.QueryRowContext(ctx, selectSandboxByIDQuery, sandbox.ID))
	if err != nil {
		return storeport.IdempotentCreateResult{}, fmt.Errorf("read idempotent sandbox: %w", err)
	}
	if err := s.commitImmediate(ctx, conn); err != nil {
		return storeport.IdempotentCreateResult{}, fmt.Errorf("commit idempotent create: %w", err)
	}
	committed = true
	response.Body = append([]byte(nil), response.Body...)
	return storeport.IdempotentCreateResult{Sandbox: created, Response: response}, nil
}

// decodeIdempotentResponse 在返回原始 bytes 前验证大小、schema v1 和 sandbox 关联。
func decodeIdempotentResponse(
	sandboxID string,
	statusCode int,
	location string,
	body []byte,
	createdAtText string,
) (storeport.IdempotentResponse, error) {
	if statusCode != 202 || location != "/v1/sandboxes/"+sandboxID ||
		len(body) < 2 || len(body) > maxIdempotencyResponseBytes {
		return storeport.IdempotentResponse{}, fmt.Errorf("decode idempotency response: %w", storeport.ErrCorrupt)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var schema storedIdempotencyResponseV1
	if err := decoder.Decode(&schema); err != nil {
		return storeport.IdempotentResponse{}, fmt.Errorf("decode idempotency response schema: %w", storeport.ErrCorrupt)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return storeport.IdempotentResponse{}, fmt.Errorf("decode idempotency response trailing data: %w", storeport.ErrCorrupt)
	}
	if schema.ID != sandboxID || schema.ID == "" || schema.State == "" || schema.Reason == "" ||
		schema.Message == "" || schema.Image == "" || schema.ExpiresAt.IsZero() ||
		schema.CreatedAt.IsZero() || schema.UpdatedAt.IsZero() ||
		schema.ExpiresAt.Location() != time.UTC || schema.CreatedAt.Location() != time.UTC ||
		schema.UpdatedAt.Location() != time.UTC {
		return storeport.IdempotentResponse{}, fmt.Errorf("validate idempotency response schema: %w", storeport.ErrCorrupt)
	}
	createdAt, err := parseStoredTime("idempotency_created_at", createdAtText)
	if err != nil {
		return storeport.IdempotentResponse{}, err
	}
	return storeport.IdempotentResponse{
		SchemaVersion: idempotencyResponseSchemaVersion,
		StatusCode:    statusCode,
		Location:      location,
		Body:          append([]byte(nil), body...),
		CreatedAt:     createdAt,
	}, nil
}

// validateIdempotentCreateRequest 在开启写事务前拒绝不可能通过 schema 的输入。
func validateIdempotentCreateRequest(request storeport.IdempotentCreateRequest) error {
	response := request.Response
	if !validIdempotencyToken(request.ScopeID, 64) ||
		!validIdempotencyToken(request.Key, 128) ||
		!validLowerHexHash(request.RequestHash) ||
		request.Sandbox.ExpiresAt == nil ||
		request.Sandbox.CreatedAt.IsZero() || request.Sandbox.UpdatedAt.IsZero() ||
		request.Sandbox.LastTransitionAt.IsZero() ||
		response.SchemaVersion != idempotencyResponseSchemaVersion ||
		response.StatusCode != 202 || response.CreatedAt.IsZero() ||
		len(response.Location) < 1 || len(response.Location) > 1024 ||
		len(response.Body) < 2 || len(response.Body) > maxIdempotencyResponseBytes ||
		!json.Valid(response.Body) {
		return fmt.Errorf("validate idempotent create: %w", domain.ErrInvalid)
	}
	if request.Sandbox.Origin != domain.SandboxOriginAPI {
		return fmt.Errorf("validate idempotent create origin: %w", domain.ErrInvalid)
	}
	if _, err := decodeIdempotentResponse(
		request.Sandbox.ID,
		response.StatusCode,
		response.Location,
		response.Body,
		response.CreatedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("validate idempotent response schema: %w", domain.ErrInvalid)
	}
	return nil
}

// validIdempotencyToken 镜像 schema 的 ASCII token 约束而不回显原值。
func validIdempotencyToken(value string, limit int) bool {
	if len(value) < 1 || len(value) > limit {
		return false
	}
	for index := range value {
		character := value[index]
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' || character == '.' || character == '_' ||
			character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// validLowerHexHash 验证固定长度 lowercase SHA-256 hex。
func validLowerHexHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' && (value[index] < 'a' || value[index] > 'f') {
			return false
		}
	}
	return true
}

// storedNullableTime 把可选 UTC 时间转换为 SQLite NULL 或 RFC3339Nano 文本。
func storedNullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
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

// Renew 只允许在租约尚未到期时延长 DesiredRunning 的非终态记录。
func (s *Store) Renew(
	ctx context.Context,
	update storeport.RenewUpdate,
) (domain.Sandbox, error) {
	if update.Now.IsZero() || update.ExpiresAt.IsZero() || !update.ExpiresAt.After(update.Now) {
		return domain.Sandbox{}, fmt.Errorf("renew sandbox lease: %w", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("begin renew transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := update.Now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(
		ctx,
		`UPDATE sandboxes
		SET expires_at = ?, next_reconcile_at = ?, revision = revision + 1, updated_at = ?
		WHERE id = ? AND revision = ? AND desired_state = ? AND observed_state <> ?
			AND expires_at > ? AND expires_at < ?`,
		update.ExpiresAt.UTC().Format(time.RFC3339Nano),
		now,
		now,
		update.ID,
		update.ExpectedRevision,
		domain.DesiredRunning,
		domain.StateTerminated,
		now,
		update.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("renew sandbox lease: %w", err)
	}
	return completeSandboxMutation(ctx, tx, update.ID, result, "renew sandbox lease")
}

// ExpireIntent 只允许匹配当前 expiry 的已到期 Running 租约提交删除意图。
func (s *Store) ExpireIntent(
	ctx context.Context,
	update storeport.ExpireIntentUpdate,
) (domain.Sandbox, error) {
	if update.Now.IsZero() || update.ExpectedExpiresAt.IsZero() || update.Now.Before(update.ExpectedExpiresAt) {
		return domain.Sandbox{}, fmt.Errorf("expire sandbox lease: %w", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("begin expire transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := update.Now.UTC().Format(time.RFC3339Nano)
	expectedExpiry := update.ExpectedExpiresAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(
		ctx,
		`UPDATE sandboxes
		SET desired_state = ?, next_reconcile_at = ?, revision = revision + 1, updated_at = ?
		WHERE id = ? AND revision = ? AND desired_state = ?
			AND observed_state <> ? AND expires_at = ? AND expires_at <= ?`,
		domain.DesiredTerminated,
		now,
		now,
		update.ID,
		update.ExpectedRevision,
		domain.DesiredRunning,
		domain.StateTerminated,
		expectedExpiry,
		now,
	)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("expire sandbox lease: %w", err)
	}
	return completeSandboxMutation(ctx, tx, update.ID, result, "expire sandbox lease")
}

// ScheduleRetry 原子写入失败观测、自增 attempt 并持久化实际选定的 backoff 时间。
func (s *Store) ScheduleRetry(
	ctx context.Context,
	update storeport.RetryUpdate,
) (domain.Sandbox, error) {
	if update.AttemptedAt.IsZero() || update.NextReconcileAt.IsZero() ||
		!update.NextReconcileAt.After(update.AttemptedAt) || strings.TrimSpace(update.Reason) == "" {
		return domain.Sandbox{}, fmt.Errorf("schedule sandbox retry: %w", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("begin retry transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	attemptedAt := update.AttemptedAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(
		ctx,
		`UPDATE sandboxes
		SET observed_state = ?, reason = ?, message = ?,
			retry_attempt = retry_attempt + 1, next_reconcile_at = ?, last_reconcile_at = ?,
			revision = revision + 1, updated_at = ?,
			last_transition_at = CASE WHEN observed_state <> ? THEN ? ELSE last_transition_at END
		WHERE id = ? AND revision = ? AND observed_state <> ? AND retry_attempt < 4294967295`,
		domain.StateFailed,
		update.Reason,
		update.Message,
		update.NextReconcileAt.UTC().Format(time.RFC3339Nano),
		attemptedAt,
		attemptedAt,
		domain.StateFailed,
		attemptedAt,
		update.ID,
		update.ExpectedRevision,
		domain.StateTerminated,
	)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("schedule sandbox retry: %w", err)
	}
	return completeSandboxMutation(ctx, tx, update.ID, result, "schedule sandbox retry")
}

// ResetRetry 原子持久化成功观测，并清除会造成重复唤醒的旧 retry metadata。
func (s *Store) ResetRetry(
	ctx context.Context,
	update storeport.RetryResetUpdate,
) (domain.Sandbox, error) {
	if update.ReconciledAt.IsZero() {
		return domain.Sandbox{}, fmt.Errorf("reset sandbox retry: %w", domain.ErrInvalid)
	}
	if _, err := parseObservedState(string(update.Observed.State)); err != nil {
		return domain.Sandbox{}, fmt.Errorf("reset sandbox retry: %w", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("begin retry reset transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	reconciledAt := update.ReconciledAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(
		ctx,
		`UPDATE sandboxes
		SET observed_state = ?, reason = ?, message = ?, runtime_id = ?,
			retry_attempt = 0, next_reconcile_at = NULL, last_reconcile_at = ?,
			revision = revision + 1, updated_at = ?,
			last_transition_at = CASE WHEN observed_state <> ? THEN ? ELSE last_transition_at END
		WHERE id = ? AND revision = ? AND (
			(desired_state = ? AND ? = ?) OR
			(desired_state = ? AND ? = ?)
		)`,
		update.Observed.State,
		update.Observed.Reason,
		update.Observed.Message,
		update.Observed.RuntimeID,
		reconciledAt,
		reconciledAt,
		update.Observed.State,
		reconciledAt,
		update.Observed.ID,
		update.Observed.ExpectedRevision,
		domain.DesiredRunning,
		update.Observed.State,
		domain.StateRunning,
		domain.DesiredTerminated,
		update.Observed.State,
		domain.StateTerminated,
	)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("reset sandbox retry: %w", err)
	}
	return completeSandboxMutation(ctx, tx, update.Observed.ID, result, "reset sandbox retry")
}

// RecordHealthResult 只允许更新仍处于 Running 的活跃记录，避免旧 probe 覆盖删除意图。
func (s *Store) RecordHealthResult(
	ctx context.Context,
	update storeport.HealthResultUpdate,
) (domain.Sandbox, error) {
	if update.CheckedAt.IsZero() {
		return domain.Sandbox{}, fmt.Errorf("record sandbox health result: %w", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("begin health result transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	checkedAt := update.CheckedAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(
		ctx,
		`UPDATE sandboxes
		SET health_failure_count = CASE
				WHEN ? THEN 0
				WHEN health_failure_count < 4294967295 THEN health_failure_count + 1
				ELSE health_failure_count
			END,
			last_reconcile_at = ?, revision = revision + 1, updated_at = ?
		WHERE id = ? AND revision = ? AND desired_state = ? AND observed_state = ?`,
		update.Healthy,
		checkedAt,
		checkedAt,
		update.ID,
		update.ExpectedRevision,
		domain.DesiredRunning,
		domain.StateRunning,
	)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("record sandbox health result: %w", err)
	}
	return completeSandboxMutation(ctx, tx, update.ID, result, "record sandbox health result")
}

// completeSandboxMutation 在同一事务快照内把零影响行区分为 NotFound 或 Conflict。
func completeSandboxMutation(
	ctx context.Context,
	tx *sql.Tx,
	id string,
	result sql.Result,
	operation string,
) (domain.Sandbox, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("%s result: %w", operation, err)
	}
	if affected > 1 {
		return domain.Sandbox{}, fmt.Errorf("%s: expected at most one affected row, got %d", operation, affected)
	}
	current, err := scanSandbox(tx.QueryRowContext(ctx, selectSandboxByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Sandbox{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("read %s result: %w", operation, err)
	}
	if affected == 0 {
		return domain.Sandbox{}, domain.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return domain.Sandbox{}, fmt.Errorf("commit %s: %w", operation, err)
	}
	return current, nil
}

// ListReconcileCandidates 按 ID keyset 返回当前真正 due 的记录。
//
// expiry 绕过旧 retry backoff；其他状态转换、cleanup、scheduled retry 和
// Running health check 都受 next_reconcile_at 门控。普通不可重试 Failed 和
// 稳定 Terminated 不进入候选。查询不修改 retry metadata，也不持有 cursor。
func (s *Store) ListReconcileCandidates(
	ctx context.Context,
	query storeport.ReconcileCandidateQuery,
) ([]domain.Sandbox, error) {
	if query.Now.IsZero() || query.RunningCutoff.IsZero() || query.Limit < 1 {
		return nil, fmt.Errorf(
			"list reconcile candidates: %w",
			domain.ErrInvalid,
		)
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT `+sandboxSelectColumns+`
		FROM sandboxes
		WHERE id > ? AND (
			(
				desired_state = ? AND
				observed_state <> ? AND
				expires_at <= ?
			) OR (
				(next_reconcile_at IS NULL OR next_reconcile_at <= ?) AND (
					observed_state IN (?, ?, ?) OR
					(desired_state = ? AND observed_state <> ?) OR
					(desired_state = ? AND observed_state = ?) OR
					(
						observed_state = ? AND
						(desired_state = ? OR reason = ? OR next_reconcile_at IS NOT NULL)
					) OR
					(
						desired_state = ? AND observed_state = ? AND
						(last_reconcile_at IS NULL OR last_reconcile_at <= ?)
					)
				)
			)
		)
		ORDER BY id ASC
		LIMIT ?`,
		query.AfterID,
		domain.DesiredRunning,
		domain.StateTerminated,
		query.Now.UTC().Format(time.RFC3339Nano),
		query.Now.UTC().Format(time.RFC3339Nano),
		domain.StatePending,
		domain.StateCreating,
		domain.StateStopping,
		domain.DesiredTerminated,
		domain.StateTerminated,
		domain.DesiredRunning,
		domain.StateTerminated,
		domain.StateFailed,
		domain.DesiredTerminated,
		cleanupPendingReason,
		domain.DesiredRunning,
		domain.StateRunning,
		query.RunningCutoff.UTC().Format(time.RFC3339Nano),
		query.Limit,
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

// ListAll 按创建时间和 ID 稳定返回全部持久化记录。
//
// 本方法用于启动恢复和诊断，不读取 Docker，也不修改任何生命周期状态；
// 空数据库返回非 nil 空切片。
func (s *Store) ListAll(ctx context.Context) ([]domain.Sandbox, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT `+sandboxSelectColumns+`
		FROM sandboxes
		ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query all sandboxes: %w", err)
	}
	defer rows.Close()

	sandboxes := make([]domain.Sandbox, 0)
	for rows.Next() {
		sandbox, err := scanSandbox(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sandbox: %w", err)
		}
		sandboxes = append(sandboxes, sandbox)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sandboxes: %w", err)
	}
	return sandboxes, nil
}

var _ storeport.Store = (*Store)(nil)
