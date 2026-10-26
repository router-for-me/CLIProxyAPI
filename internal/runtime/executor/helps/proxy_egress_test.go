package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/egresstest"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestProxyEgressStandardClient(t *testing.T) {
	for _, where := range []string{"global", "auth", "request"} {
		t.Run(where+"/"+egresstest.BadProxy, func(t *testing.T) {
			addr, count := egresstest.Target(t)
			cfg, auth, ctx := &config.Config{}, &cliproxyauth.Auth{}, context.Background()
			switch where {
			case "global":
				cfg = &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: egresstest.BadProxy}}
			case "auth":
				auth.ProxyURL = egresstest.BadProxy
			default:
				ctx = coreexecutor.WithRequestProxyURL(ctx, egresstest.BadProxy)
			}
			egresstest.Get(t, egresstest.ClientRT{C: NewProxyAwareHTTPClient(ctx, cfg, auth, 0)}, "http", addr, count)
		})
	}
}

func TestProxyEgressDevinClient(t *testing.T) {
	addr, count := egresstest.Target(t)
	c := NewDevinHTTPClient(context.Background(), &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: egresstest.BadProxy}}, nil, 0)
	egresstest.Get(t, egresstest.ClientRT{C: c}, "http", addr, count)
}

func TestProxyEgressUtlsChromeDialer(t *testing.T) {
	addr, count := egresstest.Target(t)
	egresstest.Get(t, newUtlsRoundTripper(egresstest.BadProxy), "https", addr, count)
}

func TestProxyEgressUtlsClaudeCodeDialer(t *testing.T) {
	addr, count := egresstest.Target(t)
	egresstest.Get(t, newClaudeCodeRoundTripper(egresstest.BadProxy), "https", addr, count)
}
