package executor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// codexTTFTTimeout returns the configured TTFT duration, or 0 if disabled.
func codexTTFTTimeout(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.CodexTTFTTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.CodexTTFTTimeoutSeconds) * time.Second
}

// codexTTFTTimeoutErr returns a 504 error indicating the TTFT expired.
// It intentionally does NOT carry RetryAfter so that request-retry logic
// does not loop on this error.
func codexTTFTTimeoutErr(timeout time.Duration) statusErr {
	return statusErr{
		code: http.StatusGatewayTimeout,
		msg:  fmt.Sprintf("codex upstream did not produce first response event within %s", timeout),
	}
}

// isDeadlineLikeError returns true when err signals a timeout / deadline exceeded.
func isDeadlineLikeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
