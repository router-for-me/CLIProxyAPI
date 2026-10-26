package live

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/egresstest"
)

func TestProxyEgressSidebandDialer(t *testing.T) {
	addr, count := egresstest.Target(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := newSidebandDialer(egresstest.BadProxy).DialContext(ctx, "ws://"+addr+"/", nil)
	if err == nil {
		_ = conn.Close()
		t.Errorf("dial succeeded through an unusable proxy")
	}
	egresstest.NoLeak(t, count)
}
