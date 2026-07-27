package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

// maxCreateSandboxBodyBytes 是创建请求允许的最大 JSON body 字节数。
//
// Phase 1 只有一个最长 512 字节的 image 字段，16 KiB 足以容纳合法请求，
// 同时在 JSON 解码前限制恶意输入占用的内存和读取时间。
const maxCreateSandboxBodyBytes int64 = 16 << 10

func registerLifecycleRoutes(mux *http.ServeMux, service LifecycleService) {
	//创建沙盒
	if service == nil {
		mux.HandleFunc("POST /v1/sandboxes", notImplemented("sandbox creation"))
	} else {
		mux.HandleFunc("POST /v1/sandboxes", createSandboxHandler(service))
	}
	//查询沙盒
	mux.HandleFunc("GET /v1/sandboxes/{sandbox_id}", notImplemented("sandbox lookup"))
	//删除沙盒
	mux.HandleFunc("DELETE /v1/sandboxes/{sandbox_id}", notImplemented("sandbox deletion"))
	//续期沙盒
	mux.HandleFunc("POST /v1/sandboxes/{sandbox_id}/renew", notImplemented("sandbox renewal"))
}

// createSandboxHandler 严格解析创建请求并提交 application 创建意图。
//
// handler 返回 202 只表示期望状态已经持久化，不等待 Docker 创建容器。
func createSandboxHandler(service LifecycleService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request protocol.CreateSandboxRequest
		if err := decodeCreateSandboxRequest(w, r, &request); err != nil {
			writeError(w, r, err)
			return
		}

		sandbox, err := service.Create(
			r.Context(),
			application.CreateSandbox{Image: request.Image},
		)
		if err != nil {
			writeError(w, r, err)
			return
		}
		response, err := mapSandboxResponse(sandbox)
		if err != nil {
			writeError(w, r, err)
			return
		}

		w.Header().Set(
			"Location",
			"/v1/sandboxes/"+url.PathEscape(response.ID),
		)
		writeJSON(w, http.StatusAccepted, response)
	}
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
