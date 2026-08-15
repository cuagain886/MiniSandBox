package application

import (
	"context"
	"errors"
	"io"

	"minisandbox/internal/domain"
	"minisandbox/internal/store"
	"minisandbox/pkg/protocol"
)

// FilesClient 是 application 调用 runner 文件能力的固定端口。
//
// 实现绑定到单个 sandbox，不暴露 runner URL、token 或 socket；路径一律
// 使用公共 workspace 相对规则。
type FilesClient interface {
	// Capabilities 查询 runner 实际提供的功能集合。
	Capabilities(context.Context) (protocol.Capabilities, error)
	// FileStat 查询单个路径的 metadata。
	FileStat(context.Context, protocol.FileStatRequest) (protocol.FileStat, error)
	// DirectoryList 列出目录直接子项。
	DirectoryList(context.Context, protocol.DirectoryListRequest) (protocol.DirectoryListing, error)
	// Mkdir 创建目录并区分新建与已存在。
	Mkdir(context.Context, protocol.MkdirRequest) (bool, protocol.FileStat, error)
	// Upload 流式上传；reader 由调用方提供且不被重放。
	Upload(context.Context, string, io.Reader, bool, bool) (bool, protocol.FileStat, error)
	// Download 打开流式下载；调用方负责关闭返回的 reader。
	Download(context.Context, string) (io.ReadCloser, protocol.FileStat, error)
	// Move 在 workspace 内移动路径。
	Move(context.Context, protocol.FileMoveRequest) (protocol.FileStat, error)
	// Delete 幂等删除文件或目录。
	Delete(context.Context, protocol.FileDeleteRequest) error
}

// FilesClientFactory 只允许按已通过 Store gate 的 sandbox ID 选择文件 client。
type FilesClientFactory interface {
	// Client 返回绑定到指定 sandbox 的固定 client。
	Client(sandboxID string) (FilesClient, error)
}

// FilesService 在 Store 生命周期 gate 后组织 sandbox 文件用例。
//
// 本服务不解释文件路径语义、不访问宿主机文件系统；所有操作都转发给
// 当前 sandbox 的 runner client。
type FilesService struct {
	store   store.Store
	factory FilesClientFactory
}

// NewFilesService 创建文件应用服务。
func NewFilesService(s store.Store, factory FilesClientFactory) (*FilesService, error) {
	if s == nil || factory == nil {
		return nil, errors.New("files service is not configured")
	}
	return &FilesService{store: s, factory: factory}, nil
}

// Capabilities 返回当前 sandbox runner 的能力集合。
func (s *FilesService) Capabilities(ctx context.Context, sandboxID string) (protocol.Capabilities, error) {
	client, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return protocol.Capabilities{}, err
	}
	capabilities, err := client.Capabilities(ctx)
	if err != nil {
		return protocol.Capabilities{}, mapFilesClientError(err)
	}
	return capabilities, nil
}

// Stat 查询单个 workspace 路径的 metadata。
func (s *FilesService) Stat(ctx context.Context, sandboxID string, request protocol.FileStatRequest) (protocol.FileStat, error) {
	if err := request.Validate(); err != nil {
		return protocol.FileStat{}, domain.ErrInvalidFilePath
	}
	client, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return protocol.FileStat{}, err
	}
	stat, err := client.FileStat(ctx, request)
	if err != nil {
		return protocol.FileStat{}, mapFilesClientError(err)
	}
	return stat, nil
}

// List 列出目录直接子项。
func (s *FilesService) List(ctx context.Context, sandboxID string, request protocol.DirectoryListRequest) (protocol.DirectoryListing, error) {
	if err := request.Validate(); err != nil {
		return protocol.DirectoryListing{}, domain.ErrInvalidFilePath
	}
	client, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return protocol.DirectoryListing{}, err
	}
	listing, err := client.DirectoryList(ctx, request)
	if err != nil {
		return protocol.DirectoryListing{}, mapFilesClientError(err)
	}
	if listing.Entries == nil {
		listing.Entries = []protocol.FileStat{}
	}
	return listing, nil
}

// Mkdir 创建目录；created 区分本次新建与已存在。
func (s *FilesService) Mkdir(ctx context.Context, sandboxID string, request protocol.MkdirRequest) (bool, protocol.FileStat, error) {
	if err := request.Validate(); err != nil {
		return false, protocol.FileStat{}, domain.ErrInvalidFilePath
	}
	client, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return false, protocol.FileStat{}, err
	}
	created, stat, err := client.Mkdir(ctx, request)
	if err != nil {
		return false, protocol.FileStat{}, mapFilesClientError(err)
	}
	return created, stat, nil
}

// Upload 流式上传到 workspace 文件。
func (s *FilesService) Upload(ctx context.Context, sandboxID, path string, content io.Reader, overwrite, createParents bool) (bool, protocol.FileStat, error) {
	if err := protocol.ValidateFilePath(path); err != nil || path == "." {
		return false, protocol.FileStat{}, domain.ErrInvalidFilePath
	}
	if content == nil {
		return false, protocol.FileStat{}, domain.ErrInvalidFilePath
	}
	client, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return false, protocol.FileStat{}, err
	}
	replaced, stat, err := client.Upload(ctx, path, content, overwrite, createParents)
	if err != nil {
		return false, protocol.FileStat{}, mapFilesClientError(err)
	}
	return replaced, stat, nil
}

// Download 打开 workspace 普通文件的流式下载。
func (s *FilesService) Download(ctx context.Context, sandboxID, path string) (io.ReadCloser, protocol.FileStat, error) {
	if err := protocol.ValidateFilePath(path); err != nil {
		return nil, protocol.FileStat{}, domain.ErrInvalidFilePath
	}
	client, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return nil, protocol.FileStat{}, err
	}
	reader, stat, err := client.Download(ctx, path)
	if err != nil {
		return nil, protocol.FileStat{}, mapFilesClientError(err)
	}
	if reader == nil {
		return nil, protocol.FileStat{}, domain.ErrRunnerUnhealthy
	}
	return reader, stat, nil
}

// Move 在 workspace 内移动路径。
func (s *FilesService) Move(ctx context.Context, sandboxID string, request protocol.FileMoveRequest) (protocol.FileStat, error) {
	if err := request.Validate(); err != nil {
		return protocol.FileStat{}, domain.ErrInvalidFilePath
	}
	client, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return protocol.FileStat{}, err
	}
	stat, err := client.Move(ctx, request)
	if err != nil {
		return protocol.FileStat{}, mapFilesClientError(err)
	}
	return stat, nil
}

// Delete 幂等删除 workspace 文件或目录。
func (s *FilesService) Delete(ctx context.Context, sandboxID string, request protocol.FileDeleteRequest) error {
	if err := request.Validate(); err != nil {
		return domain.ErrInvalidFilePath
	}
	client, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return err
	}
	if err := client.Delete(ctx, request); err != nil {
		return mapFilesClientError(err)
	}
	return nil
}

// runningClient 校验 sandbox 处于 Running 且删除意图未提交后返回绑定 client。
func (s *FilesService) runningClient(ctx context.Context, sandboxID string) (FilesClient, error) {
	sandbox, err := s.store.Get(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	if sandbox.DesiredState != domain.DesiredRunning || sandbox.ObservedState != domain.StateRunning {
		return nil, domain.ErrSandboxNotRunning
	}
	client, err := s.factory.Client(sandboxID)
	if err != nil || client == nil {
		return nil, domain.ErrRunnerUnhealthy
	}
	return client, nil
}

// mapFilesClientError 把 runner 文件错误映射为稳定 domain 哨兵。
//
// 映射依据 runner 公共错误码；未知码一律按 runner 不健康处理，不把
// 内部错误文本透传给公共响应。
func mapFilesClientError(err error) error {
	for _, known := range []error{
		domain.ErrInvalidFilePath, domain.ErrFileNotFound, domain.ErrFileTypeMismatch,
		domain.ErrFileConflict, domain.ErrFileTooLarge, domain.ErrFilesUnavailable,
		domain.ErrRunnerUnhealthy, domain.ErrRunnerProtocolMismatch,
	} {
		if errors.Is(err, known) {
			return known
		}
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		switch coded.ErrorCode() {
		case string(protocol.ErrorCodeInvalidFilePath):
			return domain.ErrInvalidFilePath
		case string(protocol.ErrorCodeFileNotFound):
			return domain.ErrFileNotFound
		case string(protocol.ErrorCodeFileTypeMismatch):
			return domain.ErrFileTypeMismatch
		case string(protocol.ErrorCodeFileConflict):
			return domain.ErrFileConflict
		case string(protocol.ErrorCodeFileTooLarge):
			return domain.ErrFileTooLarge
		case string(protocol.ErrorCodeFilesUnavailable):
			return domain.ErrFilesUnavailable
		}
	}
	return domain.ErrRunnerUnhealthy
}
