package docker

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"
)

const artifactFileMode int64 = 0o755

var artifactModTime = time.Unix(0, 0).UTC()

// BuildArtifactTar 把 provider 中两个固定产物编码为 Docker Copy API 可用的 tar。
//
// archive entry 只能是 runnerd 和 sandbox-init，调用方不能传目标路径；目标
// `/opt/minisandbox` 由后续 CopyToContainer 原子任务固定。全部内容在内存中
// 生成，不创建宿主机临时文件。调用方必须关闭返回的 reader。
func BuildArtifactTar(provider ArtifactProvider) (io.ReadCloser, error) {
	if provider == nil {
		return nil, errors.New("artifact provider must not be nil")
	}
	artifacts := provider.Artifacts()
	if err := validateArtifactSet(artifacts); err != nil {
		return nil, fmt.Errorf("build artifact tar: %w", err)
	}

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, artifact := range []Artifact{artifacts.Runner, artifacts.Init} {
		header := &tar.Header{
			Name:     artifact.Name,
			Mode:     artifactFileMode,
			Uid:      0,
			Gid:      0,
			Size:     int64(len(artifact.Data)),
			ModTime:  artifactModTime,
			Typeflag: tar.TypeReg,
			Format:   tar.FormatUSTAR,
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("write artifact tar header: %w", err)
		}
		if _, err := writer.Write(artifact.Data); err != nil {
			return nil, fmt.Errorf("write artifact tar content: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close artifact tar: %w", err)
	}
	return io.NopCloser(bytes.NewReader(buffer.Bytes())), nil
}
