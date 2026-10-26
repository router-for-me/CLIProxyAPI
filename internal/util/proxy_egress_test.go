package util

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/egresstest"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestProxyEgressSetProxy(t *testing.T) {
	addr, count := egresstest.Target(t)
	c := SetProxy(&config.SDKConfig{ProxyURL: egresstest.BadProxy}, &http.Client{})
	egresstest.Get(t, c.Transport, "http", addr, count)
}
