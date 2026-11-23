//go:build solaris

package live

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// The solaris build tag also covers illumos. Pion requires SO_REUSEPORT, which is unavailable on these targets.
func newMediaRelay(config.CodexLiveMediaRelayConfig, *mediaSessionLimiter) (mediaRelayFactory, error) {
	return nil, fmt.Errorf("Codex Live WebRTC media relay on %s: %w", runtime.GOOS, errors.ErrUnsupported)
}
