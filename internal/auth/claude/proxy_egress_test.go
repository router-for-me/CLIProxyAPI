package claude

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/egresstest"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestProxyEgressClaudeOAuthTransport(t *testing.T) {
	addr, count := egresstest.Target(t)
	c := NewAnthropicHttpClient(&config.SDKConfig{ProxyURL: egresstest.BadProxy})
	egresstest.Get(t, c.Transport, "https", addr, count)
}
