package runner

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
)

// TestPipeReadersKeepStreamsSeparateAndPreserveBinary 验证空输出、二进制、非 UTF-8 和交错写入不混流。
func TestPipeReadersKeepStreamsSeparateAndPreserveBinary(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	readers, err := StartPipeReaders(stdoutReader, stderrReader)
	if err != nil {
		t.Fatalf("start pipe readers: %v", err)
	}
	stdoutWant := []byte{0x00, 0xff, 'a', '\n'}
	stderrWant := []byte{0xfe, 'b', 0x00}
	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		_, _ = stdoutWriter.Write(stdoutWant[:2])
		_, _ = stdoutWriter.Write(stdoutWant[2:])
		_ = stdoutWriter.Close()
	}()
	go func() {
		defer writers.Done()
		_, _ = stderrWriter.Write(stderrWant)
		_ = stderrWriter.Close()
	}()
	got := collectPipeOutput(readers)
	writers.Wait()
	if !bytes.Equal(got[OutputStreamStdout], stdoutWant) || !bytes.Equal(got[OutputStreamStderr], stderrWant) {
		t.Fatalf("output: stdout=%v stderr=%v", got[OutputStreamStdout], got[OutputStreamStderr])
	}
	assertSuccessfulPipeResults(t, readers.Results)
}

// TestPipeReadersDrainOutputLargerThanBuffer 验证大于单次 buffer 的输出被完整分块排空。
func TestPipeReadersDrainOutputLargerThanBuffer(t *testing.T) {
	stdout := bytes.Repeat([]byte{0x7f, 0x00, 0x80}, pipeReadBufferBytes)
	readers, err := StartPipeReaders(
		io.NopCloser(bytes.NewReader(stdout)),
		io.NopCloser(bytes.NewReader(nil)),
	)
	if err != nil {
		t.Fatalf("start pipe readers: %v", err)
	}
	got := collectPipeOutput(readers)
	if !bytes.Equal(got[OutputStreamStdout], stdout) || len(got[OutputStreamStderr]) != 0 {
		t.Fatalf("large output lengths: stdout=%d stderr=%d", len(got[OutputStreamStdout]), len(got[OutputStreamStderr]))
	}
	assertSuccessfulPipeResults(t, readers.Results)
}

type scriptedRead struct {
	steps []scriptedReadStep
	index int
}

type scriptedReadStep struct {
	data []byte
	err  error
}

func (r *scriptedRead) Read(buffer []byte) (int, error) {
	if r.index >= len(r.steps) {
		return 0, io.EOF
	}
	step := r.steps[r.index]
	r.index++
	return copy(buffer, step.data), step.err
}

func (*scriptedRead) Close() error { return nil }

// TestPipeReadersCopyReusedBufferAndReportReadError 验证早期 chunk 不被后续 Read 覆盖，且非 EOF 错误独立上报。
func TestPipeReadersCopyReusedBufferAndReportReadError(t *testing.T) {
	wantErr := errors.New("read failed")
	stdout := &scriptedRead{steps: []scriptedReadStep{
		{data: []byte("first")},
		{data: []byte("later"), err: wantErr},
	}}
	readers, err := StartPipeReaders(stdout, io.NopCloser(bytes.NewReader(nil)))
	if err != nil {
		t.Fatalf("start pipe readers: %v", err)
	}
	var stdoutChunks [][]byte
	for chunk := range readers.Chunks {
		if chunk.Stream == OutputStreamStdout {
			stdoutChunks = append(stdoutChunks, chunk.Data)
		}
	}
	if !reflect.DeepEqual(stdoutChunks, [][]byte{[]byte("first"), []byte("later")}) {
		t.Fatalf("stdout chunks changed: %q", stdoutChunks)
	}
	results := collectPipeResults(readers.Results)
	if !errors.Is(results[OutputStreamStdout], wantErr) || results[OutputStreamStderr] != nil {
		t.Fatalf("read results: %#v", results)
	}
}

// TestPipeReadersUseBoundedChunkQueue 验证内部输出队列容量固定且有界。
func TestPipeReadersUseBoundedChunkQueue(t *testing.T) {
	readers, err := StartPipeReaders(io.NopCloser(bytes.NewReader(nil)), io.NopCloser(bytes.NewReader(nil)))
	if err != nil {
		t.Fatalf("start pipe readers: %v", err)
	}
	if capacity := cap(readers.Chunks); capacity != pipeChunkQueueSize {
		t.Fatalf("chunk queue capacity: got %d, want %d", capacity, pipeChunkQueueSize)
	}
	for range readers.Chunks {
	}
	assertSuccessfulPipeResults(t, readers.Results)
}

func collectPipeOutput(readers *PipeReaders) map[OutputStream][]byte {
	output := make(map[OutputStream][]byte)
	for chunk := range readers.Chunks {
		output[chunk.Stream] = append(output[chunk.Stream], chunk.Data...)
	}
	return output
}

func collectPipeResults(results <-chan PipeReadResult) map[OutputStream]error {
	got := make(map[OutputStream]error)
	for result := range results {
		got[result.Stream] = result.Err
	}
	return got
}

func assertSuccessfulPipeResults(t *testing.T, results <-chan PipeReadResult) {
	t.Helper()
	got := collectPipeResults(results)
	if len(got) != 2 || got[OutputStreamStdout] != nil || got[OutputStreamStderr] != nil {
		t.Fatalf("pipe results: %#v", got)
	}
}
