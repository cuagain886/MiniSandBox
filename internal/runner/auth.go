package runner

import (
	"crypto/subtle"
	"net/http"
)

// TokenAuth 使用常量时间比较校验内部 Bearer token。
//
// 空 token 只用于显式关闭第二层鉴权；生产环境仍应同时依赖 Unix Socket 权限。
func TokenAuth(expected string, next http.Handler) http.Handler {
	if expected == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actual := r.Header.Get("Authorization")
		want := "Bearer " + expected
		if subtle.ConstantTimeCompare([]byte(actual), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
