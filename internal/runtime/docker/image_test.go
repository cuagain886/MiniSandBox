package docker

import (
	"errors"
	"strings"
	"testing"

	"minisandbox/internal/domain"
)

// TestParseImageReferenceAcceptsCommonForms 验证普通 name、tag、registry 和 digest。
func TestParseImageReferenceAcceptsCommonForms(t *testing.T) {
	digest := "sha256:" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		input      string
		wantName   string
		wantDigest string
	}{
		{
			input:    "alpine",
			wantName: "docker.io/library/alpine",
		},
		{
			input:    "alpine:3.22",
			wantName: "docker.io/library/alpine:3.22",
		},
		{
			input:    "ghcr.io/example/agent:latest",
			wantName: "ghcr.io/example/agent:latest",
		},
		{
			input:    "localhost:5000/team/agent:v1",
			wantName: "localhost:5000/team/agent:v1",
		},
		{
			input:      "alpine@" + digest,
			wantName:   "docker.io/library/alpine@" + digest,
			wantDigest: digest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseImageReference(tt.input)
			if err != nil {
				t.Fatalf("parse image reference: %v", err)
			}
			if got.Name != tt.wantName || got.Digest != tt.wantDigest {
				t.Fatalf("parsed: got %#v, want name=%q digest=%q", got, tt.wantName, tt.wantDigest)
			}
		})
	}
}

// TestParseImageReferenceRejectsUnsafeForms 验证空值、控制字符和超长输入。
func TestParseImageReferenceRejectsUnsafeForms(t *testing.T) {
	const credentialCanary = "registry-password-canary"
	tests := []string{
		"",
		" alpine:latest",
		"alpine:latest ",
		"alpine:\nlatest",
		"alpine:\x7flatest",
		"https://registry.example/alpine:latest",
		"registry.example/user:" + credentialCanary + "@image:latest",
		strings.Repeat("a", domain.MaxImageReferenceLength+1),
	}

	for _, input := range tests {
		_, err := ParseImageReference(input)
		if err == nil {
			t.Fatalf("invalid image reference accepted: length=%d", len(input))
		}
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("error classification: %T %v", err, err)
		}
		if strings.Contains(err.Error(), credentialCanary) ||
			strings.Contains(err.Error(), input) && len(input) > 0 {
			t.Fatalf("error leaked image reference: %v", err)
		}
	}
}
