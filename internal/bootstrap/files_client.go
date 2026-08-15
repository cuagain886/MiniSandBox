package bootstrap

import (
	"context"
	"errors"
	"io"
	"net/http"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/internal/runnerclient"
	"minisandbox/pkg/protocol"
)

// applicationFilesFactory 把固定 Unix Socket runner factory 适配为文件
// application 端口；调用方不能提供 URL、路径或 token。
type applicationFilesFactory struct {
	factory *runnerclient.Factory
}

func (f applicationFilesFactory) Client(sandboxID string) (application.FilesClient, error) {
	client, err := f.factory.Client(sandboxID)
	if err != nil {
		return nil, err
	}
	return applicationFilesClient{client: client}, nil
}

// applicationFilesClient 把 runnerclient 文件方法适配为 application 端口。
type applicationFilesClient struct {
	client *runnerclient.Client
}

func (c applicationFilesClient) Capabilities(ctx context.Context) (protocol.Capabilities, error) {
	capabilities, err := c.client.Capabilities(ctx)
	return capabilities, mapRunnerFilesError(err)
}

func (c applicationFilesClient) FileStat(ctx context.Context, request protocol.FileStatRequest) (protocol.FileStat, error) {
	stat, err := c.client.FileStat(ctx, request)
	return stat, mapRunnerFilesError(err)
}

func (c applicationFilesClient) DirectoryList(ctx context.Context, request protocol.DirectoryListRequest) (protocol.DirectoryListing, error) {
	listing, err := c.client.DirectoryList(ctx, request)
	return listing, mapRunnerFilesError(err)
}

func (c applicationFilesClient) Mkdir(ctx context.Context, request protocol.MkdirRequest) (bool, protocol.FileStat, error) {
	created, stat, err := c.client.Mkdir(ctx, request)
	return created, stat, mapRunnerFilesError(err)
}

func (c applicationFilesClient) Upload(ctx context.Context, path string, content io.Reader, overwrite, createParents bool) (bool, protocol.FileStat, error) {
	replaced, stat, err := c.client.Upload(ctx, path, content, overwrite, createParents)
	return replaced, stat, mapRunnerFilesError(err)
}

func (c applicationFilesClient) Download(ctx context.Context, path string) (io.ReadCloser, protocol.FileStat, error) {
	download, err := c.client.Download(ctx, path)
	if err != nil {
		return nil, protocol.FileStat{}, mapRunnerFilesError(err)
	}
	return download.Reader, download.Stat, nil
}

func (c applicationFilesClient) Move(ctx context.Context, request protocol.FileMoveRequest) (protocol.FileStat, error) {
	stat, err := c.client.Move(ctx, request)
	return stat, mapRunnerFilesError(err)
}

func (c applicationFilesClient) Delete(ctx context.Context, request protocol.FileDeleteRequest) error {
	return mapRunnerFilesError(c.client.Delete(ctx, request))
}

// mapRunnerFilesError 把 runner 文件错误映射为稳定 domain 哨兵。
func mapRunnerFilesError(err error) error {
	if err == nil {
		return nil
	}
	var status *runnerclient.StatusError
	if !errors.As(err, &status) {
		return err
	}
	switch status.Code {
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
	switch status.StatusCode {
	case http.StatusBadRequest:
		return domain.ErrInvalidFilePath
	case http.StatusNotFound:
		return domain.ErrFileNotFound
	case http.StatusRequestEntityTooLarge:
		return domain.ErrFileTooLarge
	default:
		return domain.ErrRunnerUnhealthy
	}
}
