//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// submitSandboxDelete 通过公共 API 幂等提交终止意图并返回 HTTP 状态。
func submitSandboxDelete(t *testing.T, baseURL, sandboxID string) int {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodDelete,
		baseURL+"/v1/sandboxes/"+sandboxID,
		nil,
	)
	if err != nil {
		t.Fatalf("build sandbox delete request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted &&
		response.StatusCode != http.StatusNoContent {
		t.Fatalf(
			"delete status: got %d, want 202 or 204",
			response.StatusCode,
		)
	}
	return response.StatusCode
}
