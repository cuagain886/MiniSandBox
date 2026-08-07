package runner

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

// TokenAuth 使用常量时间比较校验内部 Bearer token，并拒绝缺失、错误或重复的认证头。
// 返回的 401 响应使用同一固定文本，不向调用方区分凭据失败原因。
func TokenAuth(expected string, next http.Handler) (http.Handler, error) {
	if strings.TrimSpace(expected) == "" {
		return nil, errors.New("runner bearer token is required")
	}
	if next == nil {
		return nil, errors.New("runner authenticated handler is required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			unauthorized(w)
			return
		}
		actual := values[0]
		want := "Bearer " + expected
		actualDigest := sha256.Sum256([]byte(actual))
		wantDigest := sha256.Sum256([]byte(want))
		if subtle.ConstantTimeCompare(actualDigest[:], wantDigest[:]) != 1 {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

func unauthorized(w http.ResponseWriter) {
	writeRunnerError(w, http.StatusUnauthorized, "UNAUTHORIZED", "runner authentication failed", false)
}
