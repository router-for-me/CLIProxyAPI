package payloadcompress

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyFidelityGate(t *testing.T) {
	cases := []struct {
		name   string
		before string
		after  string
		revert bool
	}{
		{
			name:   "keeps protected tokens",
			before: "edit /src/main.go, timeout 30s, see https://x.test/a and {\"user_id\": 1}\n```go\nx := 1\n```",
			after:  "edit /src/main.go, timeout 30s, see https://x.test/a and {\"user_id\": 1}\n```go\nx := 1\n```",
		},
		{
			name:   "drops url",
			before: "see https://x.test/a now",
			after:  "see now",
			revert: true,
		},
		{
			name:   "drops number with unit",
			before: "timeout is 30s",
			after:  "timeout is thirty seconds",
			revert: true,
		},
		{
			name:   "drops json key",
			before: `config {"user_id": 1}`,
			after:  `config {"id": 1}`,
			revert: true,
		},
		{
			name:   "drops file path",
			before: "edit internal/api/server.go",
			after:  "edit server.go",
			revert: true,
		},
		{
			name:   "drops fenced code",
			before: "run:\n```go\nfunc main() {}\n```",
			after:  "run:",
			revert: true,
		},
		{
			name:   "drops diff line",
			before: "diff --git a/x b/x\n@@ -1,2 +1,2 @@\n-old line\n+new line",
			after:  "diff --git a/x b/x\n@@ -1,2 +1,2 @@\n-old line",
			revert: true,
		},
		{
			name:   "empty before",
			before: "",
			after:  "anything",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reverted := applyFidelityGate(tc.before, tc.after)
			if reverted != tc.revert {
				t.Fatalf("reverted = %v, want %v", reverted, tc.revert)
			}
			want := tc.after
			if tc.revert {
				want = tc.before
			}
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestMaskRisksRoundTrip(t *testing.T) {
	secret := "sk-abcdefghijklmnopqrstuvwx"
	text := "key=" + secret + " password: hunter2hunter2"
	masked, restore := maskRisks(text)
	if strings.Contains(masked, secret) || strings.Contains(masked, "hunter2hunter2") {
		t.Fatalf("masked text leaks a secret: %q", masked)
	}
	if masked == text {
		t.Fatal("expected masking to change the text")
	}
	if got := restore(masked); got != text {
		t.Fatalf("restore round trip = %q, want %q", got, text)
	}
	// The restore func must survive surrounding edits (compression).
	compressed := "compressed " + masked + " tail"
	if got := restore(compressed); got != "compressed "+text+" tail" {
		t.Fatalf("restore after compression = %q", got)
	}
}

func TestMaskRisksPEM(t *testing.T) {
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"
	masked, restore := maskRisks("creds:\n" + pem)
	if strings.Contains(masked, "MIIEowIBAAKCAQEA") {
		t.Fatalf("masked text leaks private key: %q", masked)
	}
	if got := restore(masked); got != "creds:\n"+pem {
		t.Fatalf("restore = %q", got)
	}
}

func TestMaskRisksNoSecret(t *testing.T) {
	text := "nothing sensitive here"
	masked, restore := maskRisks(text)
	if masked != text {
		t.Fatalf("masked = %q, want unchanged", masked)
	}
	if got := restore(text); got != text {
		t.Fatalf("restore = %q, want unchanged", got)
	}
}

func TestApplyHardBudgetTrimsInsideMessages(t *testing.T) {
	big := strings.Repeat("plain prose line\n", 500)
	last := "CURRENT-TURN marker"
	raw, errJSON := json.Marshal(map[string]any{
		"model": "m",
		"messages": []map[string]string{
			{"role": "system", "content": "SYSTEM-PROMPT"},
			{"role": "user", "content": big},
			{"role": "assistant", "content": big},
			{"role": "user", "content": last},
		},
	})
	if errJSON != nil {
		t.Fatal(errJSON)
	}
	out := applyHardBudget(string(raw), 1000, false)
	if out == string(raw) {
		t.Fatal("expected the body to be trimmed")
	}
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if errUnmarshal := json.Unmarshal([]byte(out), &parsed); errUnmarshal != nil {
		t.Fatalf("trimmed body is not valid JSON: %v", errUnmarshal)
	}
	contentTokens := 0
	for _, m := range parsed.Messages {
		contentTokens += approxTokens(m.Content)
	}
	if contentTokens > 1000 {
		t.Fatalf("message content has %d tokens, over budget", contentTokens)
	}
	if len(parsed.Messages) != 4 {
		t.Fatalf("kept %d messages, want all 4", len(parsed.Messages))
	}
	if len(parsed.Messages[1].Content) >= len(big) || len(parsed.Messages[2].Content) >= len(big) {
		t.Fatal("expected low-saliency units to be dropped inside the big messages")
	}
	if parsed.Messages[3].Content != last {
		t.Fatal("current turn was not preserved")
	}
}

func TestApplyHardBudgetNoop(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}]}`
	if got := applyHardBudget(body, 1_000_000, false); got != body {
		t.Fatalf("under-budget body changed: %q", got)
	}
	if got := applyHardBudget("{not json", 10, false); got != "{not json" {
		t.Fatalf("invalid JSON changed: %q", got)
	}
	noMsgs := `{"model":"m"}`
	if got := applyHardBudget(noMsgs, 1, false); got != noMsgs {
		t.Fatalf("body without messages changed: %q", got)
	}
}

func TestApplyHardBudgetKeepsMessages(t *testing.T) {
	big := strings.Repeat("plain prose line\n", 2000)
	raw, errJSON := json.Marshal(map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": big},
			{"role": "user", "content": big},
		},
	})
	if errJSON != nil {
		t.Fatal(errJSON)
	}
	out := applyHardBudget(string(raw), 10, false)
	if out == string(raw) {
		t.Fatal("expected units to be dropped")
	}
	var parsed struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if errUnmarshal := json.Unmarshal([]byte(out), &parsed); errUnmarshal != nil {
		t.Fatalf("trimmed body is not valid JSON: %v", errUnmarshal)
	}
	if len(parsed.Messages) != 2 {
		t.Fatalf("kept %d messages, want all 2", len(parsed.Messages))
	}
}

func TestIsExcluded(t *testing.T) {
	patterns := []string{"gpt-5-6", "openai/*", "Claude-*", "  "}
	cases := []struct {
		model string
		want  bool
	}{
		{"gpt-5-6", true},
		{"GPT-5-6", true},
		{"openai/gpt-4o", true},
		{"OPENAI/GPT-4O", true},
		{"claude-3", true},
		{"gpt-5-6-extra", false},
		{"anthropic/claude-3", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isExcluded(tc.model, patterns); got != tc.want {
			t.Errorf("isExcluded(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
	if isExcluded("gpt-5-6", nil) {
		t.Error("nil patterns must not exclude")
	}
	if !isExcluded("gpt-4o", patterns, "openai") {
		t.Error("provider-qualified pattern must match a bare model")
	}
	if isExcluded("gpt-4o", patterns, "anthropic") {
		t.Error("provider-qualified pattern must not match another provider")
	}
}

type staticTestEngine struct {
	engineName string
	fn         func(string) string
}

func (e staticTestEngine) name() string { return e.engineName }

func (e staticTestEngine) compress(text string) (string, bool) {
	out := e.fn(text)
	return out, out != text
}

// A budget-only configuration must be enforced even when no engine changes the
// payload.
func TestApplyHardBudgetRunsWithoutEngineChange(t *testing.T) {
	useTestEngine(t, func(Step) engine {
		return staticTestEngine{engineName: EngineLite, fn: func(s string) string { return s }}
	})
	big := strings.Repeat("plain prose line\n", 500)
	body, errJSON := json.Marshal(map[string]any{
		"model":    "m",
		"messages": []map[string]string{{"role": "user", "content": big}},
	})
	if errJSON != nil {
		t.Fatal(errJSON)
	}
	cfg := Config{
		Enabled:          true,
		DefaultCombo:     "default",
		Combos:           []Combo{{Name: "default", Pipeline: []Step{{Engine: EngineLite}}}},
		HardBudgetTokens: 100,
	}
	out, _ := Apply(cfg, "m", body)
	if len(out) >= len(body) {
		t.Fatalf("hard budget was skipped: body %d -> %d bytes", len(body), len(out))
	}
	if !json.Valid(out) {
		t.Fatal("budget output is not valid JSON")
	}
}

// Fidelity gate reverts only segments that lost a protected token; safe
// compression must survive.
func TestApplyFidelityGateKeepsSafeCompression(t *testing.T) {
	useTestEngine(t, func(Step) engine {
		return staticTestEngine{engineName: EngineLite, fn: func(s string) string {
			return strings.ReplaceAll(s, "verbose prose ", "")
		}}
	})
	on := true
	cfg := Config{
		Enabled:      true,
		DefaultCombo: "default",
		Combos: []Combo{{Name: "default", Pipeline: []Step{{
			Engine:  "fidelitytest",
			Options: StepOptions{FidelityGate: &on},
		}}}},
	}
	body, errJSON := json.Marshal(map[string]any{
		"model":    "m",
		"messages": []map[string]string{{"role": "user", "content": "verbose prose keep https://x.test/a now"}},
	})
	if errJSON != nil {
		t.Fatal(errJSON)
	}
	out, stats := Apply(cfg, "m", body)
	var parsed struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if errUnmarshal := json.Unmarshal(out, &parsed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if got := parsed.Messages[0].Content; got != "keep https://x.test/a now" {
		t.Fatalf("safe compression was reverted: %q", got)
	}
	if !stats.Applied {
		t.Fatal("fidelity gate disabled the whole step")
	}
}

func TestRiskGateThroughApply(t *testing.T) {
	// The engine corrupts the secret unless it was masked before the rewrite, so
	// this only passes if runEngine masks and restores.
	useTestEngine(t, func(Step) engine {
		return staticTestEngine{engineName: EngineLite, fn: func(s string) string {
			return strings.ReplaceAll(strings.ReplaceAll(s, "end", "E"), "sk-abcdefghijklmnopqrstuvwx", "REDACTED")
		}}
	})
	enabled := true
	// An engine name the built-in switch does not know, so useTestEngine's hook
	// is the engine that actually runs.
	cfg := testConfig("static-risk")
	cfg.Combos[0].Pipeline[0].Options.RiskGate = &enabled
	secret := "sk-abcdefghijklmnopqrstuvwx"
	body := []byte(`{"messages":[{"role":"user","content":"key=` + secret + `    \n\n\n\nend"}]}`)
	out, stats := Apply(cfg, "m", body)
	if !stats.Applied {
		t.Fatal("risk gate disabled the whole step")
	}
	if !strings.Contains(string(out), secret) {
		t.Fatalf("risk gate failed to restore the secret:\n%s", out)
	}
	if strings.Contains(string(out), "REDACTED") {
		t.Fatalf("masked secret leaked into the rewrite:\n%s", out)
	}
	// Companion: with the gate off the same engine corrupts the secret, which
	// proves the assertions above depend on masking rather than on a no-op engine.
	cfg.Combos[0].Pipeline[0].Options.RiskGate = nil
	out, _ = Apply(cfg, "m", body)
	if strings.Contains(string(out), secret) {
		t.Fatalf("engine did not rewrite the secret with the gate off:\n%s", out)
	}
}

func TestApplyFidelityGateNumericIntegrity(t *testing.T) {
	before := "pid 8123\nline 42: unexpected EOF"
	if _, reverted := applyFidelityGate(before, "pid 8123"); !reverted {
		t.Fatal("a dropped bare number must revert the step")
	}
	if _, reverted := applyFidelityGate(before, before); reverted {
		t.Fatal("an unchanged text must pass")
	}
}

func TestMaskRisksRestoreStripsSplitPlaceholder(t *testing.T) {
	masked, restore := maskRisks("key=sk-abcdefghijklmnopqrstuvwx")
	cut := strings.Index(masked, "\x00OMNI_") + 6
	out := restore(masked[:cut])
	if strings.Contains(out, "OMNI_") || strings.ContainsRune(out, 0) {
		t.Fatalf("split placeholder fragment leaked: %q", out)
	}
}

func TestHardBudgetSkipsProtectedMessages(t *testing.T) {
	one := strings.Repeat("plain prose line\n", 200)
	raw, errJSON := json.Marshal(map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": "be terse"},
			{"role": "user", "content": one},
		},
	})
	if errJSON != nil {
		t.Fatal(errJSON)
	}
	out := applyHardBudget(string(raw), approxTokens(one)/2, true)
	var parsed struct {
		Messages []struct{ Content string }
	}
	if errJSON := json.Unmarshal([]byte(out), &parsed); errJSON != nil {
		t.Fatal(errJSON)
	}
	if got := parsed.Messages[0].Content; got != "be terse" {
		t.Fatalf("budget rewrote the preserved system prompt: %q", got)
	}
	if got := parsed.Messages[1].Content; len(got) >= len(one) {
		t.Fatal("budget did not trim the unprotected message")
	}
}
