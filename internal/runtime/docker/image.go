package docker

// ImageReference 保存镜像名称及解析后的不可变 digest。
type ImageReference struct {
	Name   string
	Digest string
}
