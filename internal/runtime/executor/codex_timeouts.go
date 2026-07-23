package executor

import (
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	codexDefaultWebsocketIdleTimeout  = 15 * time.Minute
	codexDefaultWebsocketPingInterval = 30 * time.Second
)

type codexTimeouts struct {
	websocketIdle time.Duration
	websocketPing time.Duration
}

// codexTimeoutsFromConfig only controls websocket liveness. HTTP requests and
// response bodies remain governed by their request context so long-running
// streams are never terminated by a proxy-owned deadline.
func codexTimeoutsFromConfig(cfg *config.Config) codexTimeouts {
	timeouts := codexTimeouts{
		websocketIdle: codexDefaultWebsocketIdleTimeout,
		websocketPing: codexDefaultWebsocketPingInterval,
	}
	if cfg == nil {
		return timeouts
	}
	timeouts.websocketIdle = positiveSecondsOrDefault(cfg.Codex.WebsocketIdleTimeoutSeconds, timeouts.websocketIdle)
	timeouts.websocketPing = positiveSecondsOrDefault(cfg.Codex.WebsocketPingIntervalSeconds, timeouts.websocketPing)
	if timeouts.websocketPing >= timeouts.websocketIdle {
		timeouts.websocketPing = timeouts.websocketIdle / 2
		if timeouts.websocketPing <= 0 {
			timeouts.websocketPing = time.Second
		}
	}
	return timeouts
}

func positiveSecondsOrDefault(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

type codexRequestTimeoutError struct {
	phase   string
	timeout time.Duration
}

func (e codexRequestTimeoutError) Error() string {
	return fmt.Sprintf("codex executor: upstream %s timeout after %s", e.phase, e.timeout)
}

func (codexRequestTimeoutError) StatusCode() int {
	return http.StatusGatewayTimeout
}

func (codexRequestTimeoutError) RetryAfter() *time.Duration {
	return nil
}

func (codexRequestTimeoutError) IsRequestScoped() bool {
	return true
}
