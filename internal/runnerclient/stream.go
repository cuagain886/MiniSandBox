package runnerclient

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"minisandbox/pkg/protocol"
)

// DecodeSSE 逐条解码 runner 的 data 事件，并按接收顺序交给 consume。
//
// consume 返回错误时立即停止读取，让调用方可以通过关闭响应体传播取消。
func DecodeSSE(reader io.Reader, consume func(protocol.ExecutionEvent) error) error {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event protocol.ExecutionEvent
		if err := json.Unmarshal(
			[]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))),
			&event,
		); err != nil {
			return err
		}
		if err := consume(event); err != nil {
			return err
		}
	}
	return scanner.Err()
}
