package runner

import (
	"crypto/subtle"
	"net/http"
)

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
