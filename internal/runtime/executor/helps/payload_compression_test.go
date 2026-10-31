package helps

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/payloadcompress"
	"github.com/tidwall/gjson"
)

func compressionConfig(pipeline ...string) payloadcompress.Config {
	steps := make([]payloadcompress.Step, 0, len(pipeline))
	for _, name := range pipeline {
		steps = append(steps, payloadcompress.Step{Engine: name})
	}
	return payloadcompress.Config{
		Enabled:      true,
		MinBytes:     1,
		DefaultCombo: "t",
		Combos:       []payloadcompress.Combo{{Name: "t", Models: []string{"*"}, Pipeline: steps}},
	}
}

func noisyPayload() []byte {
	noise := strings.Repeat("tool output   \n\n\n\n", 40)
	return []byte(`{"model":"gpt-4o","messages":[` +
		`{"role":"user","content":` + payloadQuote(noise) + `},` +
		`{"role":"user","content":"second   with   noise   \n\n\n\nsecond"}]}`)
}

// payloadQuote keeps the test independent from the production encoder.
func payloadQuote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\\':
			out = append(out, '\\', c)
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, c)
		}
	}
	return string(append(out, '"'))
}

func TestPayloadCompressionRunsBeforePayloadRules(t *testing.T) {
	cfg := &config.Config{Compression: compressionConfig(payloadcompress.EngineLite)}
	cfg.Payload.Override = []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}},
		Params: map[string]any{"messages.1.content": "OVERRIDDEN"},
	}}

	body := noisyPayload()
	out := ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "openai", "", body, body, "gpt-4o", "/v1/chat/completions")

	if got := gjson.GetBytes(out, "messages.1.content").String(); got != "OVERRIDDEN" {
		t.Fatalf("payload rule must stay the final barrier, got %q", got)
	}
	if strings.Contains(gjson.GetBytes(out, "messages.0.content").String(), "\n\n\n\n") {
		t.Fatal("compression did not run before payload rules")
	}
	if len(out) >= len(body) {
		t.Fatalf("expected shrink, got %d >= %d", len(out), len(body))
	}
}

func TestPayloadCompressionSkipsRootedPayloads(t *testing.T) {
	cfg := &config.Config{Compression: compressionConfig(payloadcompress.EngineLite)}
	body := noisyPayload()
	out := applyRequestCompression(cfg, "gpt-4o", "request", body, nil)
	if string(out) != string(body) {
		t.Fatal("rooted payloads must be left alone")
	}
}

func TestPayloadCompressionDisabledByDefault(t *testing.T) {
	cfg := &config.Config{}
	body := noisyPayload()
	out := applyRequestCompression(cfg, "gpt-4o", "", body, nil)
	if string(out) != string(body) {
		t.Fatal("compression must be opt-in")
	}
}

func TestPayloadCompressionShadowReturnsOriginal(t *testing.T) {
	cfg := &config.Config{Compression: compressionConfig(payloadcompress.EngineLite)}
	cfg.Compression.Shadow = true
	body := noisyPayload()
	out := applyRequestCompression(cfg, "gpt-4o", "", body, nil)
	if string(out) != string(body) {
		t.Fatal("shadow mode must send the original payload")
	}
}

func TestPayloadOutputStyleAppliedThroughBarrier(t *testing.T) {
	cfg := &config.Config{}
	cfg.Compression.OutputStyles = []payloadcompress.OutputStyle{{ID: "terse-prose", Level: "full"}}
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"}]}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "openai", "", body, body, "gpt-4o", "/v1/chat/completions")
	system := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.HasPrefix(system, "base") || !strings.Contains(system, "CLIProxyAPI Output Styles") {
		t.Fatalf("style not injected through the barrier: %q", system)
	}
}

func TestPayloadOutputStyleSurvivesRepeatedApplication(t *testing.T) {
	cfg := &config.Config{}
	cfg.Compression.OutputStyles = []payloadcompress.OutputStyle{{ID: "ponytail", Level: "full"}}
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"}]}`)

	once := ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "openai", "", body, body, "gpt-4o", "/v1/chat/completions")
	twice := ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "openai", "", once, body, "gpt-4o", "/v1/chat/completions")
	if string(once) != string(twice) {
		t.Fatal("barrier reapplication changed the payload")
	}
	if strings.Count(string(twice), "CLIProxyAPI Output Styles") != 1 {
		t.Fatal("style injected twice")
	}
}
