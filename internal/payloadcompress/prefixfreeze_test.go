package payloadcompress

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These tests pin the port of upstream's preserveSystemPromptMode and its two
// cache signals. The only thing a cache is allowed to protect here is the system
// prompt: the message history is always fair game, which is what makes the
// default worth having.

// noisySystem is a system prompt long enough and noisy enough that lite rewrites
// it whenever the config lets it through.
func noisySystem() string {
	var b strings.Builder
	b.WriteString("\x1b[32mRules:\x1b[0m   \n\n\n\n")
	b.WriteString(strings.Repeat("Keep diffs small and run gofmt before committing.   \n", 8))
	return b.String()
}

// testCacheMarker is the client-owned breakpoint. It is stripped before wire
// bytes are compared, because it moves between turns while the token stream it
// annotates does not.
const testCacheMarker = `,"cache_control":{"type":"ephemeral"}`

func textMessage(role, text string, mark bool) []byte {
	b := append([]byte(`{"role":"`), role...)
	if role == "tool" {
		// Plain string content: upstream lite only collapses string content.
		b = append(b, `","content":`...)
		b = appendJSONString(b, text)
		return append(b, '}')
	}
	b = append(b, `","content":[{"type":"text","text":`...)
	b = appendJSONString(b, text)
	if mark {
		b = append(b, testCacheMarker...)
	}
	return append(b, `}]}`...)
}

// systemPromptBody builds a chat body whose system prompt is the noisy one, with
// an optional cache_control marker on the newest user turn.
func systemPromptBody(marked bool) []byte {
	b := append([]byte(`{"model":"`), "some-unknown-model"...)
	b = append(b, `","max_tokens":64,"system":`...)
	b = appendJSONString(b, noisySystem())
	b = append(b, `,"messages":[`...)
	b = append(b, textMessage("user", "please fix the failing test", marked)...)
	b = append(b, `]}`...)
	return b
}

func TestPreserveSystemPromptAlwaysLeavesSystemRaw(t *testing.T) {
	resetPrefixFreeze()
	for _, mode := range []string{"", PreserveSystemPromptAlways} {
		cfg := testConfig(EngineLite)
		cfg.PreserveSystemPrompt = mode
		cfg.Sanitize()
		body := systemPromptBody(false)
		out, stats := Apply(cfg, "some-unknown-model", body)
		if !bytes.Contains(out, appendJSONString(nil, noisySystem())) {
			t.Fatalf("mode %q rewrote the system prompt", mode)
		}
		if stats.Segments != 1 {
			t.Fatalf("mode %q: %d segments, want 1 (the message only)", mode, stats.Segments)
		}
	}
}

func TestPreserveSystemPromptWhenNoCacheRewritesSystemWithoutMarkers(t *testing.T) {
	resetPrefixFreeze()
	cfg := testConfig(EngineLite)
	cfg.PreserveSystemPrompt = PreserveSystemPromptWhenNoCache
	cfg.Sanitize()
	body := systemPromptBody(false)
	out, stats := Apply(cfg, "some-unknown-model", body)
	if bytes.Contains(out, appendJSONString(nil, noisySystem())) {
		t.Fatal("nothing is cached, so the system prompt should have been rewritten")
	}
	if stats.Segments != 2 {
		t.Fatalf("%d segments, want 2 (system prompt and message)", stats.Segments)
	}
}

func TestPreserveSystemPromptWhenNoCacheLeavesSystemRawWhenCached(t *testing.T) {
	resetPrefixFreeze()
	cfg := testConfig(EngineLite)
	cfg.PreserveSystemPrompt = PreserveSystemPromptWhenNoCache
	cfg.Sanitize()
	for _, marked := range []bool{true} {
		out, _ := Apply(cfg, "some-unknown-model", systemPromptBody(marked))
		if !bytes.Contains(out, appendJSONString(nil, noisySystem())) {
			t.Fatal("a cache_control marker was present, so the system prompt must survive byte for byte")
		}
	}
	// A provider that caches the prefix automatically sends no markers at all.
	out, _ := Apply(cfg, "gpt-5", systemPromptBody(false))
	if !bytes.Contains(out, appendJSONString(nil, noisySystem())) {
		t.Fatal("an automatic-prefix-cache model must be treated as cached")
	}
}

func TestPreserveSystemPromptNeverRewritesSystemEvenWhenCached(t *testing.T) {
	resetPrefixFreeze()
	cfg := testConfig(EngineLite)
	cfg.PreserveSystemPrompt = PreserveSystemPromptNever
	cfg.Sanitize()
	out, _ := Apply(cfg, "claude-sonnet-4", systemPromptBody(true))
	if bytes.Contains(out, appendJSONString(nil, noisySystem())) {
		t.Fatal("mode never must rewrite the system prompt even when a cache is present")
	}
}

func TestPrefixFreezeFreezesAfterThreshold(t *testing.T) {
	resetPrefixFreeze()
	cfg := testConfig(EngineLite)
	cfg.PreserveSystemPrompt = PreserveSystemPromptWhenNoCache
	cfg.PrefixFreeze = PrefixFreezeConfig{Enabled: true, Threshold: 3}
	cfg.Sanitize()

	body := systemPromptBody(false)
	rewritten := func() bool {
		out, _ := Apply(cfg, "some-unknown-model", body)
		return !bytes.Contains(out, appendJSONString(nil, noisySystem()))
	}
	if !rewritten() || !rewritten() {
		t.Fatal("a first-seen system prompt has no cache to protect, so it should be rewritten")
	}
	if rewritten() {
		t.Fatal("the third sighting should have frozen the prefix")
	}
}

func TestPrefixFreezeOffByDefault(t *testing.T) {
	cfg := Config{}
	cfg.Sanitize()
	if cfg.PrefixFreeze.Enabled {
		t.Fatal("prefix freeze must be opt-in")
	}
	if cfg.PrefixFreeze.Threshold != defaultPrefixFreezeThreshold {
		t.Fatalf("threshold = %d, want %d", cfg.PrefixFreeze.Threshold, defaultPrefixFreezeThreshold)
	}
}

func TestModelCachesPrefix(t *testing.T) {
	caching := []string{"claude-sonnet-4", "gpt-5-codex", "openai/gpt-4o", "azure/gpt-4", "o3-mini", "deepseek-chat"}
	for _, model := range caching {
		if !modelCachesPrefix(model) {
			t.Fatalf("%q should be treated as a caching provider", model)
		}
	}
	for _, model := range []string{"llama-3.1-70b", "mistral-large", "local-model"} {
		if modelCachesPrefix(model) {
			t.Fatalf("%q should not be treated as a caching provider", model)
		}
	}
}

func TestCompressionBlockParsesPreserveSystemPromptKey(t *testing.T) {
	const raw = `
compression:
  enabled: true
  preserve-system-prompt: when-no-cache
  prefix-freeze:
    enabled: true
    threshold: 5
  combos:
    - name: c
      pipeline:
        - engine: lite
`
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	decoder.KnownFields(true)
	var wrapper struct {
		Compression Config `yaml:"compression"`
	}
	if errDecode := decoder.Decode(&wrapper); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	cfg := wrapper.Compression
	cfg.Sanitize()
	if cfg.PreserveSystemPrompt != PreserveSystemPromptWhenNoCache {
		t.Fatalf("cache protection keys lost: %+v", cfg)
	}
	if !cfg.PrefixFreeze.Enabled || cfg.PrefixFreeze.Threshold != 5 {
		t.Fatalf("prefix-freeze lost: %+v", cfg.PrefixFreeze)
	}
}

func TestSanitizeFillsDefaultPipelineWhenEnabledWithoutCombos(t *testing.T) {
	stated := Config{Enabled: true}
	stated.Sanitize()
	if len(stated.Combos) != 1 || stated.DefaultCombo != defaultCombo {
		t.Fatalf("enabled with no combos should fall back to the built-in pipeline: %+v", stated)
	}
	if len(stated.Combos[0].Pipeline) != 2 {
		t.Fatalf("built-in pipeline has %d steps, want 2", len(stated.Combos[0].Pipeline))
	}
	inert := Config{}
	inert.Sanitize()
	if len(inert.Combos) != 0 {
		t.Fatal("a config that is not enabled must stay inert")
	}
}
