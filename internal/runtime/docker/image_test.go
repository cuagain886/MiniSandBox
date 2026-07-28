package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobyimage "github.com/moby/moby/api/types/image"
	mobyclient "github.com/moby/moby/client"
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

// TestEnsureImageUsesExistingImage 验证本地命中时不触发 pull。
func TestEnsureImageUsesExistingImage(t *testing.T) {
	const imageID = "sha256:existing"
	inspectCalls := 0
	pullCalls := 0
	engine := &fakeEngine{
		imageInspectFunc: func(
			_ context.Context,
			image string,
			_ ...mobyclient.ImageInspectOption,
		) (mobyclient.ImageInspectResult, error) {
			inspectCalls++
			if image != "docker.io/library/alpine:3.22" {
				t.Fatalf("inspect image: %q", image)
			}
			return mobyclient.ImageInspectResult{
				InspectResponse: imageInspectResponse(imageID),
			}, nil
		},
		imagePullFunc: func(
			context.Context,
			string,
			mobyclient.ImagePullOptions,
		) (mobyclient.ImagePullResponse, error) {
			pullCalls++
			return nil, nil
		},
	}

	result, err := ensureImage(
		context.Background(),
		engine,
		"alpine:3.22",
		time.Second,
	)
	if err != nil {
		t.Fatalf("ensure existing image: %v", err)
	}
	if result.ID != imageID || inspectCalls != 1 || pullCalls != 0 {
		t.Fatalf(
			"result/calls: ID=%q inspect=%d pull=%d",
			result.ID,
			inspectCalls,
			pullCalls,
		)
	}
}

// TestEnsureImagePullsMissingImage 验证 not-found 后完整消费、关闭并重新 inspect。
func TestEnsureImagePullsMissingImage(t *testing.T) {
	const imageID = "sha256:pulled"
	inspectCalls := 0
	response := newFakePullResponse(nil)
	engine := &fakeEngine{
		imageInspectFunc: func(
			_ context.Context,
			_ string,
			_ ...mobyclient.ImageInspectOption,
		) (mobyclient.ImageInspectResult, error) {
			inspectCalls++
			if inspectCalls == 1 {
				return mobyclient.ImageInspectResult{}, cerrdefs.ErrNotFound
			}
			return mobyclient.ImageInspectResult{
				InspectResponse: imageInspectResponse(imageID),
			}, nil
		},
		imagePullFunc: func(
			_ context.Context,
			image string,
			options mobyclient.ImagePullOptions,
		) (mobyclient.ImagePullResponse, error) {
			if image != "docker.io/library/alpine:3.22" {
				t.Fatalf("pull image: %q", image)
			}
			if options.RegistryAuth != "" || options.PrivilegeFunc != nil {
				t.Fatalf("unexpected pull credentials/options: %#v", options)
			}
			return response, nil
		},
	}

	result, err := ensureImage(
		context.Background(),
		engine,
		"alpine:3.22",
		time.Second,
	)
	if err != nil {
		t.Fatalf("ensure missing image: %v", err)
	}
	if result.ID != imageID || inspectCalls != 2 {
		t.Fatalf("result/inspect calls: ID=%q calls=%d", result.ID, inspectCalls)
	}
	if response.waitCalls != 1 || response.closeCalls != 1 {
		t.Fatalf(
			"pull stream lifecycle: wait=%d close=%d",
			response.waitCalls,
			response.closeCalls,
		)
	}
}

// TestEnsureImageClassifiesPullFailures 验证 pull 与 stream 错误的固定分类和关闭。
func TestEnsureImageClassifiesPullFailures(t *testing.T) {
	cause := errors.New("secret registry response")
	tests := []struct {
		name string
		pull func() (mobyclient.ImagePullResponse, error)
	}{
		{
			name: "pull request",
			pull: func() (mobyclient.ImagePullResponse, error) {
				return nil, cause
			},
		},
		{
			name: "pull stream",
			pull: func() (mobyclient.ImagePullResponse, error) {
				return newFakePullResponse(cause), nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var response *fakePullResponse
			engine := &fakeEngine{
				imageInspectFunc: func(
					context.Context,
					string,
					...mobyclient.ImageInspectOption,
				) (mobyclient.ImageInspectResult, error) {
					return mobyclient.ImageInspectResult{}, cerrdefs.ErrNotFound
				},
				imagePullFunc: func(
					context.Context,
					string,
					mobyclient.ImagePullOptions,
				) (mobyclient.ImagePullResponse, error) {
					stream, err := tt.pull()
					if typed, ok := stream.(*fakePullResponse); ok {
						response = typed
					}
					return stream, err
				},
			}

			_, err := ensureImage(
				context.Background(),
				engine,
				"alpine:3.22",
				time.Second,
			)
			var pullError *ImagePullFailedError
			if !errors.As(err, &pullError) || !errors.Is(err, cause) {
				t.Fatalf("pull error classification: %T %v", err, err)
			}
			if strings.Contains(err.Error(), cause.Error()) {
				t.Fatalf("pull error leaked cause: %v", err)
			}
			if response != nil && response.closeCalls != 1 {
				t.Fatalf("failed pull stream close calls: %d", response.closeCalls)
			}
		})
	}
}

// TestEnsureImageTimeoutIsRuntimeUnavailable 验证独立超时取消 Engine 操作。
func TestEnsureImageTimeoutIsRuntimeUnavailable(t *testing.T) {
	engine := &fakeEngine{
		imageInspectFunc: func(
			ctx context.Context,
			_ string,
			_ ...mobyclient.ImageInspectOption,
		) (mobyclient.ImageInspectResult, error) {
			<-ctx.Done()
			return mobyclient.ImageInspectResult{}, ctx.Err()
		},
	}

	_, err := ensureImage(
		context.Background(),
		engine,
		"alpine:3.22",
		10*time.Millisecond,
	)
	var unavailable *RuntimeUnavailableError
	if !errors.As(err, &unavailable) ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout classification: %T %v", err, err)
	}
}

// imageInspectResponse 构造只设置 ID 的 SDK inspect payload。
func imageInspectResponse(id string) mobyimage.InspectResponse {
	return mobyimage.InspectResponse{ID: id}
}
