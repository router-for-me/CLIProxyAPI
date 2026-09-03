package helps

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func ttftReporter() *UsageReporter {
	return &UsageReporter{
		requestedAt: time.Now().Add(-5 * time.Second),
		ttft:        3 * time.Second,
		ttftSet:     true,
	}
}

func TestAttachTokensPerSecondExcludesTTFT(t *testing.T) {
	reporter := ttftReporter()
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
	reporter := ttftReporter()
	attachTokensPerSecondHeader(headers, []byte(`{"usage":{"completion_tokens":200}}`), reporter)
	got := headers.Get(tokensPerSecondHeader)
	parsed, err := strconv.ParseFloat(got, 64)
	if err != nil || parsed < 99 || parsed > 101 {
		t.Fatalf("header = %q, want ~100", got)
	}
}

func TestAttachTokensPerSecondFromUsageMetadata(t *testing.T) {
	reporter := ttftReporter()
	got := AttachTokensPerSecond([]byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":200,"totalTokenCount":210}}`), reporter)
	tps := gjson.GetBytes(got, "usageMetadata.tokens_per_second").Float()
	if tps < 99 || tps > 101 {
		t.Fatalf("usageMetadata.tokens_per_second = %v, want ~100; body=%s", tps, got)
	}
}

func TestAttachTokensPerSecondFromWrappedUsageMetadata(t *testing.T) {
	reporter := ttftReporter()
	got := AttachTokensPerSecond([]byte(`{"response":{"usageMetadata":{"candidatesTokenCount":200}}}`), reporter)
	tps := gjson.GetBytes(got, "response.usageMetadata.tokens_per_second").Float()
	if tps < 99 || tps > 101 {
		t.Fatalf("response.usageMetadata.tokens_per_second = %v, want ~100; body=%s", tps, got)
	}
}

func TestAttachStreamTokensPerSecondNamedSSEEvent(t *testing.T) {
	reporter := ttftReporter()
	line := []byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":10,\"output_tokens\":200}}\n")
	got := AttachStreamTokensPerSecond(line, reporter)
	if !bytes.Contains(got, []byte("event: message_delta")) {
		t.Fatalf("lost SSE event name: %s", got)
	}
	dataIdx := bytes.Index(got, []byte("data:"))
	if dataIdx < 0 {
		t.Fatalf("lost data line: %s", got)
	}
	payload := bytes.TrimSpace(got[dataIdx+len("data:"):])
	tps := gjson.GetBytes(payload, "usage.tokens_per_second").Float()
	if tps < 99 || tps > 101 {
		t.Fatalf("named SSE tokens_per_second = %v, want ~100; body=%s", tps, got)
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
