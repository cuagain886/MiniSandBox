package domain

import "testing"

func TestSandboxStateTerminal(t *testing.T) {
	if !StateTerminated.Terminal() {
		t.Fatal("Terminated must be terminal")
	}
	if StateRunning.Terminal() {
		t.Fatal("Running must not be terminal")
	}
}
