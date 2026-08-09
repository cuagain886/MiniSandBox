package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"minisandbox/pkg/protocol"
)

var (
	// ErrBackgroundLogNotFound 表示 execution 日志不存在或目标不是受控 regular file。
	ErrBackgroundLogNotFound = errors.New("background execution log not found")
	// ErrBackgroundLogCorrupt 表示日志含超大行、非法 JSON、错序事件或 terminal 后数据。
	ErrBackgroundLogCorrupt = errors.New("background execution log is corrupt")
	// ErrInvalidLogCursor 表示 cursor 不是当前日志中已存在的 sequence。
	ErrInvalidLogCursor = errors.New("invalid background log cursor")
)

// BackgroundLogReader 从固定 execution 日志生成有界、可重复的 sequence cursor 页面。
type BackgroundLogReader struct {
	directory string
	trustedFD bool
	maxEvents int
	maxBytes  int64
}

// NewBackgroundLogReaderFromDirectory 使用降权前打开的固定目录句柄创建 reader。
// 句柄生命周期由调用方管理；该入口不接受普通路径，因此不会放宽 symlink 校验。
func NewBackgroundLogReaderFromDirectory(directory *os.File, maxEvents int, maxBytes int64) (*BackgroundLogReader, error) {
	if directory == nil || maxEvents <= 0 || maxBytes < 64 {
		return nil, errors.New("background log reader limits or directory are invalid")
	}
	return &BackgroundLogReader{
		directory: "/proc/self/fd/" + strconv.FormatUint(uint64(directory.Fd()), 10),
		trustedFD: true,
		maxEvents: maxEvents,
		maxBytes:  maxBytes,
	}, nil
}

// NewBackgroundLogReader 创建同时限制事件数量和最终 JSON response bytes 的 reader。
func NewBackgroundLogReader(directory string, maxEvents int, maxBytes int64) (*BackgroundLogReader, error) {
	if maxEvents <= 0 || maxBytes < 64 {
		return nil, errors.New("background log reader limits are invalid")
	}
	if _, err := BackgroundLogPath(directory, "exec_validation"); err != nil {
		return nil, err
	}
	return &BackgroundLogReader{directory: directory, maxEvents: maxEvents, maxBytes: maxBytes}, nil
}

// Read 返回 cursor 之后的一页完整事件。next_cursor 是本页最后事件，空页保持输入 cursor。
func (r *BackgroundLogReader) Read(id ExecutionID, cursor uint64) (protocol.ExecutionLogPage, error) {
	if r == nil {
		return protocol.ExecutionLogPage{}, ErrBackgroundLogCorrupt
	}
	var path string
	var err error
	if r.trustedFD {
		if !validStoredExecutionID(id) {
			return protocol.ExecutionLogPage{}, ErrBackgroundLogNotFound
		}
		path = filepath.Join(r.directory, string(id)+backgroundLogFileSuffix)
	} else {
		path, err = BackgroundLogPath(r.directory, id)
	}
	if err != nil {
		return protocol.ExecutionLogPage{}, ErrBackgroundLogNotFound
	}
	file, err := openRegularLog(path)
	if err != nil {
		return protocol.ExecutionLogPage{}, err
	}
	defer file.Close()

	page := protocol.ExecutionLogPage{Events: []protocol.ExecutionEvent{}, NextCursor: cursor}
	scanner := bufio.NewScanner(file)
	scanner.Split(splitCompleteNDJSONLine)
	maxLine := r.maxBytes
	if maxLine > int64(maxInt()) {
		maxLine = int64(maxInt())
	}
	scanner.Buffer(make([]byte, minInt(64*1024, int(maxLine))), int(maxLine))
	expected := uint64(1)
	lastSequence := uint64(0)
	terminalSequence := uint64(0)
	scanComplete := true

scanLoop:
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		event, decodeErr := decodeStoredEvent(line)
		if decodeErr != nil || event.ExecutionID != string(id) || event.Sequence != expected || terminalSequence != 0 {
			return protocol.ExecutionLogPage{}, ErrBackgroundLogCorrupt
		}
		lastSequence = event.Sequence
		expected++
		if event.Terminal() {
			terminalSequence = event.Sequence
		}
		if event.Sequence <= cursor {
			continue
		}
		if len(page.Events) >= r.maxEvents {
			scanComplete = false
			break scanLoop
		}
		candidate := page
		candidate.Events = append(append([]protocol.ExecutionEvent(nil), page.Events...), event)
		candidate.NextCursor = event.Sequence
		candidate.Complete = event.Terminal()
		encoded, marshalErr := json.Marshal(candidate)
		if marshalErr != nil {
			return protocol.ExecutionLogPage{}, ErrBackgroundLogCorrupt
		}
		if int64(len(encoded)) > r.maxBytes {
			if len(page.Events) == 0 {
				return protocol.ExecutionLogPage{}, ErrBackgroundLogCorrupt
			}
			scanComplete = false
			break scanLoop
		}
		page = candidate
		if len(page.Events) == r.maxEvents || int64(len(encoded)) == r.maxBytes {
			scanComplete = false
			break scanLoop
		}
	}
	if err := scanner.Err(); err != nil {
		return protocol.ExecutionLogPage{}, ErrBackgroundLogCorrupt
	}
	if scanComplete && cursor > lastSequence {
		return protocol.ExecutionLogPage{}, ErrInvalidLogCursor
	}
	page.Complete = terminalSequence != 0 && terminalSequence <= page.NextCursor
	encoded, err := json.Marshal(page)
	if err != nil || int64(len(encoded)) > r.maxBytes {
		return protocol.ExecutionLogPage{}, ErrBackgroundLogCorrupt
	}
	return page, nil
}

func openRegularLog(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, ErrBackgroundLogNotFound
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrBackgroundLogNotFound
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		file.Close()
		return nil, ErrBackgroundLogNotFound
	}
	return file, nil
}

func splitCompleteNDJSONLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return index + 1, data[:index], nil
	}
	if atEOF {
		// writer 可能正处于 partial write；没有 LF 的尾部不是完整事件，暂不解释为日志数据。
		return len(data), nil, nil
	}
	return 0, nil, nil
}

func decodeStoredEvent(line []byte) (protocol.ExecutionEvent, error) {
	if len(line) == 0 {
		return protocol.ExecutionEvent{}, ErrBackgroundLogCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var event protocol.ExecutionEvent
	if err := decoder.Decode(&event); err != nil {
		return protocol.ExecutionEvent{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || event.Validate() != nil {
		return protocol.ExecutionEvent{}, ErrBackgroundLogCorrupt
	}
	return event, nil
}

func maxInt() int { return int(^uint(0) >> 1) }

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
