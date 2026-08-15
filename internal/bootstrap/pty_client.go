package bootstrap

import (
	"context"

	"minisandbox/internal/application"
	"minisandbox/internal/runnerclient"
)

// applicationPTYDialer 把固定 Unix Socket runner factory 适配为 PTY 端口。
type applicationPTYDialer struct {
	factory *runnerclient.Factory
}

// DialPTY 与指定 sandbox 的 runner PTY endpoint 建立固定帧连接。
func (d applicationPTYDialer) DialPTY(ctx context.Context, sandboxID string) (application.PTYFrameConnection, error) {
	client, err := d.factory.Client(sandboxID)
	if err != nil {
		return nil, err
	}
	return client.DialPTY(ctx)
}
