package executor

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCodexTimeoutsOnlyConfigureWebsocketLiveness(t *testing.T) {
	cfg := &config.Config{}
	cfg.Codex.WebsocketIdleTimeoutSeconds = 44
	cfg.Codex.WebsocketPingIntervalSeconds = 5

	timeouts := codexTimeoutsFromConfig(cfg)
	if timeouts.websocketIdle != 44*time.Second {
		t.Fatalf("websocketIdle = %s, want 44s", timeouts.websocketIdle)
	}
	if timeouts.websocketPing != 5*time.Second {
		t.Fatalf("websocketPing = %s, want 5s", timeouts.websocketPing)
	}
}

func TestCodexTimeoutsKeepPingBelowIdleDeadline(t *testing.T) {
	cfg := &config.Config{}
	cfg.Codex.WebsocketIdleTimeoutSeconds = 10
	cfg.Codex.WebsocketPingIntervalSeconds = 20

	timeouts := codexTimeoutsFromConfig(cfg)
	if timeouts.websocketIdle != 10*time.Second {
		t.Fatalf("websocketIdle = %s, want 10s", timeouts.websocketIdle)
	}
	if timeouts.websocketPing != 5*time.Second {
		t.Fatalf("websocketPing = %s, want 5s", timeouts.websocketPing)
	}
}
