package executor

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const claudeCCH21220BaseBody = `{"model":"model-a","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.test; cc_entrypoint=sdk-cli; cch=00000;"},{"type":"text","text":"system-x"}],"tools":[],"metadata":{"user_id":"meta-x"},"max_tokens":1,"thinking":{"type":"adaptive","display":"omitted"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"output_config":{"effort":"high"},"stream":true}`

func TestSignAnthropicMessagesBody_ClaudeCode21220KnownVectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "base", body: claudeCCH21220BaseBody, want: "7ee87"},
		{name: "model value ignored", body: strings.Replace(claudeCCH21220BaseBody, `"model":"model-a"`, `"model":"model-b"`, 1), want: "7ee87"},
		{name: "max tokens ignored", body: strings.Replace(claudeCCH21220BaseBody, `"max_tokens":1`, `"max_tokens":2`, 1), want: "7ee87"},
		{name: "message changes hash", body: strings.Replace(claudeCCH21220BaseBody, `"text":"x"`, `"text":"y"`, 1), want: "b9cc8"},
		{name: "system changes hash", body: strings.Replace(claudeCCH21220BaseBody, `"system-x"`, `"system-y"`, 1), want: "a30d3"},
		{name: "metadata changes hash", body: strings.Replace(claudeCCH21220BaseBody, `"user_id":"meta-x"`, `"user_id":"meta-y"`, 1), want: "7a89d"},
		{name: "thinking changes hash", body: strings.Replace(claudeCCH21220BaseBody, `"thinking":{"type":"adaptive","display":"omitted"}`, `"thinking":{"type":"disabled"}`, 1), want: "7205c"},
		{name: "context changes hash", body: strings.Replace(claudeCCH21220BaseBody, `"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`, `"context_management":{"edits":[]}`, 1), want: "05073"},
		{name: "effort changes hash", body: strings.Replace(claudeCCH21220BaseBody, `"effort":"high"`, `"effort":"low"`, 1), want: "12366"},
		{name: "stream changes hash", body: strings.Replace(claudeCCH21220BaseBody, `"stream":true`, `"stream":false`, 1), want: "60400"},
		{name: "tool changes hash", body: strings.Replace(claudeCCH21220BaseBody, `"tools":[]`, `"tools":[{"name":"t","description":"d","input_schema":{"type":"object"}}]`, 1), want: "3d78d"},
		{name: "extra field changes hash", body: strings.Replace(claudeCCH21220BaseBody, `"stream":true}`, `"stream":true,"extra_top":"extra"}`, 1), want: "2d622"},
		{
			name: "field order remains significant",
			body: `{"stream":true,"output_config":{"effort":"high"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"thinking":{"type":"adaptive","display":"omitted"},"max_tokens":1,"metadata":{"user_id":"meta-x"},"tools":[],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.test; cc_entrypoint=sdk-cli; cch=00000;"},{"type":"text","text":"system-x"}],"messages":[{"role":"user","content":[{"type":"text","text":"x"}]}],"model":"model-a"}`,
			want: "e5b6c",
		},
		{name: "nested model value ignored", body: strings.Replace(claudeCCH21220BaseBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","model":"a"}`, 1), want: "0601b"},
		{name: "nested max tokens member omitted", body: strings.Replace(claudeCCH21220BaseBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","max_tokens":2}`, 1), want: "7ee87"},
		{name: "top level fallbacks member omitted", body: strings.Replace(claudeCCH21220BaseBody, `"stream":true}`, `"stream":true,"fallbacks":[{"model":"fallback-a"}]}`, 1), want: "7ee87"},
		{name: "nested fallbacks member omitted", body: strings.Replace(claudeCCH21220BaseBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","fallbacks":[{"model":"nested-a"}]}`, 1), want: "7ee87"},
		{name: "top level fallback credit token omitted", body: strings.Replace(claudeCCH21220BaseBody, `"stream":true}`, `"stream":true,"fallback_credit_token":"a"}`, 1), want: "7ee87"},
		{name: "nested fallback credit token omitted", body: strings.Replace(claudeCCH21220BaseBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","fallback_credit_token":"a"}`, 1), want: "7ee87"},
		{name: "trailing dispatch run keeps native comma", body: strings.Replace(claudeCCH21220BaseBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","max_tokens":999,"fallbacks":[{"model":"fallback-model"}]}`, 1), want: "4589b"},
		{name: "model before trailing dispatch run", body: strings.Replace(claudeCCH21220BaseBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","model":"nested-model","max_tokens":999,"fallbacks":[{"model":"fallback-model"}],"fallback_credit_token":"not-a-real-token"}`, 1), want: "2d312"},
		{name: "model splits dispatch runs", body: strings.Replace(claudeCCH21220BaseBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","max_tokens":999,"model":"nested-model","fallbacks":[{"model":"fallback-model"}]}`, 1), want: "0601b"},
		{name: "ordinary nested member remains", body: strings.Replace(claudeCCH21220BaseBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","plain":"a"}`, 1), want: "8d74c"},
		{name: "billing block only", body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.test; cc_entrypoint=sdk-cli; cch=00000;"}]}`, want: "f2edb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			signed, err := signAnthropicMessagesBody([]byte(tt.body))
			if err != nil {
				t.Fatalf("signAnthropicMessagesBody() error = %v", err)
			}
			if got := claudeCCHFromBody(t, signed); got != tt.want {
				t.Fatalf("cch = %q, want %q\nbody: %s", got, tt.want, signed)
			}
		})
	}
}

func TestSignAnthropicMessagesBody_PreservesFinalSerializedBytes(t *testing.T) {
	t.Parallel()

	literal := "keep literal cch=00000; in the message"
	body := []byte(strings.Replace(claudeCCH21220BaseBody, `"text":"x"`, `"text":"`+literal+`"`, 1))
	signed, err := signAnthropicMessagesBody(body)
	if err != nil {
		t.Fatalf("signAnthropicMessagesBody() error = %v", err)
	}
	if got := gjson.GetBytes(signed, "messages.0.content.0.text").String(); got != literal {
		t.Fatalf("message text = %q, want %q", got, literal)
	}

	cchOffset, ok := claudeBillingCCHDigitsOffset(signed)
	if !ok {
		t.Fatal("signed billing CCH not found")
	}
	unsigned := bytes.Clone(signed)
	copy(unsigned[cchOffset:cchOffset+claudeCCHLength], "00000")
	if !bytes.Equal(unsigned, body) {
		t.Fatalf("signing changed bytes outside CCH\n got: %s\nwant: %s", unsigned, body)
	}
}

func TestFinalizeAnthropicMessagesBodyCCH_InsertsMissingPlaceholder(t *testing.T) {
	t.Parallel()

	body := []byte(strings.Replace(claudeCCH21220BaseBody, " cch=00000;", "", 1))
	signed, err := finalizeAnthropicMessagesBodyCCH(body, "")
	if err != nil {
		t.Fatalf("finalizeAnthropicMessagesBodyCCH() error = %v", err)
	}
	if got := claudeCCHFromBody(t, signed); got != "7ee87" {
		t.Fatalf("cch = %q, want %q", got, "7ee87")
	}
	billing := gjson.GetBytes(signed, "system.0.text").String()
	if !strings.Contains(billing, "cc_entrypoint=sdk-cli; cch=7ee87;") {
		t.Fatalf("billing header = %q, want CCH after entrypoint", billing)
	}
}

func TestFinalizeAnthropicMessagesBodyCCH_AddsMissingBillingBlock(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"claude-opus-4-6","system":"keep this system text","messages":[{"role":"user","content":"hello"}],"max_tokens":128}`)
	fallback := "x-anthropic-billing-header: cc_version=2.1.220.test; cc_entrypoint=sdk-cli; cch=00000;"
	signed, err := finalizeAnthropicMessagesBodyCCH(body, fallback)
	if err != nil {
		t.Fatalf("finalizeAnthropicMessagesBodyCCH() error = %v", err)
	}
	if got := gjson.GetBytes(signed, "system.0.text").String(); !strings.HasPrefix(got, "x-anthropic-billing-header:") {
		t.Fatalf("system.0.text = %q, want billing block", got)
	}
	if got := gjson.GetBytes(signed, "system.1.text").String(); got != "keep this system text" {
		t.Fatalf("system.1.text = %q, want preserved system text", got)
	}
	if _, ok := claudeBillingCCHDigitsOffset(signed); !ok {
		t.Fatalf("generated billing block is missing CCH: %s", signed)
	}
}

func TestClaudeCCHSigningEnabled(t *testing.T) {
	t.Parallel()

	const (
		anthropicOrigin = "https://api.anthropic.com/v1/messages?beta=true"
		gatewayOrigin   = "https://gateway.example/v1/messages?beta=true"
	)

	tests := []struct {
		name           string
		apiKey         string
		kind           claudeCCHUpstreamKind
		cliFingerprint bool
		origin         string
		want           bool
	}{
		{name: "official API key default", apiKey: "key-123", kind: claudeCCHUpstreamAnthropic, origin: anthropicOrigin, want: false},
		{name: "official API key opt-in", apiKey: "key-123", kind: claudeCCHUpstreamAnthropic, cliFingerprint: true, origin: anthropicOrigin, want: true},
		{name: "Kimi API key default", apiKey: "key-123", kind: claudeCCHUpstreamAnthropic, origin: "https://api.kimi.com/v1/messages", want: false},
		// Native emits cch only for firstParty on api.anthropic.com or for vertex, so an
		// opted-in key on any other gateway keeps a cache-stable billing header.
		{name: "Kimi API key opt-in", apiKey: "key-123", kind: claudeCCHUpstreamAnthropic, cliFingerprint: true, origin: "https://api.kimi.com/v1/messages", want: false},
		{name: "gateway API key opt-in", apiKey: "key-123", kind: claudeCCHUpstreamAnthropic, cliFingerprint: true, origin: gatewayOrigin, want: false},
		{name: "anthropic host over http opt-in", apiKey: "key-123", kind: claudeCCHUpstreamAnthropic, cliFingerprint: true, origin: "http://api.anthropic.com/v1/messages", want: false},
		{name: "anthropic host explicit 443 opt-in", apiKey: "key-123", kind: claudeCCHUpstreamAnthropic, cliFingerprint: true, origin: "https://api.anthropic.com:443/v1/messages", want: true},
		{name: "anthropic lookalike host opt-in", apiKey: "key-123", kind: claudeCCHUpstreamAnthropic, cliFingerprint: true, origin: "https://api.anthropic.com.evil.example/v1/messages", want: false},
		// A real OAuth credential signs on every upstream: CPA is the hop that has to
		// regenerate the cch a downstream Claude Code could not produce.
		{name: "Claude OAuth", apiKey: "sk-ant-oat-custom", kind: claudeCCHUpstreamAnthropic, origin: anthropicOrigin, want: true},
		{name: "Claude OAuth custom gateway", apiKey: "sk-ant-oat-custom", kind: claudeCCHUpstreamAnthropic, origin: gatewayOrigin, want: true},
		{name: "other provider Claude OAuth", apiKey: "sk-ant-oat-other", kind: claudeCCHUpstreamOther, origin: gatewayOrigin, want: true},
		{name: "Vertex provider API key", apiKey: "key-123", kind: claudeCCHUpstreamVertex, origin: "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/l/publishers/anthropic/models/m:streamRawPredict", want: true},
		{name: "other provider API key", apiKey: "key-123", kind: claudeCCHUpstreamOther, origin: gatewayOrigin, want: false},
		{name: "other provider API key opt-in", apiKey: "key-123", kind: claudeCCHUpstreamOther, cliFingerprint: true, origin: gatewayOrigin, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := claudeCCHSigningEnabled(tt.apiKey, tt.kind, tt.cliFingerprint, tt.origin); got != tt.want {
				t.Fatalf("claudeCCHSigningEnabled() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestNormalizeClaudeCCHInput_PreservesRawJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "model string becomes empty", body: `{"model":"claude","keep":1}`, want: `{"model":"","keep":1}`},
		{name: "excluded first member", body: `{"max_tokens":1,"keep":2}`, want: `{"keep":2}`},
		{name: "excluded middle member", body: `{"keep":1,"fallbacks":[{"model":"x"}],"tail":2}`, want: `{"keep":1,"tail":2}`},
		{name: "excluded last member", body: `{"keep":1,"fallback_credit_token":"secret"}`, want: `{"keep":1}`},
		{name: "all members excluded", body: `{"max_tokens":1,"fallbacks":[],"fallback_credit_token":"secret"}`, want: `{}`},
		{name: "adjacent excluded members", body: `{"keep":1,"max_tokens":1,"fallbacks":[],"tail":2}`, want: `{"keep":1,"tail":2}`},
		{name: "native trailing dispatch run", body: `{"keep":1,"max_tokens":1,"fallbacks":[]}`, want: `{"keep":1,}`},
		{name: "nested fields", body: `{"outer":{"model":"x","max_tokens":1,"keep":"y"}}`, want: `{"outer":{"model":"","keep":"y"}}`},
		{name: "escaped key text stays inside string", body: `{"text":"literal \"model\":\"x\" and \"max_tokens\":1"}`, want: `{"text":"literal \"model\":\"x\" and \"max_tokens\":1"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeClaudeCCHInput([]byte(tt.body))
			if err != nil {
				t.Fatalf("normalizeClaudeCCHInput() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("normalized body = %s, want %s", got, tt.want)
			}
		})
	}
}

func claudeCCHFromBody(t *testing.T, body []byte) string {
	t.Helper()

	offset, ok := claudeBillingCCHDigitsOffset(body)
	if !ok {
		t.Fatalf("billing CCH not found in body: %s", body)
	}
	return string(body[offset : offset+claudeCCHLength])
}
