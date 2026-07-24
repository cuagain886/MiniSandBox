package docker

// InjectedArtifact 描述复制到容器文件系统的 runner 或 init 二进制。
type InjectedArtifact struct {
	Name string
	Data []byte
	Mode int64
}
