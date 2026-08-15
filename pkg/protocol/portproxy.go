package protocol

import "errors"

// ErrInvalidPort 表示端口不在代理允许范围内。
var ErrInvalidPort = errors.New("invalid proxy port")

// ValidateProxyPort 校验端口是服务端允许范围内的十进制 TCP 端口。
//
// 端口只能来自路由解析结果，不信任任何调用方提供的 host、scheme 或 socket；
// min/max 由服务端配置固定，普通请求不能扩大范围。
func ValidateProxyPort(port int, minimum, maximum int) error {
	if port < minimum || port > maximum || port < 1 || port > 65535 {
		return ErrInvalidPort
	}
	return nil
}
