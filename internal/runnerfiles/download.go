package runnerfiles

import (
	"io"
	"os"

	"minisandbox/pkg/protocol"
)

// Download 是一次下载的句柄：安全打开的文件 fd 包装为流式 reader。
type Download struct {
	// Stat 是下载开始时的文件 metadata 快照。
	Stat protocol.FileStat
	// Reader 流式读取文件内容；调用方必须调用 Close 释放 fd。
	Reader io.ReadCloser
}

// sectionCloser 把 SectionReader 与其底层文件组合成 ReadCloser。
type sectionCloser struct {
	section *io.SectionReader
	file    *os.File
}

func (s *sectionCloser) Read(p []byte) (int, error) { return s.section.Read(p) }
func (s *sectionCloser) Close() error               { return s.file.Close() }

// ensure io.ReadCloser 装配满足接口检查。
var _ io.ReadCloser = (*sectionCloser)(nil)
