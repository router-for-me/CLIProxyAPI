package helps

import (
	"context"
	"net/http"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestParseCodexQuotaEventHeadersPreservesActiveLimit(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"plan_type":"pro",
		"metered_limit_name":"codex_bengalfox",
		"rate_limits":{
			"primary":{"used_percent":2,"window_minutes":10080,"reset_at":1782951970},
			"secondary":null
		}
	}`))
	if headers.Get("X-Codex-Active-Limit") != "codex_bengalfox" ||
		headers.Get("X-Codex-Primary-Used-Percent") != "2" ||
		headers.Get("X-Codex-Primary-Window-Minutes") != "10080" ||
		headers.Get("X-Codex-Primary-Reset-At") != "1782951970" ||
		headers.Get("X-Codex-Plan-Type") != "pro" {
		t.Fatalf("unexpected quota headers: %#v", headers)
	}
}

func TestParseCodexQuotaEventHeadersDefaultsToCodexAndRejectsIncompleteWindows(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"rate_limits":{"primary":{"used_percent":42,"window_minutes":300,"reset_after_seconds":60}}
	}`))
	if headers.Get("X-Codex-Active-Limit") != "codex" || headers.Get("X-Codex-Primary-Reset-After-Seconds") != "60" {
		t.Fatalf("unexpected default quota headers: %#v", headers)
	}
	if got := ParseCodexQuotaEventHeaders([]byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":42}}}`)); got != nil {
		t.Fatalf("incomplete quota event produced headers: %#v", got)
	}

	headers = ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"rate_limits":{
			"primary":{"used_percent":42,"window_minutes":300},
			"secondary":{"used_percent":17,"window_minutes":10080,"reset_after_seconds":60}
		}
	}`))
	if headers.Get("X-Codex-Primary-Used-Percent") != "" ||
		headers.Get("X-Codex-Secondary-Used-Percent") != "17" {
		t.Fatalf("partially valid quota event produced incomplete headers: %#v", headers)
	}
}

func TestUsageReporterMergesWebsocketQuotaHeaders(t *testing.T) {
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{"X-Request-Id": []string{"req-1"}})
	reporter := NewUsageReporter(ctx, "codex", "gpt-5.3-codex-spark", nil)
	reporter.ObserveQuotaHeaders(http.Header{
		"X-Codex-Active-Limit":           []string{"codex_bengalfox"},
		"X-Codex-Primary-Used-Percent":   []string{"2"},
		"X-Codex-Primary-Window-Minutes": []string{"10080"},
		"X-Codex-Primary-Reset-At":       []string{"1782951970"},
	})
	headers := reporter.responseHeaders(ctx)
	if headers.Get("X-Request-Id") != "req-1" || headers.Get("X-Codex-Active-Limit") != "codex_bengalfox" {
		t.Fatalf("merged response headers = %#v", headers)
	}
}
