package executor

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/egresstest"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestProxyEgressAntigravityClient(t *testing.T) {
	addr, count := egresstest.Target(t)
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: egresstest.BadProxy}}
	c := newAntigravityHTTPClient(context.Background(), cfg, &cliproxyauth.Auth{ID: "a"}, time.Second)
	egresstest.Get(t, egresstest.ClientRT{C: c}, "http", addr, count)
}

func TestProxyEgressCodexWebsocketDialer(t *testing.T) {
	for _, where := range []string{"global", "auth"} {
		t.Run(where+"/"+egresstest.BadProxy, func(t *testing.T) {
			addr, count := egresstest.Target(t)
			cfg, auth := &config.Config{}, &cliproxyauth.Auth{}
			if where == "global" {
				cfg.ProxyURL = egresstest.BadProxy
			} else {
				auth.ProxyURL = egresstest.BadProxy
			}
			d := newProxyAwareWebsocketDialer(context.Background(), cfg, auth)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, _, err := d.DialContext(ctx, "ws://"+addr+"/", nil)
			if err == nil {
				_ = conn.Close()
				t.Errorf("dial succeeded through an unusable proxy")
			}
			egresstest.NoLeak(t, count)
		})
	}
}
