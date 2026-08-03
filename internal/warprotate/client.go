// Package warprotate coordinates safe Warp LB restarts with the host rotate agent.
package warprotate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	httpClient = &http.Client{Timeout: 8 * time.Second}
	asyncHTTP  = &http.Client{Timeout: 5 * time.Minute}
)

// ClaimResult is returned by the rotate agent after a successful claim+drain.
type ClaimResult struct {
	Claimed  bool   `json:"claimed"`
	Instance string `json:"instance,omitempty"`
	Server   string `json:"server,omitempty"`
}

// CloseIdleFunc closes idle connections on the proxy HTTP client pool.
type CloseIdleFunc func()

// PrepareBeforeUpstream tries to claim a rotate key. On success it drains the
// nominated LB, closes idle proxy connections, fires async restart, then returns.
// Failures are non-fatal: the caller continues the upstream request.
func PrepareBeforeUpstream(ctx context.Context, baseURL string, closeIdle CloseIdleFunc) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	claimCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	result, err := claimAndDrain(claimCtx, baseURL)
	if err != nil {
		log.Debugf("warprotate: claim skipped: %v", err)
		return
	}
	if result == nil || !result.Claimed {
		return
	}
	log.Infof("warprotate: claimed %s/%s, closing idle proxy connections", result.Instance, result.Server)
	if closeIdle != nil {
		closeIdle()
	}
	go restartAsync(baseURL, result.Instance, result.Server)
}

func claimAndDrain(ctx context.Context, baseURL string) (*ClaimResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/claim-and-drain", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out ClaimResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func restartAsync(baseURL, instance, server string) {
	payload, _ := json.Marshal(map[string]string{
		"instance": instance,
		"server":   server,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/restart", bytes.NewReader(payload))
	if err != nil {
		log.Warnf("warprotate: restart request build failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := asyncHTTP.Do(req)
	if err != nil {
		log.Warnf("warprotate: restart %s failed: %v", instance, err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		log.Warnf("warprotate: restart %s status %d: %s", instance, resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}
	log.Infof("warprotate: restart done %s/%s", instance, server)
}

// SetHTTPClientForTest overrides the short HTTP client (tests only).
func SetHTTPClientForTest(c *http.Client) {
	if c != nil {
		httpClient = c
	}
}

var once sync.Once

// EnsureInitialized is a no-op placeholder for future shared state.
func EnsureInitialized() { once.Do(func() {}) }
