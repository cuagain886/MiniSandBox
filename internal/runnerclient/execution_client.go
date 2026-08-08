package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"minisandbox/pkg/protocol"
)

const maxRunnerResponseBytes int64 = 1 << 20

// EventStream 是一个已建立、只能消费一次的前台 execution typed event stream。
type EventStream struct {
	body io.ReadCloser
	once sync.Once
}

// CancelDisposition 描述 runner DELETE 响应观察到的幂等状态。
type CancelDisposition string

const (
	// CancelAccepted 表示活动 execution 的取消已接受。
	CancelAccepted CancelDisposition = "accepted"
	// CancelAlreadyTerminal 表示 execution 已终态，取消是成功 no-op。
	CancelAlreadyTerminal CancelDisposition = "already_terminal"
)

// Consume 严格解码全部事件直到唯一 terminal；返回后 response body 已关闭。
func (s *EventStream) Consume(consume func(protocol.ExecutionEvent) error) error {
	if s == nil || s.body == nil {
		return &ProtocolMismatchError{}
	}
	called := false
	var result error
	s.once.Do(func() {
		called = true
		result = DecodeSSE(s.body, consume)
	})
	if !called {
		return errors.New("runner event stream was already consumed")
	}
	return result
}

// Close 提前关闭尚未消费完的前台 stream，并通过请求 context 触发 runner 取消。
func (s *EventStream) Close() error {
	if s == nil || s.body == nil {
		return nil
	}
	return s.body.Close()
}

// ExecuteForeground 创建前台 execution，并返回严格 typed SSE stream。
func (c *Client) ExecuteForeground(ctx context.Context, request protocol.ExecuteRequest) (*EventStream, error) {
	if err := c.ensureHealthy(ctx); err != nil {
		return nil, err
	}
	request.Background = false
	body, err := json.Marshal(request)
	if err != nil {
		return nil, errors.New("encode runner execution request failed")
	}
	httpRequest, err := c.newRequest(ctx, http.MethodPost, "/v1/executions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	response, err := c.do(httpRequest)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, &StatusError{StatusCode: response.StatusCode}
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		response.Body.Close()
		return nil, &ProtocolMismatchError{}
	}
	return &EventStream{body: response.Body}, nil
}

// ExecuteBackground 创建后台 execution，并返回最小描述符。
func (c *Client) ExecuteBackground(ctx context.Context, request protocol.ExecuteRequest) (protocol.ExecutionDescriptor, error) {
	if err := c.ensureHealthy(ctx); err != nil {
		return protocol.ExecutionDescriptor{}, err
	}
	request.Background = true
	var result protocol.ExecutionDescriptor
	err := c.doJSON(ctx, http.MethodPost, "/v1/executions", request, http.StatusAccepted, &result)
	if err != nil {
		return protocol.ExecutionDescriptor{}, err
	}
	if result.ExecutionID == "" || !validExecutionState(result.State) {
		return protocol.ExecutionDescriptor{}, &ProtocolMismatchError{}
	}
	return result, nil
}

// Status 查询当前 sandbox 内一个 execution 的状态。
func (c *Client) Status(ctx context.Context, executionID string) (protocol.ExecutionStatus, error) {
	if err := c.ensureHealthy(ctx); err != nil {
		return protocol.ExecutionStatus{}, err
	}
	var result protocol.ExecutionStatus
	err := c.doJSON(ctx, http.MethodGet, executionPath(executionID), nil, http.StatusOK, &result)
	if err != nil {
		return protocol.ExecutionStatus{}, err
	}
	if result.ExecutionID != executionID || !validExecutionState(result.State) {
		return protocol.ExecutionStatus{}, &ProtocolMismatchError{}
	}
	if result.TerminalEvent != nil && (result.TerminalEvent.ExecutionID != executionID || !result.TerminalEvent.Terminal() || result.TerminalEvent.Validate() != nil) {
		return protocol.ExecutionStatus{}, &ProtocolMismatchError{}
	}
	return result, nil
}

// Cancel 幂等请求取消当前 sandbox 内一个 execution。
func (c *Client) Cancel(ctx context.Context, executionID string) (CancelDisposition, error) {
	if err := c.ensureHealthy(ctx); err != nil {
		return "", err
	}
	request, err := c.newRequest(ctx, http.MethodDelete, executionPath(executionID), nil)
	if err != nil {
		return "", err
	}
	response, err := c.do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusNoContent {
		return "", &StatusError{StatusCode: response.StatusCode}
	}
	limited, err := io.ReadAll(io.LimitReader(response.Body, 1))
	if err != nil || len(limited) != 0 {
		return "", &ProtocolMismatchError{}
	}
	if response.StatusCode == http.StatusNoContent {
		return CancelAlreadyTerminal, nil
	}
	return CancelAccepted, nil
}

// Logs 从 cursor 之后读取一页后台 execution 事件。
func (c *Client) Logs(ctx context.Context, executionID string, cursor uint64) (protocol.ExecutionLogPage, error) {
	if err := c.ensureHealthy(ctx); err != nil {
		return protocol.ExecutionLogPage{}, err
	}
	query := url.Values{"cursor": []string{strconv.FormatUint(cursor, 10)}}
	var result protocol.ExecutionLogPage
	err := c.doJSON(ctx, http.MethodGet, executionPath(executionID)+"/logs?"+query.Encode(), nil, http.StatusOK, &result)
	if err != nil {
		return protocol.ExecutionLogPage{}, err
	}
	if result.Events == nil || result.NextCursor < cursor {
		return protocol.ExecutionLogPage{}, &ProtocolMismatchError{}
	}
	expected := cursor + 1
	for index, event := range result.Events {
		if event.ExecutionID != executionID || event.Sequence != expected || event.Validate() != nil || index < len(result.Events)-1 && event.Terminal() {
			return protocol.ExecutionLogPage{}, &ProtocolMismatchError{}
		}
		expected++
	}
	if len(result.Events) == 0 && result.NextCursor != cursor || len(result.Events) > 0 && result.NextCursor != result.Events[len(result.Events)-1].Sequence {
		return protocol.ExecutionLogPage{}, &ProtocolMismatchError{}
	}
	return result, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, input any, expectedStatus int, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return errors.New("encode runner request failed")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		return &StatusError{StatusCode: response.StatusCode}
	}
	limited, err := io.ReadAll(io.LimitReader(response.Body, maxRunnerResponseBytes+1))
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

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c == nil || c.httpClient == nil || c.authorization == nil || ctx == nil {
		return nil, errors.New("runner client is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	return request, nil
}

func (c *Client) ensureHealthy(ctx context.Context) error {
	if c == nil || c.expectedProtocolVersion == 0 {
		return nil
	}
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	now := c.now()
	if !c.healthCheckedAt.IsZero() && now.Sub(c.healthCheckedAt) >= 0 && now.Sub(c.healthCheckedAt) < c.healthCacheTTL {
		return nil
	}
	_, err := c.Health(ctx, c.expectedProtocolVersion)
	if err == nil {
		c.healthCheckedAt = now
		return nil
	}
	var mismatch *ProtocolMismatchError
	if errors.As(err, &mismatch) {
		return err
	}
	var status *StatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusUnauthorized {
		return &AuthenticationError{}
	}
	return &ConnectionError{cause: err}
}

func executionPath(executionID string) string {
	return "/v1/executions/" + url.PathEscape(executionID)
}

func validExecutionState(state protocol.ExecutionState) bool {
	switch state {
	case protocol.ExecutionStatePending, protocol.ExecutionStateRunning, protocol.ExecutionStateExited, protocol.ExecutionStateFailed, protocol.ExecutionStateCancelled, protocol.ExecutionStateTimedOut:
		return true
	default:
		return false
	}
}
