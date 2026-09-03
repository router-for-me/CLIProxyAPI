package helps

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestAttachTokensPerSecondExcludesTTFT(t *testing.T) {
	reporter := &UsageReporter{
		requestedAt: time.Now().Add(-5 * time.Second),
		ttft:        3 * time.Second,
		ttftSet:     true,
	}
	got := AttachTokensPerSecond([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":200,"total_tokens":210}}`), reporter)
	tps := gjson.GetBytes(got, "usage.tokens_per_second").Float()
	if tps < 99 || tps > 101 {
		t.Fatalf("tokens_per_second = %v, want ~100", tps)
	}
}

func TestAttachTokensPerSecondOmittedWithoutTTFT(t *testing.T) {
	reporter := &UsageReporter{requestedAt: time.Now().Add(-5 * time.Second)}
	raw := []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":200,"total_tokens":210}}`)
	got := AttachTokensPerSecond(raw, reporter)
	if gjson.GetBytes(got, "usage.tokens_per_second").Exists() {
		t.Fatalf("tok/s must be omitted without TTFT, got %s", got)
	}
}

func TestTokensPerSecondHeaderFromUsage(t *testing.T) {
	headers := http.Header{}
	reporter := &UsageReporter{
		requestedAt: time.Now().Add(-5 * time.Second),
		ttft:        3 * time.Second,
		ttftSet:     true,
	}
	attachTokensPerSecondHeader(headers, []byte(`{"usage":{"completion_tokens":200}}`), reporter)
	got := headers.Get(tokensPerSecondHeader)
	parsed, err := strconv.ParseFloat(got, 64)
	if err != nil || parsed < 99 || parsed > 101 {
		t.Fatalf("header = %q, want ~100", got)
	}
}

func TestUsageRecordTokensPerSecondJSON(t *testing.T) {
	record := usage.Record{
		Detail:          usage.Detail{OutputTokens: 50},
		Latency:         2 * time.Second,
		TTFT:            1 * time.Second,
		TokensPerSecond: usage.TokensPerSecond(50, 2*time.Second, 1*time.Second),
	}
	if record.TokensPerSecond != 50 {
		t.Fatalf("TokensPerSecond = %v, want 50", record.TokensPerSecond)
	}
	if _, err := json.Marshal(record); err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
}
