package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/egresstest"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestProxyEgressRoundTripperProvider(t *testing.T) {
	addr, count := egresstest.Target(t)
	rt := newDefaultRoundTripperProvider().RoundTripperFor(&coreauth.Auth{ProxyURL: egresstest.BadProxy})
	if rt == nil {
		t.Errorf("nil round tripper: the caller falls back to the default transport")
		return
	}
	egresstest.Get(t, rt, "http", addr, count)
}
