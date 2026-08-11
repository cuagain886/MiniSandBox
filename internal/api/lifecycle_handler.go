package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

// maxCreateSandboxBodyBytes 是创建请求允许的最大 JSON body 字节数。
//
// 当前创建请求只有最长 512 字节的 image 和一个布尔网络对象，16 KiB 足以
// 容纳合法请求，同时在 JSON 解码前限制恶意输入占用的内存和读取时间。
const maxCreateSandboxBodyBytes int64 = 16 << 10

// maxRenewSandboxBodyBytes 限制仅含 expires_at 的续期 JSON 请求。
const maxRenewSandboxBodyBytes int64 = 4 << 10

func registerLifecycleRoutes(mux *http.ServeMux, service LifecycleService) {
	//创建沙盒
	if service == nil {
		mux.HandleFunc("POST /v1/sandboxes", notImplemented("sandbox creation"))
	} else {
		mux.HandleFunc("POST /v1/sandboxes", createSandboxHandler(service))
	}
	//查询沙盒
	if service == nil {
		mux.HandleFunc(
			"GET /v1/sandboxes/{sandbox_id}",
			notImplemented("sandbox lookup"),
		)
	} else {
		mux.HandleFunc("GET /v1/sandboxes/{sandbox_id}", getSandboxHandler(service))
	}
	//删除沙盒
	if service == nil {
		mux.HandleFunc(
			"DELETE /v1/sandboxes/{sandbox_id}",
			notImplemented("sandbox deletion"),
		)
	} else {
		mux.HandleFunc(
			"DELETE /v1/sandboxes/{sandbox_id}",
			deleteSandboxHandler(service),
		)
	}
	//续期沙盒
	if service == nil {
		mux.HandleFunc("POST /v1/sandboxes/{sandbox_id}/renew", notImplemented("sandbox renewal"))
	} else {
		mux.HandleFunc("POST /v1/sandboxes/{sandbox_id}/renew", renewSandboxHandler(service))
	}
}

// renewSandboxHandler 严格解析绝对 expiry，并把全部租约决策委托给 application。
func renewSandboxHandler(service LifecycleService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("sandbox_id")
		if !validSandboxID(id) || r.URL.RawQuery != "" {
			writeError(w, r, domain.ErrInvalidExpiration)
			return
		}
		request, err := decodeRenewSandboxRequest(w, r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		sandbox, err := service.Renew(r.Context(), application.RenewSandbox{
			SandboxID: id, ExpiresAt: request.ExpiresAt,
		})
		if err != nil {
			writeError(w, r, err)
			return
		}
		response, err := mapSandboxResponse(sandbox)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	}
}

func decodeRenewSandboxRequest(w http.ResponseWriter, r *http.Request) (protocol.RenewSandboxRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return protocol.RenewSandboxRequest{}, domain.ErrInvalidExpiration
	}
	reader := http.MaxBytesReader(w, r.Body, maxRenewSandboxBodyBytes)
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var request protocol.RenewSandboxRequest
	if err := decoder.Decode(&request); err != nil || request.ExpiresAt.IsZero() {
		return protocol.RenewSandboxRequest{}, domain.ErrInvalidExpiration
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return protocol.RenewSandboxRequest{}, domain.ErrInvalidExpiration
	}
	return request, nil
}

// deleteSandboxHandler 校验公共 ID 并幂等提交 sandbox 终止意图。
//
// 202 表示删除仍需 reconciler 收敛；只有 Store 已观测到 Terminated 时才返回
// 204。本 handler 不直接调用 runtime，也不等待 Docker 资源清理。
func deleteSandboxHandler(service LifecycleService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("sandbox_id")
		if !validSandboxID(id) {
			writeError(w, r, domain.ErrInvalid)
			return
		}

		sandbox, err := service.Delete(
			r.Context(),
			application.DeleteSandbox{SandboxID: id},
		)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if sandbox.ObservedState == domain.StateTerminated {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

// getSandboxHandler 校验公共 ID 并返回最近一次持久化的 sandbox 状态。
func getSandboxHandler(service LifecycleService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("sandbox_id")
		if !validSandboxID(id) {
			writeError(w, r, domain.ErrInvalid)
			return
		}

		sandbox, err := service.Get(r.Context(), id)
		if err != nil {
			writeError(w, r, err)
			return
		}
		response, err := mapSandboxResponse(sandbox)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// validSandboxID 只接受控制面生成的规范小写 UUID v4。
//
// 在进入 application 和后续路径/资源命名逻辑前拒绝分隔符、路径穿越和
// 非规范别名，保证同一个 sandbox 只有一种公共 ID 表示。
func validSandboxID(id string) bool {
	if len(id) != 36 ||
		id[8] != '-' ||
		id[13] != '-' ||
		id[18] != '-' ||
		id[23] != '-' ||
		id[14] != '4' {
		return false
	}
	for index := range id {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !isLowerHex(id[index]) {
			return false
		}
	}
	switch id[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}

// isLowerHex 判断单字节是否属于规范 UUID 使用的小写十六进制集合。
func isLowerHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f'
}

// createSandboxHandler 严格解析创建请求并提交 application 创建意图。
//
// handler 返回 202 只表示期望状态已经持久化，不等待 Docker 创建容器。
func createSandboxHandler(service LifecycleService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idempotency, err := mapIdempotencyKey(r.Header)
		if err != nil {
			writeError(w, r, err)
			return
		}
		var request protocol.CreateSandboxRequest
		if err := decodeCreateSandboxRequest(w, r, &request); err != nil {
			writeError(w, r, err)
			return
		}

		outbound := false
		if request.Network != nil {
			outbound = request.Network.Outbound
		}
		outcome, err := service.CreateAccepted(
			r.Context(),
			application.CreateSandbox{
				Image:       request.Image,
				Outbound:    outbound,
				TTLSeconds:  request.TTLSeconds,
				Idempotency: idempotency,
			},
		)
		if err != nil {
			writeError(w, r, err)
			return
		}
		_ = writeCreateOutcome(w, outcome)
	}
}

// mapIdempotencyKey 把可选 HTTP header 转为当前单租户 scope 的安全值对象。
//
// Header.Values 保留重复 field-line；单个值中的逗号也拒绝，避免代理合并后把
// 多个 key 误解释成一个。错误只返回统一 ErrInvalid，绝不回显 raw value。
func mapIdempotencyKey(header http.Header) (*application.IdempotencyKey, error) {
	values := header.Values("Idempotency-Key")
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return nil, domain.ErrInvalid
	}
	key, err := application.NewLocalIdempotencyKey(values[0])
	if err != nil {
		return nil, domain.ErrInvalid
	}
	return &key, nil
}

// writeCreateOutcome 写出 Store 已提交后的精确首次或 replay 响应。
//
// 写失败只作为 transport 结果返回，调用方不得据此回滚 Store 或重复 Wake；
// middleware 已为本次请求独立设置 X-Request-ID，它不属于持久化 body。
func writeCreateOutcome(w http.ResponseWriter, outcome application.IdempotentCreateOutcome) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", outcome.Location)
	w.WriteHeader(outcome.StatusCode)
	written, err := w.Write(outcome.Body)
	if err != nil {
		return err
	}
	if written != len(outcome.Body) {
		return io.ErrShortWrite
	}
	return nil
}

// decodeCreateSandboxRequest 限制 body 并只接受一个字段集合固定的 JSON 对象。
func decodeCreateSandboxRequest(
	w http.ResponseWriter,
	r *http.Request,
	request *protocol.CreateSandboxRequest,
) error {
	reader := http.MaxBytesReader(w, r.Body, maxCreateSandboxBodyBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return domain.ErrInvalid
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return domain.ErrInvalid
	}
	return nil
}
