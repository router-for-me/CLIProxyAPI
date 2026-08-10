package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	xxHash64 "github.com/pierrec/xxHash/xxHash64"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// collectCacheControlTTLs returns the ttl value of every cache_control block in
// Anthropic evaluation order (tools → system → messages). Blocks without a ttl are
// reported as an empty string so callers can assert on default breakpoints too.
func collectCacheControlTTLs(t *testing.T, payload []byte) []string {
	t.Helper()

	ttls := make([]string, 0, 8)
	appendBlock := func(item gjson.Result) {
		cc := item.Get("cache_control")
		if !cc.Exists() {
			return
		}
		ttls = append(ttls, cc.Get("ttl").String())
	}

	gjson.GetBytes(payload, "tools").ForEach(func(_, item gjson.Result) bool {
		appendBlock(item)
		return true
	})
	gjson.GetBytes(payload, "system").ForEach(func(_, item gjson.Result) bool {
		appendBlock(item)
		return true
	})
	gjson.GetBytes(payload, "messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, item gjson.Result) bool {
			appendBlock(item)
			return true
		})
		return true
	})
	return ttls
}

func assertAllCacheControlTTLs(t *testing.T, payload []byte, want string) {
	t.Helper()

	ttls := collectCacheControlTTLs(t, payload)
	if len(ttls) == 0 {
		t.Fatalf("expected at least one cache_control block in body: %s", string(payload))
	}
	for i, got := range ttls {
		if got != want {
			t.Fatalf("cache_control[%d].ttl = %q, want %q\nbody: %s", i, got, want, string(payload))
		}
	}
}

func TestPromoteDefaultCacheControlTTL_PromotesToolsSystemAndMessages(t *testing.T) {
	payload := []byte(`{
		"tools": [{"name":"t1","cache_control":{"type":"ephemeral"}}],
		"system": [{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],
		"messages": [{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}}]}]
	}`)

	out := promoteDefaultCacheControlTTL(payload, "1h")

	assertAllCacheControlTTLs(t, out, "1h")
	if got := len(collectCacheControlTTLs(t, out)); got != 3 {
		t.Fatalf("cache_control block count = %d, want 3", got)
	}
	if got := countCacheControls(out); got != countCacheControls(payload) {
		t.Fatalf("breakpoint count changed: %d -> %d", countCacheControls(payload), got)
	}
}

func TestPromoteDefaultCacheControlTTL_PromotesInjectedBreakpoints(t *testing.T) {
	input := []byte(`{"model":"claude-3-5-sonnet","tools":[{"name":"t1","input_schema":{"type":"object"}},{"name":"t2","input_schema":{"type":"object"}}],"system":"long system prompt","messages":[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"assistant","content":[{"type":"text","text":"a1"}]},{"role":"user","content":[{"type":"text","text":"u2"}]}]}`)

	out := promoteDefaultCacheControlTTL(ensureCacheControl(input), "1h")

	assertAllCacheControlTTLs(t, out, "1h")
}

func TestPromoteDefaultCacheControlTTL_LeavesExistingOneHourBlocksUntouched(t *testing.T) {
	payload := []byte(`{"tools":[{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)

	out := promoteDefaultCacheControlTTL(payload, "1h")

	if !bytes.Equal(out, payload) {
		t.Fatalf("promoteDefaultCacheControlTTL altered bytes for already-1h blocks.\noriginal: %s\ngot:      %s", payload, out)
	}
}

func TestPromoteDefaultCacheControlTTL_PromotesExplicitFiveMinuteBlocks(t *testing.T) {
	payload := []byte(`{"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral","ttl":"5m"}}],"messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`)

	out := promoteDefaultCacheControlTTL(payload, "1h")

	assertAllCacheControlTTLs(t, out, "1h")
}

func TestPromoteDefaultCacheControlTTL_LeavesMalformedAndForeignBlocksUntouched(t *testing.T) {
	payload := []byte(`{"tools":[{"name":"t1","cache_control":"ephemeral"},{"name":"t2","cache_control":["ephemeral"]},{"name":"t3","cache_control":{"type":"persistent"}},{"name":"t4","cache_control":{"type":123}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral","ttl":"30m"}},{"type":"text","text":"s2","cache_control":{"type":"ephemeral","ttl":300}}],"messages":[{"role":"user","content":"plain string content"}]}`)

	out := promoteDefaultCacheControlTTL(payload, "1h")

	if !bytes.Equal(out, payload) {
		t.Fatalf("promoteDefaultCacheControlTTL altered malformed or foreign cache_control values.\noriginal: %s\ngot:      %s", payload, out)
	}
}

func TestPromoteDefaultCacheControlTTL_DisabledTTLKeepsPayloadByteIdentical(t *testing.T) {
	payload := []byte(`{"tools":[{"name":"t1","cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"<system-reminder>foo & bar</system-reminder>","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}}]}]}`)

	out := promoteDefaultCacheControlTTL(payload, "")

	if !bytes.Equal(out, payload) {
		t.Fatalf("disabled promotion must keep the payload byte-identical.\noriginal: %s\ngot:      %s", payload, out)
	}
}

func TestPromoteDefaultCacheControlTTL_NormalizeKeepsPromotedBlocks(t *testing.T) {
	payload := []byte(`{
		"tools": [{"name":"t1","cache_control":{"type":"ephemeral"}}],
		"system": [{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],
		"messages": [{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]
	}`)

	promoted := promoteDefaultCacheControlTTL(payload, "1h")
	out := normalizeCacheControlTTL(promoted)

	// Without promotion the leading default blocks would force the trailing 1h block
	// to be downgraded; after promotion the ordering constraint is already satisfied.
	assertAllCacheControlTTLs(t, out, "1h")
	if !bytes.Equal(out, promoted) {
		t.Fatalf("normalizeCacheControlTTL downgraded promoted blocks.\npromoted: %s\ngot:      %s", promoted, out)
	}
}

func TestPromoteDefaultCacheControlTTL_NormalizeStillRepairsOrderingAfterMalformedBlock(t *testing.T) {
	payload := []byte(`{
		"tools": [{"name":"t1","cache_control":"ephemeral"}],
		"system": [{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}]
	}`)

	out := normalizeCacheControlTTL(promoteDefaultCacheControlTTL(payload, "1h"))

	if got := gjson.GetBytes(out, "tools.0.cache_control").String(); got != "ephemeral" {
		t.Fatalf("malformed tools.0.cache_control = %q, want it untouched", got)
	}
	if gjson.GetBytes(out, "system.0.cache_control.ttl").Exists() {
		t.Fatalf("system.0.cache_control.ttl must be stripped after a malformed 5m-equivalent block: %s", string(out))
	}
}

func TestPromoteDefaultCacheControlTTL_AfterEnforceLimitKeepsMaxFourBreakpoints(t *testing.T) {
	payload := []byte(`{
		"tools": [
			{"name":"t1","cache_control":{"type":"ephemeral"}},
			{"name":"t2","cache_control":{"type":"ephemeral"}}
		],
		"system": [
			{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"s2","cache_control":{"type":"ephemeral"}}
		],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"u2","cache_control":{"type":"ephemeral"}}]}
		]
	}`)

	limited := enforceCacheControlLimit(payload, 4)
	if got := countCacheControls(limited); got != 4 {
		t.Fatalf("cache_control count after enforce = %d, want 4", got)
	}

	out := promoteDefaultCacheControlTTL(limited, "1h")

	if got := countCacheControls(out); got != 4 {
		t.Fatalf("cache_control count after promotion = %d, want 4", got)
	}
	assertAllCacheControlTTLs(t, out, "1h")
}

func TestClaudeCacheControlDefaultTTL_ResolvesSupportedValuesOnly(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{name: "nil config", cfg: nil, want: ""},
		{name: "unset", cfg: &config.Config{}, want: ""},
		{name: "one hour", cfg: &config.Config{CacheControlDefaultTTL: "1h"}, want: "1h"},
		{name: "five minutes", cfg: &config.Config{CacheControlDefaultTTL: "5m"}, want: "5m"},
		{name: "padded and uppercase", cfg: &config.Config{CacheControlDefaultTTL: "  1H "}, want: "1h"},
		{name: "unsupported duration", cfg: &config.Config{CacheControlDefaultTTL: "2h"}, want: ""},
		{name: "garbage", cfg: &config.Config{CacheControlDefaultTTL: "yes"}, want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeCacheControlDefaultTTL(tc.cfg); got != tc.want {
				t.Fatalf("claudeCacheControlDefaultTTL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeExecutor_ExecutePromotesCacheControlTTL(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{CacheControlDefaultTTL: "1h"})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if got := countCacheControls(seenBody); got > 4 {
		t.Fatalf("cache_control count = %d, want <= 4", got)
	}
	assertAllCacheControlTTLs(t, seenBody, "1h")
}

func TestClaudeExecutor_ExecuteStreamPromotesCacheControlTTL(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{CacheControlDefaultTTL: "1h"})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if got := countCacheControls(seenBody); got > 4 {
		t.Fatalf("cache_control count = %d, want <= 4", got)
	}
	assertAllCacheControlTTLs(t, seenBody, "1h")
}

func TestClaudeExecutor_CountTokensUpstreamPromotesCacheControlTTL(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":7}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{CacheControlDefaultTTL: "1h"})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"tools":[{"name":"t1","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`)

	_, err := executor.countTokensUpstream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("countTokensUpstream() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if got := countCacheControls(seenBody); got > 4 {
		t.Fatalf("cache_control count = %d, want <= 4", got)
	}
	assertAllCacheControlTTLs(t, seenBody, "1h")
}

func TestClaudeExecutor_ExecuteWithoutCacheControlDefaultTTLKeepsDefaultBreakpoints(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	assertAllCacheControlTTLs(t, seenBody, "")
}

func TestClaudeExecutor_CCHSigningCoversPromotedCacheControlTTL(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{
		CacheControlDefaultTTL: "1h",
		ClaudeKey: []config.ClaudeKey{{
			APIKey:                 "key-123",
			BaseURL:                server.URL,
			ExperimentalCCHSigning: true,
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	assertAllCacheControlTTLs(t, seenBody, "1h")

	billingPattern := regexp.MustCompile(`(x-anthropic-billing-header:[^"]*?\bcch=)([0-9a-f]{5})(;)`)
	match := billingPattern.FindSubmatch(seenBody)
	if match == nil {
		t.Fatalf("expected signed billing header in body: %s", string(seenBody))
	}
	actualCCH := string(match[2])
	unsignedBody := billingPattern.ReplaceAll(seenBody, []byte(`${1}00000${3}`))
	wantCCH := fmt.Sprintf("%05x", xxHash64.Checksum(unsignedBody, claudeCCHSeed)&0xFFFFF)
	if actualCCH != wantCCH {
		t.Fatalf("cch = %q, want %q — signature must cover the promoted cache_control TTL\nbody: %s", actualCCH, wantCCH, string(seenBody))
	}
}
