// Package main 提供 Phase 4 PTY 与端口代理的真实服务端验收程序。
// 它复用公开 SDK 完成生命周期，通过公共 WebSocket 与端口代理接口验证
// 交互终端与 loopback HTTP 访问。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"

	"minisandbox/pkg/protocol"
	"minisandbox/sdk/go"
)

const (
	defaultBaseURL = "http://127.0.0.1:8080"
	defaultImage   = "debian:bookworm-slim"
	proxyPort      = 18080
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nAgent 能力验收失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n5/5 PASS：PTY 与端口代理真实验收通过")
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	baseURL := environmentOrDefault("MINISANDBOX_URL", defaultBaseURL)
	image := environmentOrDefault("MINISANDBOX_IMAGE", defaultImage)
	client := sdk.NewClient(baseURL, &http.Client{Timeout: 60 * time.Second})

	fmt.Printf("Agent 能力验收：server=%s image=%s\n", baseURL, image)
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

	capabilities, err := getCapabilities(ctx, baseURL, sandbox.ID())
	if err != nil {
		return fmt.Errorf("查询 capabilities: %w", err)
	}
	if !capabilities.PTY || !capabilities.HTTPPortProxy {
		return fmt.Errorf("capabilities 未全部启用: %+v", capabilities)
	}
	pass("A01", "capabilities 报告 PTY 与端口代理", fmt.Sprintf("%+v", capabilities))

	if err := verifyPTY(ctx, baseURL, sandbox.ID()); err != nil {
		return fmt.Errorf("A02/A03 PTY: %w", err)
	}

	if err := verifyFilesWorkflow(ctx, baseURL, sandbox); err != nil {
		return fmt.Errorf("A04 文件工作流: %w", err)
	}

	serverBinary := os.Getenv("MINISANDBOX_TEST_SERVER")
	if serverBinary == "" {
		return errors.New("需要 MINISANDBOX_TEST_SERVER 指向已构建的 tests/agent/testserver 二进制")
	}
	if err := verifyPortProxy(ctx, baseURL, sandbox, serverBinary); err != nil {
		return fmt.Errorf("A05/A06 端口代理: %w", err)
	}

	deleteCtx, deleteCancel := context.WithTimeout(ctx, 60*time.Second)
	defer deleteCancel()
	if _, err := sandbox.DeleteAndWait(deleteCtx); err != nil {
		return fmt.Errorf("删除 sandbox: %w", err)
	}
	cleanup = false
	return nil
}

// verifyFilesWorkflow 通过 SDK 完成 upload → run → download 闭环。
func verifyFilesWorkflow(ctx context.Context, baseURL string, sandbox *sdk.Sandbox) error {
	script := []byte("#!/bin/sh\necho agent-full-ok > artifact.txt\n")
	query := url.Values{}
	query.Set("path", "src/build.sh")
	query.Set("create_parents", "true")
	uploadURL := baseURL + "/v1/sandboxes/" + url.PathEscape(sandbox.ID()) + "/files/content?" + query.Encode()
	uploadRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(script))
	if err != nil {
		return err
	}
	uploadRequest.Header.Set("Content-Type", "application/octet-stream")
	uploadResponse, err := http.DefaultClient.Do(uploadRequest)
	if err != nil {
		return err
	}
	_ = uploadResponse.Body.Close()
	if uploadResponse.StatusCode != http.StatusCreated {
		return fmt.Errorf("上传 build.sh: HTTP %d", uploadResponse.StatusCode)
	}

	result, err := sandbox.Run(ctx, sdk.ExecuteRequest{
		Argv: []string{"/bin/sh", "/workspace/src/build.sh"}, Timeout: 30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("执行 build.sh: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("build.sh 退出码 %d", result.ExitCode)
	}

	downloadURL := baseURL + "/v1/sandboxes/" + url.PathEscape(sandbox.ID()) + "/files/content?path=artifact.txt"
	downloadRequest, _ := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	downloadResponse, err := http.DefaultClient.Do(downloadRequest)
	if err != nil {
		return err
	}
	artifact := readBody(downloadResponse)
	if artifact != "agent-full-ok\n" {
		return fmt.Errorf("artifact 内容不匹配: %q", artifact)
	}
	pass("A04", "上传脚本、执行并下载产物", strings.TrimSpace(artifact))
	return nil
}

// verifyPTY 打开 shell、发送输入、读取输出、resize 并正常退出。
func verifyPTY(ctx context.Context, baseURL, sandboxID string) error {
	connection, _, err := websocket.Dial(ctx, baseURL+"/v1/sandboxes/"+url.PathEscape(sandboxID)+"/pty", &websocket.DialOptions{
		Subprotocols: []string{protocol.PTYSubprotocol},
	})
	if err != nil {
		return fmt.Errorf("dial PTY: %w", err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "done")
	if connection.Subprotocol() != protocol.PTYSubprotocol {
		return errors.New("PTY 子协议不匹配")
	}

	start, _ := json.Marshal(protocol.PTYStartRequest{
		Type: "start", Argv: []string{"/bin/sh"}, Cols: 80, Rows: 24,
	})
	if err := connection.Write(ctx, websocket.MessageText, start); err != nil {
		return fmt.Errorf("发送 start: %w", err)
	}

	terminal := make(chan protocol.PTYServerEvent, 1)
	output := make(chan string, 64)
	readDone := make(chan error, 1)
	go func() {
		for {
			messageType, reader, readErr := connection.Reader(ctx)
			if readErr != nil {
				readDone <- readErr
				return
			}
			payload, _ := readAll(reader)
			if messageType == websocket.MessageText {
				var event protocol.PTYServerEvent
				if json.Unmarshal(payload, &event) == nil && event.Type == protocol.PTYServerEventTerminal {
					terminal <- event
				}
				continue
			}
			select {
			case output <- string(payload):
			default:
			}
		}
	}()

	var accumulated strings.Builder
	deadline := time.After(20 * time.Second)
	sawEcho := false
	sawResize := false
	// shell 启动后立即注入命令；stdin 由终端行缓冲，无需等待提示符。
	if err := connection.Write(ctx, websocket.MessageBinary, []byte("echo pty-ok\n")); err != nil {
		return fmt.Errorf("发送 echo: %w", err)
	}
	for !(sawEcho && sawResize) {
		select {
		case chunk := <-output:
			accumulated.WriteString(chunk)
			if strings.Contains(accumulated.String(), "pty-ok") && !sawEcho {
				sawEcho = true
				resize, _ := json.Marshal(protocol.PTYResizeRequest{Type: "resize", Cols: 120, Rows: 40})
				if err := connection.Write(ctx, websocket.MessageText, resize); err != nil {
					return fmt.Errorf("发送 resize: %w", err)
				}
				if err := connection.Write(ctx, websocket.MessageBinary, []byte("stty size\n")); err != nil {
					return fmt.Errorf("发送 stty: %w", err)
				}
			}
			if strings.Contains(accumulated.String(), "40 120") {
				sawResize = true
			}
		case event := <-terminal:
			return fmt.Errorf("提前终态: %+v 输出=%q", event, accumulated.String())
		case <-deadline:
			return fmt.Errorf("等待 PTY 输出超时: %q", accumulated.String())
		}
	}
	detail := strings.TrimSpace(accumulated.String())
	if len(detail) > 60 {
		detail = detail[:60]
	}
	pass("A02", "PTY 打开 shell 并回显输入与 resize", detail)

	if err := connection.Write(ctx, websocket.MessageBinary, []byte("exit\n")); err != nil {
		return fmt.Errorf("发送 exit: %w", err)
	}
	select {
	case event := <-terminal:
		if event.Type != protocol.PTYServerEventTerminal || event.ExitCode == nil || *event.ExitCode != 0 {
			return fmt.Errorf("非预期终态: %+v", event)
		}
	case <-time.After(10 * time.Second):
		return errors.New("等待 PTY 终态超时")
	}
	pass("A03", "PTY 正常退出并返回 terminal 事件", "exit=0")
	return nil
}

// verifyPortProxy 上传并启动 sandbox 内 HTTP 服务，经代理完成 GET/POST。
func verifyPortProxy(ctx context.Context, baseURL string, sandbox *sdk.Sandbox, serverBinary string) error {
	content, err := os.ReadFile(serverBinary)
	if err != nil {
		return err
	}
	uploadURL := baseURL + "/v1/sandboxes/" + url.PathEscape(sandbox.ID()) + "/files/content?path=bin/testserver&create_parents=true"
	uploadRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(content))
	if err != nil {
		return err
	}
	uploadRequest.Header.Set("Content-Type", "application/octet-stream")
	uploadResponse, err := http.DefaultClient.Do(uploadRequest)
	if err != nil {
		return err
	}
	_ = uploadResponse.Body.Close()
	if uploadResponse.StatusCode != http.StatusCreated {
		return fmt.Errorf("上传 testserver: HTTP %d", uploadResponse.StatusCode)
	}

	if _, err := sandbox.Run(ctx, sdk.ExecuteRequest{
		Argv: []string{"chmod", "+x", "bin/testserver"}, Timeout: 10 * time.Second,
	}); err != nil {
		var exitErr *sdk.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("chmod: %w", err)
		}
	}
	if _, err := sandbox.StartExecution(ctx, sdk.ExecuteRequest{
		Argv: []string{"/workspace/bin/testserver"}, Timeout: 10 * time.Minute,
	}); err != nil {
		return fmt.Errorf("启动 testserver: %w", err)
	}

	proxyBase := baseURL + "/v1/sandboxes/" + url.PathEscape(sandbox.ID()) + "/ports/18080/http"
	var helloResponse *http.Response
	for attempt := 0; attempt < 30; attempt++ {
		helloResponse, err = http.Get(proxyBase + "/hello")
		if err == nil && helloResponse.StatusCode == http.StatusOK {
			break
		}
		if helloResponse != nil {
			_ = helloResponse.Body.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if helloResponse == nil || helloResponse.StatusCode != http.StatusOK {
		return errors.New("代理 GET 未就绪")
	}
	helloBody := readBody(helloResponse)
	if helloBody != "hello-from-sandbox" {
		return fmt.Errorf("GET 内容不匹配: %q", helloBody)
	}

	echoResponse, err := http.Post(proxyBase+"/echo", "text/plain", strings.NewReader("proxy-post"))
	if err != nil {
		return err
	}
	echoBody := readBody(echoResponse)
	if echoBody != "proxy-post!" {
		return fmt.Errorf("POST 内容不匹配: %q", echoBody)
	}
	if echoResponse.Header.Get("Authorization") != "" || echoResponse.Header.Get("X-MiniSandbox-Proxied") != "" {
		return errors.New("响应泄漏内部头")
	}
	pass("A05", "端口代理 GET /hello", helloBody)
	pass("A06", "端口代理 POST /echo", echoBody)
	return nil
}

func getCapabilities(ctx context.Context, baseURL, sandboxID string) (protocol.Capabilities, error) {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet,
		baseURL+"/v1/sandboxes/"+url.PathEscape(sandboxID)+"/capabilities", nil,
	)
	if err != nil {
		return protocol.Capabilities{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return protocol.Capabilities{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return protocol.Capabilities{}, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	var capabilities protocol.Capabilities
	return capabilities, json.NewDecoder(response.Body).Decode(&capabilities)
}

func readBody(response *http.Response) string {
	defer response.Body.Close()
	buffer := new(bytes.Buffer)
	_, _ = buffer.ReadFrom(response.Body)
	return buffer.String()
}

func readAll(reader io.Reader) ([]byte, error) {
	buffer := new(bytes.Buffer)
	chunk := make([]byte, 32*1024)
	for {
		read, err := reader.Read(chunk)
		buffer.Write(chunk[:read])
		if err != nil {
			return buffer.Bytes(), nil
		}
	}
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
