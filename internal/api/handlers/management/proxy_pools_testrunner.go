package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// probeTimeout is the hard cap for one connectivity probe. This is an
// operator-triggered ops check (credential/setup phase), not request-serving
// traffic, so an explicit client timeout is allowed per the repo timeout
// policy.
const probeTimeout = 30 * time.Second

// probeContextTimeout is the default probe deadline; the client timeout above
// is the hard cap for relay tests that ignore the request context deadline.
const probeContextTimeout = 8 * time.Second

// probeResult carries the probe outcome into the persistence helper.
type probeResult struct {
	ok     bool
	detail string
}

// testStatusFor maps a probe outcome to the persisted test_status value.
func testStatusFor(ok bool) string {
	if ok {
		return "active"
	}
	return "error"
}

// runProxyPoolTest executes the connectivity probe for one pool.
//   - http pools: HEAD https://www.google.com/ through the pool's own proxy
//     URL via the full composite semantics (noProxy + strict).
//   - relay pools: GET https://httpbin.org/get with the
//     x-relay-target/x-relay-path contract against the pool's relay base
//     (mirrors 9router's testVercelRelay).
func (h *Handler) runProxyPoolTest(ctx context.Context, pool store.ProxyPool) error {
	ctx, cancel := context.WithTimeout(ctx, probeContextTimeout)
	defer cancel()

	client := &http.Client{Timeout: probeTimeout}

	var req *http.Request
	var errReq error
	if pool.Type == "vercel" || pool.Type == "cloudflare" || pool.Type == "deno" {
		rt, errRelay := proxyutil.NewRelayTransport(pool.ProxyURL)
		if errRelay != nil {
			return fmt.Errorf("invalid relay base: %w", errRelay)
		}
		client.Transport = rt
		req, errReq = http.NewRequestWithContext(ctx, http.MethodGet, "https://httpbin.org/get", nil)
		if errReq != nil {
			return errReq
		}
		req.Header.Set("x-relay-target", "https://httpbin.org")
		req.Header.Set("x-relay-path", "/get")
	} else {
		rt, _, errBuild := proxyutil.BuildProxiedTransport(pool.ProxyURL)
		if errBuild != nil {
			return fmt.Errorf("invalid proxy URL: %w", errBuild)
		}
		client.Transport = rt
		req, errReq = http.NewRequestWithContext(ctx, http.MethodHead, "https://www.google.com/", nil)
		if errReq != nil {
			return errReq
		}
	}

	resp, errDo := client.Do(req)
	if errDo != nil {
		return errDo
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Debugf("probe body close: %v", err)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("probe returned status %d", resp.StatusCode)
	}
	return nil
}

// recordProxyPoolTest persists the probe outcome. Status-only: is_active is
// never touched (the operator decides). The row is re-read so concurrent
// edits to unrelated columns are not clobbered.
func (h *Handler) recordProxyPoolTest(ctx context.Context, srcs store.ProxyPoolStore, pool store.ProxyPool, ok bool, detail string, _ *time.Time) (*store.ProxyPool, error) {
	if srcs == nil {
		return nil, errors.New("proxy pool store is nil")
	}
	current, errGet := srcs.Get(ctx, pool.ID)
	if errGet != nil {
		return nil, errGet
	}
	current.TestStatus = testStatusFor(ok)
	now := time.Now()
	current.LastTestedAt = &now
	detail = strings.TrimSpace(detail)
	if ok {
		current.LastError = ""
	} else {
		current.LastError = detail
	}
	updated, errUpdate := srcs.Update(ctx, *current)
	if errUpdate != nil {
		return nil, errUpdate
	}
	return updated, nil
}
