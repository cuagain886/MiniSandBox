// Package egresscontrol 定义 sandboxd 与 egressd 之间可重连、请求响应式的 attach
// 控制协议。它只承载一次 bootstrap 和后续只读 inspect，不提供策略热更新、网络
// 管理端点或面向 sandbox 的接口。
package egresscontrol

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egressnft"
	"minisandbox/internal/egresspolicy"
)

const (
	// MaxRequestBytes 是单个控制请求 JSON payload 的最大长度；额外空间只用于
	// envelope，bootstrap 自身仍受 egressnft.MaxBootstrapBytes 限制。
	MaxRequestBytes = egressnft.MaxBootstrapBytes + 1024
	// MaxResponseBytes 是单个 attestation 响应 JSON payload 的最大长度。
	MaxResponseBytes = egressanchor.MaxAttestationBytes
	requestIDBytes   = 16
	nonceBytes       = 32
)

// RequestType 标识控制通道允许的封闭请求集合。
type RequestType string

const (
	// RequestBootstrap 只允许未初始化 sidecar 执行一次 nft 安装与永久降权。
	RequestBootstrap RequestType = "bootstrap"
	// RequestInspect 只允许已初始化 sidecar 回验当前权限并返回内存 attestation。
	RequestInspect RequestType = "inspect"
)

// Request 是完成严格 framing、版本、关联标识和 payload 校验后的控制请求。
type Request struct {
	// Type 决定本请求是唯一 bootstrap 还是只读 inspect。
	Type RequestType
	// RequestID 是本次交互的 128-bit 小写十六进制关联标识。
	RequestID string
	// Nonce 是本次交互的 256-bit 小写十六进制随机挑战，响应必须原样返回。
	Nonce string
	// Bootstrap 仅 RequestBootstrap 非 nil；RequestInspect 必须为空。
	Bootstrap *egressnft.Bootstrap
}

// Response 是 egressd 在完成操作后返回的唯一 attestation 响应。
type Response struct {
	// RequestID 必须与对应请求完全相同。
	RequestID string
	// Nonce 必须与对应请求完全相同，防止旧响应或串线被接受。
	Nonce string
	// Attestation 是 nft 回验及永久降权后保存在 egressd 内存中的证明。
	Attestation egressanchor.Attestation
}

type requestWire struct {
	ProtocolVersion int             `json:"protocol_version"`
	Type            RequestType     `json:"type"`
	RequestID       string          `json:"request_id"`
	Nonce           string          `json:"nonce"`
	Bootstrap       json.RawMessage `json:"bootstrap,omitempty"`
}

type responseWire struct {
	ProtocolVersion int             `json:"protocol_version"`
	Type            string          `json:"type"`
	RequestID       string          `json:"request_id"`
	Nonce           string          `json:"nonce"`
	Attestation     json.RawMessage `json:"attestation"`
}

// NewCorrelation 生成一次 attach 请求使用的 request ID 与随机 nonce；随机源失败时
// 必须中止交互，不能退化为时间戳或可预测值。
func NewCorrelation() (string, string, error) {
	requestID := make([]byte, requestIDBytes)
	nonce := make([]byte, nonceBytes)
	if _, err := io.ReadFull(rand.Reader, requestID); err != nil {
		return "", "", errors.New("generate egress request ID")
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", errors.New("generate egress nonce")
	}
	return hex.EncodeToString(requestID), hex.EncodeToString(nonce), nil
}

// EncodeRequest 把一个合法控制请求编码为 uint32 big-endian 长度加封闭 JSON；
// 返回值可直接写入 Docker attach stdin，连接保持可供后续重新 attach。
func EncodeRequest(request Request) ([]byte, error) {
	if !validCorrelation(request.RequestID, request.Nonce) {
		return nil, errors.New("egress control request correlation is invalid")
	}
	wire := requestWire{
		ProtocolVersion: egresspolicy.CurrentProtocolVersion,
		Type:            request.Type,
		RequestID:       request.RequestID,
		Nonce:           request.Nonce,
	}
	switch request.Type {
	case RequestBootstrap:
		if request.Bootstrap == nil {
			return nil, errors.New("egress bootstrap request is incomplete")
		}
		payload, err := egressnft.MarshalBootstrap(*request.Bootstrap)
		if err != nil {
			return nil, err
		}
		wire.Bootstrap = payload
	case RequestInspect:
		if request.Bootstrap != nil {
			return nil, errors.New("egress inspect request must not include bootstrap")
		}
	default:
		return nil, errors.New("egress control request type is invalid")
	}
	return encodeFrame(wire, MaxRequestBytes)
}

// ReadRequest 从可持续存在的流中严格读取一个请求帧；它只消费当前帧，不要求 EOF，
// 因而同一 egressd stdin 可以顺序处理重新 attach 后的后续 inspect。
func ReadRequest(reader io.Reader) (Request, error) {
	payload, err := readFrame(reader, MaxRequestBytes)
	if err != nil {
		return Request{}, err
	}
	if err := rejectDuplicateJSONFields(payload); err != nil {
		return Request{}, err
	}
	var wire requestWire
	if err := decodeClosedJSON(payload, &wire); err != nil {
		return Request{}, errors.New("decode egress control request")
	}
	if wire.ProtocolVersion != egresspolicy.CurrentProtocolVersion || !validCorrelation(wire.RequestID, wire.Nonce) {
		return Request{}, errors.New("egress control request identity is invalid")
	}
	request := Request{Type: wire.Type, RequestID: wire.RequestID, Nonce: wire.Nonce}
	switch wire.Type {
	case RequestBootstrap:
		if len(wire.Bootstrap) == 0 {
			return Request{}, errors.New("egress bootstrap request is incomplete")
		}
		bootstrap, err := egressnft.ParseBootstrap(wire.Bootstrap)
		if err != nil {
			return Request{}, err
		}
		request.Bootstrap = &bootstrap
	case RequestInspect:
		if len(wire.Bootstrap) != 0 {
			return Request{}, errors.New("egress inspect request must not include bootstrap")
		}
	default:
		return Request{}, errors.New("egress control request type is invalid")
	}
	return request, nil
}

// EncodeResponse 编码唯一 attestation 响应；响应关联字段必须来自当前请求，不能由
// sidecar 自行复用上一轮值。
func EncodeResponse(response Response) ([]byte, error) {
	if !validCorrelation(response.RequestID, response.Nonce) {
		return nil, errors.New("egress control response correlation is invalid")
	}
	attestation, err := json.Marshal(response.Attestation)
	if err != nil {
		return nil, errors.New("encode egress attestation")
	}
	if _, err := egressanchor.ParseAttestation(attestation); err != nil {
		return nil, err
	}
	return encodeFrame(responseWire{
		ProtocolVersion: egresspolicy.CurrentProtocolVersion,
		Type:            "attestation",
		RequestID:       response.RequestID,
		Nonce:           response.Nonce,
		Attestation:     attestation,
	}, MaxResponseBytes)
}

// ReadResponse 从去除 Docker multiplex header 后的 stdout 中读取一个严格响应帧。
func ReadResponse(reader io.Reader) (Response, error) {
	payload, err := readFrame(reader, MaxResponseBytes)
	if err != nil {
		return Response{}, err
	}
	if err := rejectDuplicateJSONFields(payload); err != nil {
		return Response{}, err
	}
	var wire responseWire
	if err := decodeClosedJSON(payload, &wire); err != nil {
		return Response{}, errors.New("decode egress control response")
	}
	if wire.ProtocolVersion != egresspolicy.CurrentProtocolVersion || wire.Type != "attestation" ||
		!validCorrelation(wire.RequestID, wire.Nonce) {
		return Response{}, errors.New("egress control response identity is invalid")
	}
	attestation, err := egressanchor.ParseAttestation(wire.Attestation)
	if err != nil {
		return Response{}, err
	}
	return Response{RequestID: wire.RequestID, Nonce: wire.Nonce, Attestation: attestation}, nil
}

func encodeFrame(value any, maximum int) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil || len(payload) == 0 || len(payload) > maximum {
		return nil, errors.New("encode egress control frame")
	}
	framed := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(framed, uint32(len(payload)))
	copy(framed[4:], payload)
	return framed, nil
}

func readFrame(reader io.Reader, maximum int) ([]byte, error) {
	var length uint32
	if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
		return nil, errors.New("read egress control frame length")
	}
	if length == 0 || uint64(length) > uint64(maximum) {
		return nil, errors.New("egress control frame size is invalid")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, errors.New("read egress control frame payload")
	}
	return payload, nil
}

func decodeClosedJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("egress control JSON has trailing value")
	}
	return nil
}

func rejectDuplicateJSONFields(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := scanJSONValue(decoder); err != nil {
		return errors.New("egress control JSON is invalid")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("egress control object key is invalid")
			}
			if _, exists := seen[key]; exists {
				return errors.New("egress control object field is duplicated")
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("egress control JSON delimiter is invalid")
	}
}

func validCorrelation(requestID, nonce string) bool {
	return validLowerHex(requestID, requestIDBytes*2) && validLowerHex(nonce, nonceBytes*2)
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
