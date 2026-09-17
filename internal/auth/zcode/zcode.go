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

func (c *CliLogin) provider() string {
	if strings.EqualFold(strings.TrimSpace(c.Provider), "bigmodel") {
		return "bigmodel"
	}
	return "zai"
}

func (c *ZaiCliLogin) base() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return ZCodeAPIBase
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}

func (c *ZaiCliLogin) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (c *ZaiCliLogin) now() time.Time {
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
	c.PollToken = randomHex(32)
	var flow CliFlow
	body := fmt.Sprintf(`{"provider":%q}`, c.provider())
	err := c.doEnvelope(ctx, http.MethodPost,
		c.base()+"/oauth/cli/init",
		strings.NewReader(body),
		c.PollToken, &flow)
	if err != nil {
		return nil, err
	}
	if flow.FlowID == "" || flow.AuthorizeURL == "" {
		return nil, fmt.Errorf("zcode: cli/init returned empty flow")
	}
	flow.Provider = c.provider()
	return &flow, nil
}

// Complete polls oauth/cli/poll/{flowID} until ready, failed or deadline.
func (c *CliLogin) Complete(ctx context.Context, flow *CliFlow, timeout time.Duration) (*CliTokens, error) {
	if flow == nil || flow.FlowID == "" {
		return nil, fmt.Errorf("zcode: flow not started")
	}
	deadline := c.now().Add(timeout)
	if exp := time.Unix(flow.ExpiresAt, 0); exp.Before(deadline) {
		deadline = exp
	}
	interval := time.Duration(flow.PollIntervalSec) * time.Second
	if interval < time.Second {
		interval = time.Second
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
			// The ready payload carries the provider-scoped token; accept either
			// field so a provider mismatch or a future rename still resolves.
			accessToken := strings.TrimSpace(data.Zai.AccessToken)
			if accessToken == "" {
				accessToken = strings.TrimSpace(data.Bigmodel.AccessToken)
			}
			if accessToken == "" {
				return nil, fmt.Errorf("zcode: ready response missing access_token")
			}
			return &CliTokens{
				AccessToken: accessToken,
				JWT:         strings.TrimSpace(data.Token),
				UserID:      data.User.UserID,
			}, nil
		case "failed":
			return nil, fmt.Errorf("zcode: authorization failed")
		case "pending":
			c.sleep(interval)
		default:
			return nil, fmt.Errorf("zcode: unexpected poll status %q", data.Status)
		}
	}
}
