// Package adminauth 加载本地受限 secret file，并为只读管理端点提供恒时 Bearer 鉴权。
// 本包不记录 token、文件内容或路径，也不实现 RBAC、轮换和远程身份。
package adminauth

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

// Token 保存启动时一次性解码的固定 32 字节凭据。
type Token struct{ value [32]byte }

// LoadToken 校验并读取 owner 为当前 euid、权限不宽于 0600 的 regular non-symlink 文件。
func LoadToken(path string) (*Token, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("admin token file path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("load admin token file")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("admin token file is not restricted")
	}
	if runtime.GOOS != "windows" {
		owner, ok := fileOwnerID(info)
		if !ok || owner != uint64(os.Geteuid()) {
			return nil, errors.New("admin token file owner is invalid")
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("read admin token file")
	}
	if len(content) > 0 && content[len(content)-1] == '\n' {
		content = content[:len(content)-1]
	}
	if len(content) == 0 || strings.ContainsAny(string(content), " \t\r\n=") {
		return nil, errors.New("admin token encoding is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(string(content))
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("admin token encoding is invalid")
	}
	token := &Token{}
	copy(token.value[:], decoded)
	for index := range decoded {
		decoded[index] = 0
	}
	return token, nil
}

func fileOwnerID(info os.FileInfo) (uint64, bool) {
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return 0, false
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, false
	}
	uid := value.FieldByName("Uid")
	if !uid.IsValid() || !uid.CanUint() {
		return 0, false
	}
	return uid.Uint(), true
}

// Authenticate 验证请求恰好包含一个 Bearer header，候选必须解码为固定 32 字节后再恒时比较。
func (token *Token) Authenticate(request *http.Request) bool {
	if token == nil || request == nil {
		return false
	}
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	parts := strings.Split(values[0], " ")
	if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" || strings.Contains(parts[1], "=") {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(decoded) != len(token.value) {
		return false
	}
	matched := subtle.ConstantTimeCompare(decoded, token.value[:]) == 1
	for index := range decoded {
		decoded[index] = 0
	}
	return matched
}

// Middleware 对 missing、malformed 与错误 token 返回完全相同的 401 响应。
func (token *Token) Middleware(next http.Handler) http.Handler {
	if next == nil {
		panic(fmt.Errorf("admin auth handler is nil"))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !token.Authenticate(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
