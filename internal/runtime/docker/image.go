package docker

import (
	"strings"

	"github.com/distribution/reference"
	"minisandbox/internal/domain"
)

var errInvalidImageReference = &invalidImageReferenceError{}

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
