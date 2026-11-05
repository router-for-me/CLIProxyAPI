package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	// BaseURL is the fixed Ollama Cloud origin. It is intentionally not
	// configurable: quota fetching must never be redirected to an arbitrary
	// host, which would turn the stored API key into an exfiltration vector.
	BaseURL = "https://" + Host

	// Host is the fixed Ollama Cloud hostname used for origin validation.
	Host = "ollama.com"

	usagePath   = "/api/usage"
	balancePath = "/api/balance"

	// MaxResponseBytes bounds upstream response bodies. Official usage and
	// balance payloads are small; anything larger is treated as malformed.
	MaxResponseBytes = 1 << 20 // 1 MiB

	// ProviderKey identifies the Ollama Cloud quota source in responses.
	ProviderKey = "ollama-cloud"

	// SourceName identifies the upstream data source in responses.
	SourceName = "ollama-official-api"
)

// UpstreamError is a sanitized failure of an Ollama Cloud call. Message is
// safe to surface to management clients: it never contains credential
// material or client-supplied values.
type UpstreamError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

// Error returns the sanitized message, including any retry-after hint.
func (e *UpstreamError) Error() string {
	return e.Message
}

// Client performs read-only Ollama Cloud account calls with a fixed origin.
type Client struct {
	HTTPClient *http.Client

	// EndpointBase overrides BaseURL. Production code must leave it empty so
	// every request targets the fixed ollama.com origin; tests set it to a
	// local test server. No management API path can influence this value, so
	// the SSRF/exfiltration guarantee is unaffected.
	EndpointBase string
}

// NewClient returns a client with the default bounded transport and the fixed
// origin.
func NewClient() *Client {
	transport, _, _ := proxyutil.BuildHTTPTransport("direct")
	return &Client{
		HTTPClient:   &http.Client{Transport: transport},
		EndpointBase: BaseURL,
	}
}

// endpoint resolves the request target for a path and query string.
func (c *Client) endpoint(pathAndQuery string) string {
	base := BaseURL
	if c.EndpointBase != "" {
		base = c.EndpointBase
	}
	return base + pathAndQuery
}

// FetchUsage calls GET /api/usage for the given range and scope.
func (c *Client) FetchUsage(ctx context.Context, apiKey string, rng UsageRange, scope UsageScope) (*UsageResponse, error) {
	if !rng.Valid() {
		return nil, fmt.Errorf("invalid usage range %q", rng)
	}
	if !scope.Valid() {
		return nil, fmt.Errorf("invalid usage scope %q", scope)
	}

	query := url.Values{}
	query.Set("range", string(rng))
	query.Set("scope", string(scope))

	var out UsageResponse
	if err := c.doJSON(ctx, apiKey, usagePath+"?"+query.Encode(), &out); err != nil {
		return nil, err
	}
	if out.Range != string(rng) || out.Scope != string(scope) || out.Buckets == nil ||
		(out.Granularity != "hour" && out.Granularity != "day") || out.From == "" || out.Until == "" || out.Totals.RequestCount < 0 {
		return nil, &UpstreamError{StatusCode: http.StatusBadGateway, Message: "Ollama Cloud returned invalid usage data"}
	}
	return &out, nil
}

// FetchBalance calls GET /api/balance.
func (c *Client) FetchBalance(ctx context.Context, apiKey string) (*BalanceResponse, error) {
	var out BalanceResponse
	if err := c.doJSON(ctx, apiKey, balancePath, &out); err != nil {
		return nil, err
	}
	if out.Purchased == nil && out.Included == nil && out.Legacy == nil {
		return nil, &UpstreamError{StatusCode: http.StatusBadGateway, Message: "Ollama Cloud returned invalid balance data"}
	}
	return &out, nil
}

// doJSON performs a GET against the fixed origin and decodes the JSON body.
func (c *Client) doJSON(ctx context.Context, apiKey string, pathAndQuery string, out any) error {
	endpoint := c.endpoint(pathAndQuery)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build ollama request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	// Never follow redirects, including redirects to subdomains that Go would
	// otherwise consider eligible to receive the Authorization header.
	baseClient := c.HTTPClient
	if baseClient == nil {
		baseClient = NewClient().HTTPClient
	}
	httpClient := *baseClient
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("ollama cloud request: %w", ctxErr)
		}
		return &UpstreamError{StatusCode: http.StatusBadGateway, Message: "failed to reach Ollama Cloud"}
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debug("ollama cloud response body close failed")
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("ollama cloud response: %w", ctxErr)
		}
		return &UpstreamError{StatusCode: http.StatusBadGateway, Message: "failed to read Ollama Cloud response"}
	}
	if len(body) > MaxResponseBytes {
		return &UpstreamError{StatusCode: http.StatusBadGateway, Message: "Ollama Cloud response is too large"}
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return upstreamStatusError(resp, body)
	}

	if err := validateDocument(body, out); err != nil {
		return &UpstreamError{StatusCode: http.StatusBadGateway, Message: "Ollama Cloud returned invalid quota data"}
	}
	// ojson validates the wire contract; the standard typed decoder binds only
	// the explicitly declared DTO fields (ojson has no struct decoder).
	if !ojson.ValidBytes(body) {
		return &UpstreamError{StatusCode: http.StatusBadGateway, Message: "Ollama Cloud returned an invalid response"}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &UpstreamError{StatusCode: http.StatusBadGateway, Message: "Ollama Cloud returned an invalid response"}
	}
	return nil
}

// upstreamStatusError maps status codes to fixed messages, ignoring response text.
func upstreamStatusError(resp *http.Response, _ []byte) *UpstreamError {
	// Even the official service may echo request credentials in an error.
	// Return only allowlisted messages, never upstream response text.
	message := "Ollama Cloud request failed"
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		message = "Ollama Cloud: Invalid credentials"
	case http.StatusForbidden:
		message = "Ollama Cloud: Account suspended or access denied"
	case http.StatusTooManyRequests:
		message = "Ollama Cloud: Too many requests"
	case http.StatusServiceUnavailable:
		message = "Ollama Cloud: temporarily unavailable"
	}

	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	if retryAfter > 0 {
		message = fmt.Sprintf("%s (retry after %ds)", message, int(retryAfter.Seconds()))
	}

	return &UpstreamError{StatusCode: resp.StatusCode, Message: message, RetryAfter: retryAfter}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return 0
	}
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

// IsUpstreamError reports whether err is a sanitized upstream failure.
func IsUpstreamError(err error) bool {
	var upstreamErr *UpstreamError
	return errors.As(err, &upstreamErr)
}
