package quota

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	"golang.org/x/net/proxy"
)

const (
	ClaudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	CodexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"
	maxUsageBytes  = 128 * 1024
	// claudeUserAgent identifies polls as Claude Code, which the usage endpoint expects.
	claudeUserAgent = "claude-cli/2.1.280 (external, cli)"
	// socksConnectTimeout bounds the TCP connect to a SOCKS proxy.
	socksConnectTimeout = 10 * time.Second
)

// Credential is the subset of a CPA auth file needed to read quota. It is
// held only for the duration of one poll and never stored.
type Credential struct {
	Type         string `json:"type"`
	AccessToken  string `json:"access_token"`
	AccountUUID  string `json:"account_uuid"`
	AccountID    string `json:"account_id"`
	Organization string `json:"organization_uuid"`
	Disabled     bool   `json:"disabled"`
	ProxyURL     string `json:"proxy_url"`
	BaseURL      string `json:"base_url"`
}

// Identity hashes the upstream account identity, falling back to the token
// when the credential carries no account ID.
func (c Credential) Identity() string {
	value := strings.ToLower(strings.TrimSpace(c.Type)) + "\x00" + c.AccountUUID + "\x00" + c.AccountID + "\x00" + c.Organization
	if c.AccountUUID == "" && c.AccountID == "" {
		value += "\x00" + c.AccessToken
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Error is a sanitized poll failure. Code never contains response bodies.
type Error struct {
	Code       string
	Status     int
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Code }

// AuthRejected reports a 401/403 from the quota endpoint.
func (e *Error) AuthRejected() bool { return e.Status == 401 || e.Status == 403 }

// Fetcher reads one account's quota.
type Fetcher interface {
	Fetch(context.Context, Credential) (Snapshot, error)
}

// HTTPFetcher polls the provider usage endpoints.
type HTTPFetcher struct {
	Client     *http.Client
	mu         sync.Mutex
	transports map[string]*http.Transport
	Now        func() time.Time
	// ClaudeURL and CodexURL override the endpoints in tests.
	ClaudeURL, CodexURL string
}

// NewHTTPFetcher returns a fetcher that never follows redirects (which could
// forward the bearer token) and honors proxy environment variables when no account proxy is set.
func NewHTTPFetcher() *HTTPFetcher {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 1
	return &HTTPFetcher{
		Client: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Now: time.Now,
	}
}

// Close releases idle connections.
func (f *HTTPFetcher) Close() {
	f.Client.CloseIdleConnections()
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, transport := range f.transports {
		transport.CloseIdleConnections()
		delete(f.transports, key)
	}
}

// clientFor caches account transports without exposing proxy URLs in errors.
func (f *HTTPFetcher) clientFor(raw string) (*http.Client, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return f.Client, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	transport := f.transports[raw]
	if transport == nil {
		var mode proxyutil.Mode
		var err error
		transport, mode, err = proxyutil.BuildHTTPTransport(raw)
		if err != nil || transport == nil || mode == proxyutil.ModeInherit {
			return nil, &Error{Code: "invalid_proxy_url"}
		}
		if err := contextSOCKS(transport, raw); err != nil {
			return nil, &Error{Code: "invalid_proxy_url"}
		}
		transport.MaxIdleConns = 2
		transport.MaxIdleConnsPerHost = 1
		if f.transports == nil {
			f.transports = make(map[string]*http.Transport)
		}
		f.transports[raw] = transport
	}
	client := *f.Client
	client.Transport = transport
	return &client, nil
}

// contextSOCKS replaces the SDK's SOCKS dial, which ignores the request
// context, so a proxy that stalls the handshake cannot outlive the poll.
func contextSOCKS(transport *http.Transport, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "socks5" && u.Scheme != "socks5h") {
		return nil
	}
	var auth *proxy.Auth
	if u.User != nil {
		password, _ := u.User.Password()
		auth = &proxy.Auth{User: u.User.Username(), Password: password}
	}
	dialer, err := proxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{Timeout: socksConnectTimeout})
	if err != nil {
		return err
	}
	cd, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return errors.New("socks dialer lacks context support")
	}
	transport.DialContext = cd.DialContext
	return nil
}

func (f *HTTPFetcher) Fetch(ctx context.Context, cred Credential) (Snapshot, error) {
	provider := strings.ToLower(strings.TrimSpace(cred.Type))
	if (provider != "claude" && provider != "codex") || cred.Disabled || strings.TrimSpace(cred.AccessToken) == "" {
		return Snapshot{}, &Error{Code: "ineligible_credential"}
	}
	// Custom provider endpoints are not supported by the usage parsers.
	if cred.BaseURL != "" {
		return Snapshot{}, &Error{Code: "custom_transport_unsupported"}
	}
	client, err := f.clientFor(cred.ProxyURL)
	if err != nil {
		return Snapshot{}, err
	}
	url := ClaudeUsageURL
	if f.ClaudeURL != "" {
		url = f.ClaudeURL
	}
	if provider == "codex" {
		url = CodexUsageURL
		if f.CodexURL != "" {
			url = f.CodexURL
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Snapshot{}, &Error{Code: "quota_request_failed"}
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("Accept", "application/json")
	if provider == "claude" {
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		req.Header.Set("User-Agent", claudeUserAgent)
	} else {
		req.Header.Set("User-Agent", "codex_cli_rs/0.76.0")
		if strings.TrimSpace(cred.AccountID) != "" {
			req.Header.Set("Chatgpt-Account-Id", cred.AccountID)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Snapshot{}, &Error{Code: "quota_canceled"}
		}
		return Snapshot{}, &Error{Code: "quota_transport_failed"}
	}
	defer resp.Body.Close()
	now := f.Now().UTC()
	if resp.StatusCode != http.StatusOK {
		e := &Error{Code: fmt.Sprintf("quota_http_%d", resp.StatusCode), Status: resp.StatusCode}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			e.RetryAfter = RetryAfter(resp.Header.Get("Retry-After"), now)
		}
		return Snapshot{}, e
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUsageBytes+1))
	if err != nil || len(body) > maxUsageBytes {
		return Snapshot{}, &Error{Code: "invalid_quota_body"}
	}
	var snap Snapshot
	if provider == "claude" {
		snap, err = ParseClaudeUsage(body, now)
	} else {
		snap, err = ParseCodexUsage(body, now)
	}
	if err != nil {
		return Snapshot{}, &Error{Code: err.Error()}
	}
	snap.PolledAt = now
	snap.Provider = provider
	snap.Identity = cred.Identity()
	return snap, nil
}

// RetryAfter parses a Retry-After header, capped at one hour.
func RetryAfter(raw string, now time.Time) time.Duration {
	delay := time.Duration(0)
	if seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
		if seconds > 3600 {
			return time.Hour
		}
		if seconds > 0 {
			delay = time.Duration(seconds) * time.Second
		}
	} else if deadline, err := http.ParseTime(raw); err == nil {
		delay = deadline.Sub(now)
	}
	return min(time.Hour, max(0, delay))
}

// IsError unwraps a sanitized poll error.
func IsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
