package zcode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ZCodeAPIBase is the zcode.z.ai API base for CLI login and token endpoints.
const ZCodeAPIBase = "https://zcode.z.ai/api/v1"

// maxPollInterval caps a server-supplied poll interval.
const maxPollInterval = 30 * time.Second

// LoginTimeout is the shared bound on how long the OAuth flow waits for the user
// to authorize. Callers (CLI runner and management handler) use it directly so the
// value is defined once.
const LoginTimeout = 5 * time.Minute

// ZCodeEnvelope is the {code,data,msg} wrapper returned by zcode.z.ai.
type ZCodeEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
	Msg  string          `json:"msg"`
}

// CliFlow describes an in-progress server-mediated OAuth flow.
type CliFlow struct {
	FlowID          string `json:"flow_id"`
	AuthorizeURL    string `json:"authorize_url"`
	PollIntervalSec int    `json:"poll_interval_sec"`
	ExpiresAt       int64  `json:"expires_at"`

	// Provider is the account platform this flow targets ("zai" or "bigmodel").
	Provider string `json:"-"`
}

// CliTokens carries the credentials returned when the flow reaches "ready".
type CliTokens struct {
	AccessToken string
	JWT         string
	UserID      string
}

// CliLogin implements the server-mediated ZCode CLI OAuth flow, shared by both
// provider variants. The ZCode server owns the callback
// (zcode.z.ai/api/v1/oauth/cli/callback/<provider>), so no localhost redirect is
// needed and cross-device login works. Provider "zai" (global, default) returns
// data.zai.access_token; "bigmodel" (China) returns data.bigmodel.access_token.
type CliLogin struct {
	HTTP      *http.Client
	PollToken string
	// Provider selects the upstream account platform: "zai" (default) or
	// "bigmodel".
	Provider string
	baseURL  string              // injectable; empty = ZCodeAPIBase (used by tests)
	Sleep    func(time.Duration) // injectable; nil = time.Sleep
	Now      func() time.Time    // injectable; nil = time.Now
}

// ZaiCliLogin is the Z.ai (global) variant of the CLI flow, kept as the
// historical type name for callers that only need the Z.ai provider.
type ZaiCliLogin = CliLogin

// ZCode account platforms. These are the accepted values of --zcode-provider
// and the management ?provider= query.
const (
	ProviderZai      = "zai"
	ProviderBigmodel = "bigmodel"
)

// NormalizeProvider canonicalizes a ZCode provider identifier. Unknown values
// are rejected so a typo (e.g. "bigmodele") cannot silently perform a Z.ai
// login and persist a credential pointed at the wrong endpoint. An empty value
// defaults to the global Z.ai platform.
func NormalizeProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", ProviderZai:
		return ProviderZai, nil
	case ProviderBigmodel:
		return ProviderBigmodel, nil
	default:
		return "", fmt.Errorf("zcode: unknown provider %q (want %q or %q)", provider, ProviderZai, ProviderBigmodel)
	}
}

func (c *CliLogin) provider() (string, error) {
	return NormalizeProvider(c.Provider)
}

func (c *CliLogin) base() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return ZCodeAPIBase
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("zcode: read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (c *CliLogin) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

// sleepCtx waits for d, aborting early when ctx is done. An injected Sleep (used
// by tests to avoid wall-clock waits) is honored as-is.
func (c *CliLogin) sleepCtx(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		c.Sleep(d)
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *CliLogin) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *ZaiCliLogin) doEnvelope(ctx context.Context, method, url string, body io.Reader, bearer string, out any) error {
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var env ZCodeEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("zcode: invalid envelope (status=%d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || env.Code != 0 {
		return fmt.Errorf("zcode: %s failed: status=%d msg=%q", url, resp.StatusCode, env.Msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("zcode: decode data: %w", err)
		}
	}
	return nil
}

// Start calls oauth/cli/init and returns the flow to authorize.
func (c *CliLogin) Start(ctx context.Context) (*CliFlow, error) {
	provider, err := c.provider()
	if err != nil {
		return nil, err
	}
	pollToken, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	c.PollToken = pollToken
	var flow CliFlow
	body := fmt.Sprintf(`{"provider":%q}`, provider)
	err = c.doEnvelope(ctx, http.MethodPost,
		c.base()+"/oauth/cli/init",
		strings.NewReader(body),
		c.PollToken, &flow)
	if err != nil {
		return nil, err
	}
	if flow.FlowID == "" || flow.AuthorizeURL == "" {
		return nil, fmt.Errorf("zcode: cli/init returned empty flow")
	}
	flow.Provider = provider
	return &flow, nil
}

// Complete polls oauth/cli/poll/{flowID} until ready, failed or deadline.
func (c *CliLogin) Complete(ctx context.Context, flow *CliFlow, timeout time.Duration) (*CliTokens, error) {
	if flow == nil || flow.FlowID == "" {
		return nil, fmt.Errorf("zcode: flow not started")
	}
	deadline := c.now().Add(timeout)
	// expires_at is optional; a zero value must not collapse the deadline to 1970.
	if flow.ExpiresAt > 0 {
		if exp := time.Unix(flow.ExpiresAt, 0); exp.Before(deadline) {
			deadline = exp
		}
	}
	// Clamp a server-supplied poll interval so a hostile/large value cannot park
	// the login goroutine for an unbounded time.
	interval := time.Duration(flow.PollIntervalSec) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	if interval > maxPollInterval {
		interval = maxPollInterval
	}
	for {
		if !c.now().Before(deadline) {
			return nil, fmt.Errorf("zcode: authorization timed out")
		}
		var data struct {
			Status string `json:"status"`
			Token  string `json:"token"`
			User   struct {
				UserID string `json:"user_id"`
			} `json:"user"`
			Zai struct {
				AccessToken string `json:"access_token"`
			} `json:"zai"`
			Bigmodel struct {
				AccessToken string `json:"access_token"`
			} `json:"bigmodel"`
		}
		url := fmt.Sprintf("%s/oauth/cli/poll/%s", c.base(), flow.FlowID)
		if err := c.doEnvelope(ctx, http.MethodGet, url, nil, c.PollToken, &data); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		switch data.Status {
		case "ready":
			// Select the token field for the flow's provider (empty = zai) so a
			// provider mismatch is a loud error rather than a silent cross-provider
			// credential.
			var accessToken string
			if flow.Provider == ProviderBigmodel {
				accessToken = strings.TrimSpace(data.Bigmodel.AccessToken)
			} else {
				accessToken = strings.TrimSpace(data.Zai.AccessToken)
			}
			if accessToken == "" {
				return nil, fmt.Errorf("zcode: ready response missing access_token for provider %q", flow.Provider)
			}
			return &CliTokens{
				AccessToken: accessToken,
				JWT:         strings.TrimSpace(data.Token),
				UserID:      data.User.UserID,
			}, nil
		case "failed":
			return nil, fmt.Errorf("zcode: authorization failed")
		case "pending":
			if err := c.sleepCtx(ctx, interval); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("zcode: unexpected poll status %q", data.Status)
		}
	}
}
