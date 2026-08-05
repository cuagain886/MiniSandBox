package runner

import (
	"errors"
	"io"
	"sync"
)

const (
	pipeReadBufferBytes = 32 * 1024
	pipeChunkQueueSize  = 16
)

// OutputStream 标识原始输出来自 stdout 或 stderr。
type OutputStream string

const (
	// OutputStreamStdout 表示用户进程标准输出。
	OutputStreamStdout OutputStream = "stdout"
	// OutputStreamStderr 表示用户进程标准错误。
	OutputStreamStderr OutputStream = "stderr"
)

// RawOutputChunk 是尚未分配 sequence、timestamp 或 Base64 编码的独立输出副本。
type RawOutputChunk struct {
	// Stream 标识 chunk 的来源 pipe。
	Stream OutputStream
	// Data 是与 reader 重用缓冲区解耦的原始字节；调用方取得后拥有该切片。
	Data []byte
}

// PipeReadResult 描述单个 stream reader 的完成结果；EOF 映射为 nil Err。
type PipeReadResult struct {
	// Stream 标识完成或失败的 pipe。
	Stream OutputStream
	// Err 仅在非 EOF 读取错误时非空。
	Err error
}

// PipeReaders 暴露有界原始 chunk 队列和每个 stream 恰好一个完成结果。
type PipeReaders struct {
	// Chunks 在两个 reader 都结束后关闭；容量固定，避免无限制积压输出。
	Chunks <-chan RawOutputChunk
	// Results 为 stdout/stderr 各产生一个结果，并在两者结束后关闭。
	Results <-chan PipeReadResult
}

// StartPipeReaders 为 stdout/stderr 各启动一个持续 reader，并接管两个读取端的关闭责任。
func StartPipeReaders(stdout, stderr io.ReadCloser) (*PipeReaders, error) {
	if stdout == nil || stderr == nil {
		return nil, errors.New("stdout and stderr pipes are required")
	}
	chunks := make(chan RawOutputChunk, pipeChunkQueueSize)
	results := make(chan PipeReadResult, 2)
	var readers sync.WaitGroup
	readers.Add(2)
	go drainPipe(OutputStreamStdout, stdout, chunks, results, &readers)
	go drainPipe(OutputStreamStderr, stderr, chunks, results, &readers)
	go func() {
		readers.Wait()
		close(chunks)
		close(results)
	}()
	return &PipeReaders{Chunks: chunks, Results: results}, nil
}

func drainPipe(
	stream OutputStream,
	reader io.ReadCloser,
	chunks chan<- RawOutputChunk,
	results chan<- PipeReadResult,
	wait *sync.WaitGroup,
) {
	defer wait.Done()
	defer reader.Close()
	buffer := make([]byte, pipeReadBufferBytes)
	defer clear(buffer)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			data := make([]byte, count)
			copy(data, buffer[:count])
			chunks <- RawOutputChunk{Stream: stream, Data: data}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			results <- PipeReadResult{Stream: stream, Err: err}
			return
		}
		if count == 0 {
			continue
		}
	}
}
