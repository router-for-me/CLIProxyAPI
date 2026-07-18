package pluginhost

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

const pluginHostABIVersion = pluginabi.ABIVersion

type pluginClient interface {
	Call(ctx context.Context, method string, request []byte) ([]byte, error)
	Shutdown()
}

type pluginLoader interface {
	Open(file pluginFile, host *Host) (pluginClient, error)
}
