package llmreqlog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// configuredProxyURL mirrors CPA config proxy-url for exit-IP probing.
var configuredProxyURL atomic.Value // string

// SetProxyURL updates the proxy used to probe the real CPA egress IP.
func SetProxyURL(proxyURL string) {
	configuredProxyURL.Store(strings.TrimSpace(proxyURL))
}

func currentProxyURL() string {
	if raw := configuredProxyURL.Load(); raw != nil {
		if value, ok := raw.(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(os.Getenv("CPA_PROXY_URL"))
}

func init() {
	coreusage.RegisterNamedPlugin("llm-request-log", &usagePlugin{})
}

type usagePlugin struct{}

func (p *usagePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)

	// Prefer upstream/response model, then alias.
	modelName := firstNonEmpty(record.Model, record.Alias, "unknown")
	if headerModel := headerFirst(record.ResponseHeaders, "X-Model", "x-model", "X-Request-Model"); headerModel != "" {
		modelName = headerModel
	}

	thinkingLevel := strings.TrimSpace(record.ReasoningEffort)
	if thinkingLevel == "" {
		thinkingLevel = coreusage.ReasoningEffortFromContext(ctx)
	}
	if thinkingLevel == "" {
		thinkingLevel = headerFirst(record.ResponseHeaders, "X-Reasoning-Effort", "x-reasoning-effort")
	}

	// Prefer client-facing streamed think/thinking text length.
	// Do NOT use usage reasoning/thought token counters (those are "thought").
	thinkingLen := int64(0)
	hasThinking := false
	if has, length, ok := thinkStatsFromContext(ctx); ok {
		hasThinking = has
		thinkingLen = length
	} else {
		flag := headerFirst(record.ResponseHeaders, "X-Has-Thinking", "x-has-thinking")
		if strings.EqualFold(flag, "true") || flag == "1" {
			hasThinking = true
		}
	}

	failed := record.Failed
	statusCode := record.Fail.StatusCode
	if statusCode <= 0 {
		statusCode = internallogging.GetResponseStatus(ctx)
	}
	if !failed && statusCode >= 400 {
		failed = true
	}
	if statusCode <= 0 {
		if failed {
			statusCode = 500
		} else {
			statusCode = 200
		}
	}

	endpoint := strings.TrimSpace(internallogging.GetEndpoint(ctx))
	requestID := strings.TrimSpace(internallogging.GetRequestID(ctx))
	if requestID == "" {
		requestID = fmt.Sprintf("%d", timestamp.UnixNano())
	}

	reqType := "chat"
	if failed {
		reqType = "error"
	} else if endpoint != "" {
		reqType = endpoint
	} else if record.ExecutorType != "" {
		reqType = record.ExecutorType
	}

	entry := Entry{
		ID:               requestID + "-" + fmt.Sprintf("%d", timestamp.UnixNano()%1000000),
		Time:             timestamp,
		Token:            maskToken(record.APIKey),
		Group:            firstNonEmpty(record.Provider, record.AuthType, "default"),
		Type:             reqType,
		Model:            modelName,
		LatencyMs:        record.Latency.Milliseconds(),
		TTFTMs:           record.TTFT.Milliseconds(),
		PromptTokens:     detail.InputTokens,
		CompletionTokens: detail.OutputTokens,
		Cost:             0,
		ThinkingLevel:    thinkingLevel,
		HasThinking:      hasThinking,
		ThinkingLen:      thinkingLen,
		Failed:           failed,
		StatusCode:       statusCode,
		Provider:         record.Provider,
		Endpoint:         endpoint,
		RequestID:        requestID,
		AuthID:           record.AuthID,
		Source:           record.Source,
		Detail: map[string]any{
			"alias":                 record.Alias,
			"auth_type":             record.AuthType,
			"auth_index":            record.AuthIndex,
			"executor_type":         record.ExecutorType,
			"think_chars":           thinkingLen,
			"reasoning_tokens":      detail.ReasoningTokens,
			"cached_tokens":         detail.CachedTokens,
			"total_tokens":          detail.TotalTokens,
			"service_tier":          record.ServiceTier,
			"response_service_tier": record.ResponseServiceTier,
			"fail_body":             strings.TrimSpace(record.Fail.Body),
		},
	}
	defaultStore.add(entry)
	go enrichExitIP(entry.ID)
}

func enrichExitIP(entryID string) {
	proxyURL := currentProxyURL()
	exitIP := probeExitIPVia(proxyURL)
	exitNode := proxyHostLabel(proxyURL)
	// When CPA itself exits via Clash/Mihomo, enrich with the active leaf node name.
	if looksLikeClashProxy(proxyURL) {
		if node := latestClashNode(); node != "" {
			exitNode = node
		}
	}
	defaultStore.updateExit(entryID, exitIP, exitNode)
}

func proxyHostLabel(rawProxy string) string {
	rawProxy = strings.TrimSpace(rawProxy)
	if rawProxy == "" || strings.EqualFold(rawProxy, "direct") || strings.EqualFold(rawProxy, "none") {
		return "direct"
	}
	parsed, err := url.Parse(rawProxy)
	if err != nil || parsed.Host == "" {
		return rawProxy
	}
	return parsed.Host
}

func looksLikeClashProxy(rawProxy string) bool {
	host := strings.ToLower(proxyHostLabel(rawProxy))
	if host == "" || host == "direct" {
		return false
	}
	return strings.Contains(host, ":7890") ||
		strings.Contains(host, ":7891") ||
		strings.Contains(host, "clash") ||
		strings.Contains(host, "mihomo") ||
		strings.Contains(host, "glados")
}

func latestClashNode() string {
	client := &http.Client{Timeout: 2 * time.Second}
	for _, clashAPI := range []string{
		strings.TrimSpace(os.Getenv("CLASH_API")),
		"http://172.17.0.1:9090",
		"http://127.0.0.1:9090",
	} {
		if clashAPI == "" {
			continue
		}
		resp, err := client.Get(strings.TrimRight(clashAPI, "/") + "/connections")
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if err != nil {
			continue
		}
		var payload struct {
			Connections []struct {
				Start  time.Time `json:"start"`
				Chains []string  `json:"chains"`
			} `json:"connections"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			continue
		}
		var bestNode string
		var bestStart time.Time
		for _, conn := range payload.Connections {
			if len(conn.Chains) == 0 {
				continue
			}
			if bestNode == "" || conn.Start.After(bestStart) {
				bestStart = conn.Start
				bestNode = conn.Chains[0]
			}
		}
		return bestNode
	}
	return ""
}

func probeExitIPVia(rawProxy string) string {
	transport, mode, err := proxyutil.BuildHTTPTransport(rawProxy)
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: 3 * time.Second}
	switch mode {
	case proxyutil.ModeProxy:
		if transport == nil {
			return ""
		}
		client.Transport = transport
	case proxyutil.ModeDirect, proxyutil.ModeInherit:
		// Probe without proxy = host egress.
		client.Transport = http.DefaultTransport
	default:
		return ""
	}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	resp.Body.Close()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func headerFirst(headers http.Header, keys ...string) string {
	if headers == nil {
		return ""
	}
	for _, key := range keys {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func maskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return "-"
	}
	if len(token) <= 8 {
		return token[:1] + "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}
