// Package main 提供 Phase 4 文件能力的真实服务端验收程序。
// 它复用公开 SDK 完成生命周期，用公共 HTTP 接口完成 mkdir、upload、stat、
// list、download、move、delete 与 capabilities，并验证二进制内容一致。
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"minisandbox/pkg/protocol"
	"minisandbox/sdk/go"
)

const (
	defaultBaseURL = "http://127.0.0.1:8080"
	defaultImage   = "debian:bookworm-slim"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nFiles API 验收失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n7/7 PASS：Files API 真实验收通过")
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	baseURL := environmentOrDefault("MINISANDBOX_URL", defaultBaseURL)
	image := environmentOrDefault("MINISANDBOX_IMAGE", defaultImage)
	client := sdk.NewClient(baseURL, &http.Client{Timeout: 60 * time.Second})
	httpClient := &http.Client{Timeout: 60 * time.Second}

	fmt.Printf("Files API 验收：server=%s image=%s\n", baseURL, image)
	sandbox, err := client.Create(ctx, sdk.CreateSandboxRequest{Image: image})
	if err != nil {
		return fmt.Errorf("创建 sandbox: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer deleteCancel()
			_ = sandbox.Delete(deleteCtx)
		}
	}()
	if _, err := sandbox.WaitRunning(ctx); err != nil {
		return fmt.Errorf("等待 Running: %w", err)
	}

	capabilities, err := getSandboxCapabilities(httpClient, baseURL, sandbox.ID())
	if err != nil {
		return fmt.Errorf("查询 capabilities: %w", err)
	}
	if !capabilities.Files {
		return errors.New("capabilities 未报告 files=true")
	}
	pass("F01", "capabilities 报告 files 能力", fmt.Sprintf("%+v", capabilities))

	if err := postJSON(httpClient, baseURL, sandbox.ID(), "/directories", protocol.MkdirRequest{
		Path: "src/generated", Parents: true,
	}, http.StatusCreated, nil); err != nil {
		return fmt.Errorf("F02 mkdir: %w", err)
	}
	pass("F02", "mkdir 创建多级目录", "src/generated")

	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		return err
	}
	if err := putFile(httpClient, baseURL, sandbox.ID(), "src/generated/app.bin", payload, false); err != nil {
		return fmt.Errorf("F03 upload: %w", err)
	}
	pass("F03", "上传 1MiB 随机二进制", "src/generated/app.bin")

	var stat protocol.FileStat
	if err := postJSON(httpClient, baseURL, sandbox.ID(), "/files/stat", protocol.FileStatRequest{
		Path: "src/generated/app.bin",
	}, http.StatusOK, &stat); err != nil {
		return fmt.Errorf("F04 stat: %w", err)
	}
	if stat.SizeBytes != int64(len(payload)) || stat.Type != protocol.FileTypeRegular {
		return fmt.Errorf("F04 stat 不匹配: %+v", stat)
	}

	var listing protocol.DirectoryListing
	if err := postJSON(httpClient, baseURL, sandbox.ID(), "/directories/list", protocol.DirectoryListRequest{
		Path: "src",
	}, http.StatusOK, &listing); err != nil {
		return fmt.Errorf("F05 list: %w", err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Path != "src/generated" {
		return fmt.Errorf("F05 list 不匹配: %+v", listing)
	}
	pass("F05", "stat 与 list 返回正确 metadata", stat.Mode)

	downloaded, err := getFile(httpClient, baseURL, sandbox.ID(), "src/generated/app.bin")
	if err != nil {
		return fmt.Errorf("F06 download: %w", err)
	}
	if !bytes.Equal(downloaded, payload) {
		return fmt.Errorf("F06 下载内容不一致: got %d bytes want %d", len(downloaded), len(payload))
	}

	if err := postJSON(httpClient, baseURL, sandbox.ID(), "/directories", protocol.MkdirRequest{
		Path: "bin", Parents: true,
	}, http.StatusCreated, nil); err != nil {
		return fmt.Errorf("F06 mkdir bin: %w", err)
	}
	var moved protocol.FileStat
	if err := postJSON(httpClient, baseURL, sandbox.ID(), "/files/move", protocol.FileMoveRequest{
		Source: "src/generated/app.bin", Destination: "bin/app.bin", Overwrite: false,
	}, http.StatusOK, &moved); err != nil {
		return fmt.Errorf("F06 move: %w", err)
	}
	movedContent, err := getFile(httpClient, baseURL, sandbox.ID(), "bin/app.bin")
	if err != nil {
		return fmt.Errorf("F06 download moved: %w", err)
	}
	if !bytes.Equal(movedContent, payload) {
		return errors.New("F06 移动后内容不一致")
	}
	pass("F06", "下载与移动内容逐字节一致", fmt.Sprintf("%d bytes", len(payload)))

	if err := postJSON(httpClient, baseURL, sandbox.ID(), "/files/delete", protocol.FileDeleteRequest{
		Path: "src", Recursive: true,
	}, http.StatusNoContent, nil); err != nil {
		return fmt.Errorf("F07 delete: %w", err)
	}
	if err := postJSON(httpClient, baseURL, sandbox.ID(), "/files/stat", protocol.FileStatRequest{
		Path: "src/generated/app.bin",
	}, http.StatusNotFound, nil); err != nil {
		return fmt.Errorf("F07 验证删除: %w", err)
	}
	if err := postJSON(httpClient, baseURL, sandbox.ID(), "/files/delete", protocol.FileDeleteRequest{
		Path: "src", Recursive: true,
	}, http.StatusNoContent, nil); err != nil {
		return fmt.Errorf("F07 重复删除: %w", err)
	}
	pass("F07", "递归删除与重复删除幂等", "src")

	deleteCtx, deleteCancel := context.WithTimeout(ctx, 60*time.Second)
	defer deleteCancel()
	if _, err := sandbox.DeleteAndWait(deleteCtx); err != nil {
		return fmt.Errorf("删除 sandbox: %w", err)
	}
	cleanup = false
	return nil
}

func getSandboxCapabilities(client *http.Client, baseURL, sandboxID string) (protocol.Capabilities, error) {
	var capabilities protocol.Capabilities
	response, err := client.Get(baseURL + "/v1/sandboxes/" + url.PathEscape(sandboxID) + "/capabilities")
	if err != nil {
		return capabilities, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return capabilities, unexpectedStatus(response)
	}
	return capabilities, json.NewDecoder(response.Body).Decode(&capabilities)
}

func postJSON(client *http.Client, baseURL, sandboxID, path string, request any, expected int, output any) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	response, err := client.Post(
		baseURL+"/v1/sandboxes/"+url.PathEscape(sandboxID)+path,
		"application/json",
		bytes.NewReader(encoded),
	)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		return unexpectedStatus(response)
	}
	if output != nil {
		return json.NewDecoder(response.Body).Decode(output)
	}
	return nil
}

func putFile(client *http.Client, baseURL, sandboxID, path string, payload []byte, overwrite bool) error {
	query := url.Values{}
	query.Set("path", path)
	query.Set("overwrite", fmt.Sprintf("%t", overwrite))
	request, err := http.NewRequest(
		http.MethodPut,
		baseURL+"/v1/sandboxes/"+url.PathEscape(sandboxID)+"/files/content?"+query.Encode(),
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return unexpectedStatus(response)
	}
	return nil
}

func getFile(client *http.Client, baseURL, sandboxID, path string) ([]byte, error) {
	query := url.Values{}
	query.Set("path", path)
	response, err := client.Get(
		baseURL + "/v1/sandboxes/" + url.PathEscape(sandboxID) + "/files/content?" + query.Encode(),
	)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, unexpectedStatus(response)
	}
	return io.ReadAll(response.Body)
}

func unexpectedStatus(response *http.Response) error {
	dump, err := httputil.DumpResponse(response, true)
	if err != nil {
		return fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	return fmt.Errorf("HTTP status %d: %s", response.StatusCode, string(dump))
}

func environmentOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func pass(id, description, detail string) {
	fmt.Printf("PASS %-3s %s (%s)\n", id, description, detail)
}
