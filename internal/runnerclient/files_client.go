package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"minisandbox/pkg/protocol"
)

// filesUploadBufferLimit 是上传路径上传给 runner 的单次读块大小。
const filesUploadBufferLimit = 32 * 1024

// Capabilities 查询 runner 实际提供的功能集合。
func (c *Client) Capabilities(ctx context.Context) (protocol.Capabilities, error) {
	var capabilities protocol.Capabilities
	err := c.doJSON(ctx, http.MethodGet, "/v1/capabilities", nil, http.StatusOK, &capabilities)
	return capabilities, err
}

// FileStat 查询一个 workspace 路径的 metadata。
func (c *Client) FileStat(ctx context.Context, request protocol.FileStatRequest) (protocol.FileStat, error) {
	var stat protocol.FileStat
	err := c.doJSON(ctx, http.MethodPost, "/v1/files/stat", request, http.StatusOK, &stat)
	return stat, err
}

// DirectoryList 列出一个 workspace 目录的直接子项。
func (c *Client) DirectoryList(ctx context.Context, request protocol.DirectoryListRequest) (protocol.DirectoryListing, error) {
	var listing protocol.DirectoryListing
	err := c.doJSON(ctx, http.MethodPost, "/v1/directories/list", request, http.StatusOK, &listing)
	return listing, err
}

// Mkdir 创建目录；created 区分本次新建与已存在两种成功。
func (c *Client) Mkdir(ctx context.Context, request protocol.MkdirRequest) (bool, protocol.FileStat, error) {
	var stat protocol.FileStat
	status, err := c.doJSONAccepted(ctx, http.MethodPost, "/v1/directories", nil, request,
		[]int{http.StatusOK, http.StatusCreated}, &stat)
	if err != nil {
		return false, protocol.FileStat{}, err
	}
	return status == http.StatusCreated, stat, nil
}

// Upload 把 content 流式上传到一个 workspace 文件。
//
// replaced 表示覆盖了已存在文件。上传体不经过内存整体缓冲；调用方负责
// content 的生命周期，重试需要自行重新提供 reader。
func (c *Client) Upload(ctx context.Context, path string, content io.Reader, overwrite, createParents bool) (bool, protocol.FileStat, error) {
	query := url.Values{}
	query.Set("path", path)
	query.Set("overwrite", strconv.FormatBool(overwrite))
	query.Set("create_parents", strconv.FormatBool(createParents))
	request, err := c.newRequest(ctx, http.MethodPut, "/v1/files/content?"+query.Encode(), content)
	if err != nil {
		return false, protocol.FileStat{}, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Accept", "application/json")
	response, err := c.do(request)
	if err != nil {
		return false, protocol.FileStat{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return false, protocol.FileStat{}, decodeStatusError(response)
	}
	var stat protocol.FileStat
	if err := decodeStrictJSON(response.Body, &stat); err != nil {
		return false, protocol.FileStat{}, err
	}
	return response.StatusCode == http.StatusOK, stat, nil
}

// FileDownload 是一次下载的流式句柄；调用方必须关闭 Reader。
type FileDownload struct {
	// Stat 是下载开始时的文件 metadata。
	Stat protocol.FileStat
	// Reader 流式读取文件内容。
	Reader io.ReadCloser
}

// Download 打开一个 workspace 普通文件的流式下载。
func (c *Client) Download(ctx context.Context, path string) (*FileDownload, error) {
	query := url.Values{}
	query.Set("path", path)
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/files/content?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	response, err := c.do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		return nil, decodeStatusError(response)
	}
	stat := protocol.FileStat{Path: path}
	if header := response.Header.Get("Content-Length"); header != "" {
		if size, parseErr := strconv.ParseInt(header, 10, 64); parseErr == nil {
			stat.SizeBytes = size
		}
	}
	return &FileDownload{Stat: stat, Reader: response.Body}, nil
}

// Move 在 workspace 内移动路径。
func (c *Client) Move(ctx context.Context, request protocol.FileMoveRequest) (protocol.FileStat, error) {
	var stat protocol.FileStat
	err := c.doJSON(ctx, http.MethodPost, "/v1/files/move", request, http.StatusOK, &stat)
	return stat, err
}

// Delete 删除文件或目录；目标不存在同样视为成功。
func (c *Client) Delete(ctx context.Context, request protocol.FileDeleteRequest) error {
	return c.doJSONNoContent(ctx, http.MethodPost, "/v1/files/delete", request, http.StatusNoContent)
}

// doJSONAccepted 在多个可接受状态码下解码 JSON 响应，返回实际状态码。
func (c *Client) doJSONAccepted(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	input any,
	accepted []int,
	output any,
) (int, error) {
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return 0, errors.New("encode runner request failed")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return 0, err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	for _, status := range accepted {
		if response.StatusCode == status {
			return response.StatusCode, decodeStrictJSON(response.Body, output)
		}
	}
	return response.StatusCode, decodeStatusError(response)
}

// doJSONNoContent 执行期望无响应体的 JSON 请求。
func (c *Client) doJSONNoContent(ctx context.Context, method, path string, input any, expectedStatus int) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return errors.New("encode runner request failed")
	}
	request, err := c.newRequest(ctx, method, path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		return decodeStatusError(response)
	}
	return nil
}

// decodeStrictJSON 以严格语义解码受限响应体。
func decodeStrictJSON(reader io.Reader, output any) error {
	limited, err := io.ReadAll(io.LimitReader(reader, maxRunnerResponseBytes+1))
	if err != nil || int64(len(limited)) > maxRunnerResponseBytes {
		return &ProtocolMismatchError{}
	}
	decoder := json.NewDecoder(bytes.NewReader(limited))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return &ProtocolMismatchError{}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return &ProtocolMismatchError{}
	}
	return nil
}
