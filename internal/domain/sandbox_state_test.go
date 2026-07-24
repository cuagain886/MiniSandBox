package domain

import "testing"

// TestSandboxStateTerminal 验证终态判断不会把运行态误判为终态。
func TestSandboxStateTerminal(t *testing.T) {
	if !StateTerminated.Terminal() {
		t.Fatal("Terminated must be terminal")
	}
	if StateRunning.Terminal() {
		t.Fatal("Running must not be terminal")
	}
}
