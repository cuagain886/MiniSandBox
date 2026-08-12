package adminauth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func validTokenText() string {
	return base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}
func writeToken(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin.token")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadTokenValidatesEncodingModeAndSymlink 验证严格 token 文件契约。
func TestLoadTokenValidatesEncodingModeAndSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode and symlink contract")
	}
	if _, err := LoadToken(writeToken(t, validTokenText()+"\n", 0600)); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"empty": "", "short": base64.RawURLEncoding.EncodeToString([]byte("short")), "padding": validTokenText() + "=", "space": validTokenText() + " "} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadToken(writeToken(t, content, 0600)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	if _, err := LoadToken(writeToken(t, validTokenText(), 0644)); err == nil {
		t.Fatal("wide mode accepted")
	}
	target := writeToken(t, validTokenText(), 0600)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(link); err == nil {
		t.Fatal("symlink accepted")
	}
}

// TestTokenMiddlewareAcceptsOneBearerAndUnifiesFailures 验证正确、错误、缺失与重复 header。
func TestTokenMiddlewareAcceptsOneBearerAndUnifiesFailures(t *testing.T) {
	token := &Token{}
	copy(token.value[:], []byte("0123456789abcdef0123456789abcdef"))
	handler := token.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer "+validTokenText())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid status: %d", response.Code)
	}
	var failureBody string
	for _, headers := range [][]string{nil, {"Bearer bad"}, {"Basic " + validTokenText()}, {"Bearer " + validTokenText(), "Bearer " + validTokenText()}} {
		request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
		for _, value := range headers {
			request.Header.Add("Authorization", value)
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), validTokenText()) {
			t.Fatalf("failure: %d %q", response.Code, response.Body.String())
		}
		if failureBody == "" {
			failureBody = response.Body.String()
		} else if response.Body.String() != failureBody {
			t.Fatalf("failure responses differ")
		}
	}
}
