package runner

import (
	"errors"
	"sort"
	"strings"

	"minisandbox/internal/runnerbootstrap"
)

// ErrInvalidExecutionEnvironment 是 execution 最终环境违反格式或 limit 时的稳定内部错误。
var ErrInvalidExecutionEnvironment = errors.New("invalid execution environment")

var executionEnvironmentDenylist = map[string]struct{}{
	"BASH_ENV":                {},
	"ENV":                     {},
	"GCONV_PATH":              {},
	"LD_AUDIT":                {},
	"LD_PRELOAD":              {},
	"RUNNER_BOOTSTRAP":        {},
	"RUNNER_BOOTSTRAP_CONFIG": {},
	"RUNNER_SOCKET":           {},
	"RUNNER_TOKEN":            {},
}

// EnvironmentBuilder 清洗 image env、应用 execution 覆盖并执行最终环境上限。
type EnvironmentBuilder struct {
	maxVariables  int
	maxKeyBytes   int
	maxValueBytes int
	maxTotalBytes int64
}

// NewEnvironmentBuilder 从可信 bootstrap limits 创建环境构造器。
func NewEnvironmentBuilder(limits runnerbootstrap.Limits) (*EnvironmentBuilder, error) {
	if limits.MaxEnvVars <= 0 || limits.MaxEnvKeyBytes <= 0 ||
		limits.MaxEnvValueBytes <= 0 || limits.MaxEnvTotalBytes <= 0 {
		return nil, errors.New("runner environment limits are invalid")
	}
	return &EnvironmentBuilder{
		maxVariables:  limits.MaxEnvVars,
		maxKeyBytes:   limits.MaxEnvKeyBytes,
		maxValueBytes: limits.MaxEnvValueBytes,
		maxTotalBytes: limits.MaxEnvTotalBytes,
	}, nil
}

// Build 返回按 key 排序的 `KEY=value` 新切片，不读取宿主机环境，也不修改输入。
// image 中格式错误的遗留条目会被清洗掉；request 中格式错误的条目会拒绝整个请求。
func (b *EnvironmentBuilder) Build(imageEnvironment []string, requestEnvironment map[string]string) ([]string, error) {
	if b == nil {
		return nil, ErrInvalidExecutionEnvironment
	}
	merged := make(map[string]string, len(imageEnvironment)+len(requestEnvironment))
	for _, entry := range imageEnvironment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !validEnvironmentKey(key) || strings.IndexByte(value, 0) >= 0 || deniedEnvironmentKey(key) {
			continue
		}
		merged[key] = value
	}
	for key, value := range requestEnvironment {
		if !validEnvironmentKey(key) || strings.IndexByte(value, 0) >= 0 {
			return nil, ErrInvalidExecutionEnvironment
		}
		if deniedEnvironmentKey(key) {
			delete(merged, key)
			continue
		}
		merged[key] = value
	}
	if len(merged) > b.maxVariables {
		return nil, ErrInvalidExecutionEnvironment
	}
	keys := make([]string, 0, len(merged))
	var totalBytes int64
	for key, value := range merged {
		if len(key) > b.maxKeyBytes || len(value) > b.maxValueBytes {
			return nil, ErrInvalidExecutionEnvironment
		}
		entryBytes := int64(len(key)) + int64(len(value))
		if entryBytes > b.maxTotalBytes-totalBytes {
			return nil, ErrInvalidExecutionEnvironment
		}
		totalBytes += entryBytes
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+merged[key])
	}
	return result, nil
}

func validEnvironmentKey(key string) bool {
	if key == "" || strings.IndexByte(key, 0) >= 0 || !environmentKeyStart(key[0]) {
		return false
	}
	for index := 1; index < len(key); index++ {
		value := key[index]
		if !environmentKeyStart(value) && (value < '0' || value > '9') {
			return false
		}
	}
	return true
}

func environmentKeyStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func deniedEnvironmentKey(key string) bool {
	canonical := strings.ToUpper(key)
	if strings.HasPrefix(canonical, "MINISANDBOX_") {
		return true
	}
	_, denied := executionEnvironmentDenylist[canonical]
	return denied
}
