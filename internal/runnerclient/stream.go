package runnerclient

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"minisandbox/pkg/protocol"
)

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
