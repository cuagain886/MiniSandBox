// Package security 提供跨日志、错误、指标和诊断观察面的秘密泄露回归测试工具。
// 本包只扫描测试哨兵，不读取真实凭据、生产日志或宿主机配置。
package security

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

type sentinel struct {
	kind  string
	value []byte
}

type redactionScanner struct {
	sentinels []sentinel
}

func newRedactionScanner(values map[string]string) redactionScanner {
	kinds := make([]string, 0, len(values))
	for kind := range values {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	result := redactionScanner{sentinels: make([]sentinel, 0, len(kinds))}
	for _, kind := range kinds {
		if values[kind] != "" {
			result.sentinels = append(result.sentinels, sentinel{kind: kind, value: []byte(values[kind])})
		}
	}
	return result
}

func (scanner redactionScanner) scan(surface string, content []byte) error {
	for _, candidate := range scanner.sentinels {
		if containsBytes(content, candidate.value) {
			// 失败信息只携带测试哨兵摘要，避免测试框架成为二次泄露面。
			digest := sha256.Sum256(candidate.value)
			return fmt.Errorf("secret sentinel detected: surface=%s kind=%s sha256=%x", surface, candidate.kind, digest[:8])
		}
	}
	return nil
}

func containsBytes(content, candidate []byte) bool {
	if len(candidate) == 0 || len(candidate) > len(content) {
		return false
	}
	for offset := 0; offset <= len(content)-len(candidate); offset++ {
		matched := true
		for index := range candidate {
			if content[offset+index] != candidate[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
