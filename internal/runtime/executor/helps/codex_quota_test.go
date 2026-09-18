package helps

import (
	"context"
	"net/http"
	"strings"
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

func TestParseCodexQuotaEventHeadersPreservesAdditionalLimitsAndCredits(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"plan_type":"pro",
		"rate_limits":{
			"allowed":true,
			"limit_reached":false,
			"primary":{"used_percent":48,"window_minutes":10080,"reset_after_seconds":523210,"reset_at":1786677299},
			"secondary":null
		},
		"additional_rate_limits":{
			"GPT-5.3-Codex-Spark":{
				"allowed":true,
				"limit_reached":false,
				"primary":{"used_percent":3,"window_minutes":300,"reset_after_seconds":10148,"reset_at":1787231961},
				"secondary":{"used_percent":63,"window_minutes":10080,"reset_after_seconds":75420,"reset_at":1787290791}
			}
		},
		"credits":{"has_credits":false,"unlimited":false,"balance":"0"}
	}`))
	for key, want := range map[string]string{
		"X-Codex-Primary-Used-Percent":                                  "48",
		"X-Codex-Primary-Window-Minutes":                                "10080",
		"X-Codex-Primary-Reset-After-Seconds":                           "523210",
		"X-Codex-Additional-GPT-5.3-Codex-Spark-Limit-Name":             "GPT-5.3-Codex-Spark",
		"X-Codex-Additional-GPT-5.3-Codex-Spark-Primary-Used-Percent":   "3",
		"X-Codex-Additional-GPT-5.3-Codex-Spark-Primary-Window-Minutes": "300",
		"X-Codex-Additional-GPT-5.3-Codex-Spark-Secondary-Used-Percent": "63",
		"X-Codex-Credits-Has-Credits":                                   "false",
		"X-Codex-Credits-Unlimited":                                     "false",
		"X-Codex-Credits-Balance":                                       "0",
	} {
		if got := headers.Get(key); got != want {
			t.Fatalf("header %s = %q, want %q; all headers = %#v", key, got, want, headers)
		}
	}
}

func TestParseCodexQuotaEventHeadersReadsQuotaHeadersFromErrorFrames(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"error",
		"status_code":429,
		"headers":{
			"X-Codex-Primary-Used-Percent":"100",
			"X-Codex-Primary-Window-Minutes":"10080",
			"X-Codex-Primary-Reset-After-Seconds":"437380",
			"X-Codex-Credits-Balance":"0",
			"X-Codex-Turn-State":"must-not-be-observed",
			"Set-Cookie":"must-not-be-observed"
		}
	}`))
	if headers.Get("X-Codex-Primary-Used-Percent") != "100" ||
		headers.Get("X-Codex-Primary-Reset-After-Seconds") != "437380" ||
		headers.Get("X-Codex-Credits-Balance") != "0" {
		t.Fatalf("unexpected error quota headers: %#v", headers)
	}
	if headers.Get("Set-Cookie") != "" || headers.Get("X-Codex-Turn-State") != "" {
		t.Fatalf("non-quota error header leaked into quota observation: %#v", headers)
	}
}

func TestParseCodexQuotaEventHeadersDoesNotInventActiveLimitAndRejectsIncompleteWindows(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"rate_limits":{"primary":{"used_percent":42,"window_minutes":300,"reset_after_seconds":60}}
	}`))
	if headers.Get("X-Codex-Active-Limit") != "" || headers.Get("X-Codex-Primary-Reset-After-Seconds") != "60" {
		t.Fatalf("unexpected quota headers: %#v", headers)
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

func TestParseCodexQuotaEventHeadersSupportsAdditionalArrayAndCamelCase(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"planType":"pro",
		"meteredLimitName":"premium",
		"rateLimit":{
			"allowed":true,
			"limitReached":false,
			"primary":{"usedPercent":71,"windowMinutes":10080,"resetAfterSeconds":60}
		},
		"additionalRateLimits":[{
			"limitName":"GPT-5.3-Codex-Spark",
			"rateLimit":{
				"primary":{"usedPercent":13,"windowMinutes":300,"resetAt":1787399817},
				"secondary":{"usedPercent":51,"windowMinutes":10080,"resetAfterSeconds":120}
			}
		}]
	}`))
	for key, want := range map[string]string{
		"X-Codex-Active-Limit":                                                 "premium",
		"X-Codex-Plan-Type":                                                    "pro",
		"X-Codex-Primary-Used-Percent":                                         "71",
		"X-Codex-Additional-GPT-5.3-Codex-Spark-Limit-Name":                    "GPT-5.3-Codex-Spark",
		"X-Codex-Additional-GPT-5.3-Codex-Spark-Primary-Used-Percent":          "13",
		"X-Codex-Additional-GPT-5.3-Codex-Spark-Secondary-Reset-After-Seconds": "120",
	} {
		if got := headers.Get(key); got != want {
			t.Fatalf("header %s = %q, want %q; all headers = %#v", key, got, want, headers)
		}
	}
}

func TestParseCodexQuotaEventHeadersKeepsOnlySafeErrorHeaders(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"error",
		"headers":{
			"X-Ratelimit-Remaining-Requests":"7",
			"Retry-After":"60",
			"X-Codex-Primary-Over-Secondary-Limit-Percent":"0",
			"X-Codex-Turn-State":"secret",
			"Authorization":"Bearer secret"
		}
	}`))
	for key, want := range map[string]string{
		"X-Ratelimit-Remaining-Requests":               "7",
		"Retry-After":                                  "60",
		"X-Codex-Primary-Over-Secondary-Limit-Percent": "0",
	} {
		if got := headers.Get(key); got != want {
			t.Fatalf("header %s = %q, want %q", key, got, want)
		}
	}
	if headers.Get("X-Codex-Turn-State") != "" || headers.Get("Authorization") != "" {
		t.Fatalf("unsafe error headers leaked: %#v", headers)
	}
}

func TestParseCodexQuotaEventHeadersNonQuotaEventDoesNotAllocate(t *testing.T) {
	payload := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	allocs := testing.AllocsPerRun(1000, func() {
		if headers := ParseCodexQuotaEventHeaders(payload); headers != nil {
			t.Fatalf("non-quota event produced headers: %#v", headers)
		}
	})
	if allocs != 0 {
		t.Fatalf("non-quota event allocations = %v, want 0", allocs)
	}
}

// Large non-quota frames must stay allocation-free: only the bounded frame
// prefix is scanned for the type discriminator.
func TestParseCodexQuotaEventHeadersLargeNonQuotaFrameDoesNotAllocate(t *testing.T) {
	big := strings.Repeat("x", 4096)
	payload := []byte(`{"sequence_number":7,"type":"response.output_text.delta","delta":"` + big + `"}`)
	allocs := testing.AllocsPerRun(100, func() {
		if headers := ParseCodexQuotaEventHeaders(payload); headers != nil {
			t.Fatalf("non-quota event produced headers: %#v", headers)
		}
	})
	if allocs != 0 {
		t.Fatalf("large non-quota frame allocations = %v, want 0", allocs)
	}
}

// Quota markers live in the frame prefix, so large quota events must still be parsed.
func TestParseCodexQuotaEventHeadersLargeRateLimitsFrameIsParsed(t *testing.T) {
	padding := strings.Repeat("p", 4096)
	payload := []byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":42,"limit_percent":100,"window_minutes":10080,"reset_after_seconds":3600,"used_minutes":120},"secondary":{"used_percent":1,"limit_percent":100,"window_minutes":300,"reset_after_seconds":60,"used_minutes":2},"active_limit":"primary","credits":{"total_credits":"10","available_credits":"5","pct_remaining":"50"}},"padding":"` + padding + `"}`)
	headers := ParseCodexQuotaEventHeaders(payload)
	if headers == nil {
		t.Fatal("large rate_limits frame produced no headers")
	}
	if got := headers.Get("X-Codex-Primary-Used-Percent"); got != "42" {
		t.Fatalf("X-Codex-Primary-Used-Percent = %q, want 42", got)
	}
}

func TestParseCodexQuotaEventHeadersRejectsInvalidAndEmptyEvents(t *testing.T) {
	for _, payload := range []string{
		``,
		`{"type":"response.completed"}`,
		`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":101,"window_minutes":300,"reset_after_seconds":60}}}`,
	} {
		if headers := ParseCodexQuotaEventHeaders([]byte(payload)); headers != nil {
			t.Fatalf("payload %q produced unexpected headers: %#v", payload, headers)
		}
	}
}

// A malformed active-limit name must not discard the window watermarks that were
// parsed successfully from the same event.
func TestParseCodexQuotaEventHeadersKeepsWindowsWhenActiveLimitIsInvalid(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"metered_limit_name":"bad limit",
		"rate_limits":{"primary":{"used_percent":1,"window_minutes":300,"reset_after_seconds":60}}
	}`))
	if headers == nil {
		t.Fatal("valid window watermarks were discarded")
	}
	if got := headers.Get("X-Codex-Primary-Used-Percent"); got != "1" {
		t.Fatalf("X-Codex-Primary-Used-Percent = %q, want \"1\"", got)
	}
	if got := headers.Get("X-Codex-Active-Limit"); got != "" {
		t.Fatalf("malformed active limit was retained: %q", got)
	}
}

// Real websocket events namespace additional limits by limit name and carry a
// code_review_rate_limits sibling; both must survive parsing.
func TestParseCodexQuotaEventHeadersObservedProductionEvent(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"plan_type":"pro",
		"rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":81,"window_minutes":10080,"reset_after_seconds":137160,"reset_at":1787588999},"secondary":null},
		"code_review_rate_limits":{"primary":{"used_percent":4,"window_minutes":300,"reset_after_seconds":900}},
		"additional_rate_limits":{"GPT-5.3-Codex-Spark":{"allowed":true,"limit_reached":false,"primary":{"used_percent":0,"window_minutes":300,"reset_after_seconds":18000}}},
		"credits":{"has_credits":false,"unlimited":false,"balance":"0"},
		"promo":null
	}`))
	if headers == nil {
		t.Fatal("observed production event produced no headers")
	}
	for key, want := range map[string]string{
		"X-Codex-Plan-Type":                                           "pro",
		"X-Codex-Primary-Used-Percent":                                "81",
		"X-Codex-Primary-Reset-At":                                    "1787588999",
		"X-Codex-Code-Review-Primary-Used-Percent":                    "4",
		"X-Codex-Additional-Gpt-5.3-Codex-Spark-Limit-Name":           "GPT-5.3-Codex-Spark",
		"X-Codex-Additional-Gpt-5.3-Codex-Spark-Primary-Used-Percent": "0",
		"X-Codex-Credits-Balance":                                     "0",
		"X-Codex-Credits-Has-Credits":                                 "false",
	} {
		if got := headers.Get(key); got != want {
			t.Fatalf("header %s = %q, want %q", key, got, want)
		}
	}
	// secondary is null upstream and must not be invented.
	if got := headers.Get("X-Codex-Secondary-Used-Percent"); got != "" {
		t.Fatalf("null secondary window was materialized: %q", got)
	}
}

// An upstream-controlled limit name containing control characters must not reach
// the plain-text request log.
func TestParseCodexQuotaEventHeadersRejectsControlCharactersInLimitName(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"additional_rate_limits":[{"limit_name":"evil\r\nX-Injected: 1","primary":{"used_percent":3,"window_minutes":300,"reset_after_seconds":60}}]
	}`))
	if headers == nil {
		t.Fatal("expected window watermarks to survive")
	}
	for key, values := range headers {
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				t.Fatalf("header %s carried control characters: %q", key, value)
			}
		}
	}
}

// Codex quota parsing must stay on the codex-specific websocket entry point.
// The generic entry point is shared with other providers (xAI), whose frames
// must never be reinterpreted as codex quota data: an xAI error frame really
// does carry x-ratelimit-* headers, and merging them here would forge codex
// quota headers into an unrelated provider's request log.
func TestGenericWebsocketEntryPointDoesNotObserveCodexQuota(t *testing.T) {
	frame := []byte(`{"type":"error","headers":{"x-ratelimit-remaining-requests":"0","retry-after":"60"}}`)

	genericCtx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(genericCtx, http.Header{"X-Request-Id": []string{"xai-1"}})
	AppendAPIWebsocketResponse(genericCtx, nil, frame)
	generic := internallogging.GetResponseHeaders(genericCtx)
	if got := generic.Get("X-Ratelimit-Remaining-Requests"); got != "" {
		t.Fatalf("generic websocket entry point observed codex quota: %q", got)
	}
	if got := generic.Get("Retry-After"); got != "" {
		t.Fatalf("generic websocket entry point observed retry-after: %q", got)
	}
	if got := generic.Get("X-Request-Id"); got != "xai-1" {
		t.Fatalf("generic websocket entry point disturbed response headers: %q", got)
	}

	codexCtx := internallogging.WithResponseHeadersHolder(context.Background())
	AppendCodexAPIWebsocketResponse(codexCtx, nil, frame)
	if got := internallogging.GetResponseHeaders(codexCtx).Get("X-Ratelimit-Remaining-Requests"); got != "0" {
		t.Fatalf("codex websocket entry point lost quota observation: %q", got)
	}
}

func TestMergeWebsocketQuotaHeaders(t *testing.T) {
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{"X-Request-Id": []string{"req-1"}})
	internallogging.MergeResponseHeaders(ctx, ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"rate_limits":{"primary":{"used_percent":2,"window_minutes":10080,"reset_at":1782951970}},
		"metered_limit_name":"codex_bengalfox"
	}`)))
	headers := internallogging.GetResponseHeaders(ctx)
	if headers.Get("X-Request-Id") != "req-1" || headers.Get("X-Codex-Active-Limit") != "codex_bengalfox" {
		t.Fatalf("merged response headers = %#v", headers)
	}
}
