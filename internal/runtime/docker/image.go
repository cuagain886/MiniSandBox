package docker

import (
	"context"
	"errors"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	mobyclient "github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

var errInvalidImageReference = &invalidImageReferenceError{}

// ImagePullFailedError 表示 registry 拉取或响应流处理失败。
//
// Error 使用固定文案，底层 cause 仅通过 errors.Is/As 提供内部分类，避免
// registry 返回内容或 credential 被上送到公共状态 message。
type ImagePullFailedError struct {
	cause error
}

// Error 返回不包含镜像引用和 registry 响应的固定文案。
func (e *ImagePullFailedError) Error() string {
	return "sandbox image pull failed"
}

// Unwrap 返回内部 cause，供后续 P1-060 统一分类。
func (e *ImagePullFailedError) Unwrap() error {
	return e.cause
}

// FailureReason 返回稳定的 image pull 生命周期 reason。
func (*ImagePullFailedError) FailureReason() string {
	return runtimeport.FailureReasonImagePullFailed
}

// ArtifactInvalidError 表示镜像平台与 Phase 1 嵌入产物不兼容。
//
// Phase 1 只提供 linux/amd64 runner 和 init；平台缺失也不能猜测，否则可能
// 在启动容器后才以 exec format error 失败。
type ArtifactInvalidError struct {
	cause error
}

// Error 返回不包含镜像内部元数据的固定安全文案。
func (*ArtifactInvalidError) Error() string {
	return "sandbox runtime artifacts are invalid"
}

// Unwrap 返回内部 artifact 校验 cause。
func (e *ArtifactInvalidError) Unwrap() error {
	return e.cause
}

// FailureReason 返回稳定的 artifact invalid 生命周期 reason。
func (*ArtifactInvalidError) FailureReason() string {
	return runtimeport.FailureReasonArtifactInvalid
}

// ImageReference 保存已经通过基础语法校验的规范镜像引用。
type ImageReference struct {
	// Name 是规范化后的完整 name/tag/digest，可直接传给 Docker Engine。
	Name string
	// Digest 在引用固定 digest 时保存 `algorithm:encoded`，普通 name/tag 为空。
	Digest string
}

// ParseImageReference 校验并规范化 Phase 1 接受的 image name、tag 或 digest。
//
// 本函数不实现 registry allowlist，也不访问 Docker。所有失败返回固定安全
// 错误且满足 errors.Is(err, domain.ErrInvalid)，绝不回显可能含凭据的原始值。
func ParseImageReference(value string) (ImageReference, error) {
	if value == "" ||
		len(value) > domain.MaxImageReferenceLength ||
		value != strings.TrimSpace(value) ||
		containsControlCharacter(value) {
		return ImageReference{}, errInvalidImageReference
	}

	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return ImageReference{}, errInvalidImageReference
	}
	result := ImageReference{Name: named.String()}
	if digested, ok := named.(reference.Digested); ok {
		result.Digest = digested.Digest().String()
	}
	return result, nil
}

// containsControlCharacter 拒绝不可见 ASCII 控制字符和 DEL。
func containsControlCharacter(value string) bool {
	for index := range value {
		if value[index] < 0x20 || value[index] == 0x7f {
			return true
		}
	}
	return false
}

// ensureImage 在独立超时内执行 inspect-or-pull，并返回最终 inspect 结果。
//
// 只有明确 not-found 才允许进入 pull；pull stream 必须通过 Wait 完整消费并
// 显式 Close。非 not-found Engine 故障分类为 runtime unavailable。
func ensureImage(
	ctx context.Context,
	engine Engine,
	value string,
	createTimeout time.Duration,
	pullLimiter runtimeport.Limiter,
) (mobyclient.ImageInspectResult, error) {
	return ensureImageForPlatform(
		ctx,
		engine,
		value,
		ocispec.Platform{OS: "linux", Architecture: "amd64"},
		createTimeout,
		pullLimiter,
	)
}

// ensureImageForPlatform 按明确平台执行镜像 inspect-or-pull，并校验最终镜像元数据。
func ensureImageForPlatform(
	ctx context.Context,
	engine Engine,
	value string,
	platform ocispec.Platform,
	createTimeout time.Duration,
	pullLimiter runtimeport.Limiter,
) (mobyclient.ImageInspectResult, error) {
	imageReference, err := ParseImageReference(value)
	if err != nil {
		return mobyclient.ImageInspectResult{}, err
	}
	if createTimeout <= 0 {
		return mobyclient.ImageInspectResult{}, errInvalidImageReference
	}
	operationContext, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	inspection, err := engine.ImageInspect(operationContext, imageReference.Name)
	if err == nil {
		if err := validateImagePlatformFor(inspection, platform); err != nil {
			return mobyclient.ImageInspectResult{}, err
		}
		return inspection, nil
	}
	if !cerrdefs.IsNotFound(err) {
		return mobyclient.ImageInspectResult{}, runtimeUnavailable(err)
	}

	if err := pullMissingImage(operationContext, engine, imageReference.Name, platform, pullLimiter); err != nil {
		return mobyclient.ImageInspectResult{}, err
	}

	inspection, err = engine.ImageInspect(operationContext, imageReference.Name)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return mobyclient.ImageInspectResult{}, &ImagePullFailedError{
				cause: err,
			}
		}
		return mobyclient.ImageInspectResult{}, runtimeUnavailable(err)
	}
	if err := validateImagePlatformFor(inspection, platform); err != nil {
		return mobyclient.ImageInspectResult{}, err
	}
	return inspection, nil
}

// pullMissingImage 只在已确认本地缺失后占用全局 slot，并覆盖完整响应流生命周期。
func pullMissingImage(ctx context.Context, engine Engine, image string, platform ocispec.Platform, limiter runtimeport.Limiter) error {
	var release func()
	if limiter != nil {
		var err error
		release, err = limiter.Acquire(ctx)
		if err != nil {
			return classifyPullError(ctx, err)
		}
		defer release()
	}
	stream, err := engine.ImagePull(ctx, image, mobyclient.ImagePullOptions{Platforms: []ocispec.Platform{platform}})
	if err != nil {
		return classifyPullError(ctx, err)
	}
	if stream == nil {
		return &ImagePullFailedError{cause: errors.New("docker returned a nil pull stream")}
	}
	waitErr := stream.Wait(ctx)
	closeErr := stream.Close()
	if waitErr != nil {
		return classifyPullError(ctx, waitErr)
	}
	if closeErr != nil {
		return &ImagePullFailedError{cause: closeErr}
	}
	return nil
}

// validateImagePlatform 强制镜像与唯一的 linux/amd64 artifact 集合匹配。
func validateImagePlatform(inspection mobyclient.ImageInspectResult) error {
	return validateImagePlatformFor(inspection, ocispec.Platform{OS: "linux", Architecture: "amd64"})
}

// validateImagePlatformFor 校验 inspect 结果与请求拉取的平台一致。
func validateImagePlatformFor(inspection mobyclient.ImageInspectResult, platform ocispec.Platform) error {
	if inspection.Os != platform.OS || inspection.Architecture != platform.Architecture {
		return &ArtifactInvalidError{}
	}
	return nil
}

// classifyPullError 区分 operation context 故障和 registry/pull 失败。
func classifyPullError(ctx context.Context, cause error) error {
	if err := ctx.Err(); err != nil {
		return runtimeUnavailable(err)
	}
	return &ImagePullFailedError{cause: cause}
}

// runtimeUnavailable 创建保留 cause 的固定安全依赖错误。
func runtimeUnavailable(cause error) error {
	return &RuntimeUnavailableError{cause: cause}
}

// invalidImageReferenceError 提供固定安全文案和领域错误分类。
type invalidImageReferenceError struct{}

// Error 返回不包含原始镜像引用的固定文案。
func (*invalidImageReferenceError) Error() string {
	return "image reference is invalid"
}

// Unwrap 使 HTTP 层可统一映射为 INVALID_REQUEST。
func (*invalidImageReferenceError) Unwrap() error {
	return domain.ErrInvalid
}
