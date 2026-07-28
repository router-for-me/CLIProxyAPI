package loguploader

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func defaultGateRules() SessionGateConfig {
	return SessionGateConfig{
		Enabled:                    true,
		MinPromptRounds:            4,
		RequireToolCall:            true,
		RequireSessionID:           true,
		RequireEndsWithoutToolCall: true,
		RejectUnpairedToolCalls:    true,
		ExcludeTitleSummary:        true,
		ExcludeIDEContext:          true,
		ExcludeEnvContext:          true,
		MaxHoldAge:                 48 * time.Hour,
		MaxAbsoluteAge:             168 * time.Hour,
		DeferredPackaging:          "eligibility-hour",
	}
}

func writeGateLog(t *testing.T, dir, name, body string) sourceLog {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(path, old, old)
	info, _ = os.Stat(path)
	loc := time.FixedZone("CST", 8*3600)
	src, err := inspectSourceLog(filepath.Dir(dir), path, info, loc)
	// inspect expects logsRoot/key/file; adjust if needed
	if err != nil {
		// parent is keys root
		src, err = inspectSourceLog(filepath.Dir(filepath.Dir(path)), path, info, loc)
	}
	if err != nil {
		// construct manually for unit tests
		src = sourceLog{
			Path:        path,
			Relative:    filepath.ToSlash(filepath.Join(filepath.Base(dir), name)),
			KeyName:     filepath.Base(dir),
			Model:       "gpt-5.6-sol",
			Provider:    providerCodex,
			Timestamp:   old,
			ArchiveHour: old.In(loc).Truncate(time.Hour),
			Size:        info.Size(),
			ModTime:     info.ModTime(),
			Fingerprint: "fp-" + name,
		}
	}
	return src
}

func passSessionBody(sessionID string) string {
	return `=== REQUEST INFO ===
Timestamp: 2026-07-15T03:10:00+08:00

=== HEADERS ===
Session-Id: ` + sessionID + `
X-Codex-Turn-Metadata: {"session_id":"` + sessionID + `","thread_id":"thr-1","request_kind":"turn"}

=== REQUEST BODY ===
{"model":"gpt-5.6-sol","input":[
  {"type":"message","role":"user","content":"q1"},
  {"type":"message","role":"user","content":"q2"},
  {"type":"message","role":"user","content":"q3"},
  {"type":"message","role":"user","content":"q4"},
  {"type":"function_call","call_id":"call-1","name":"shell","arguments":"{}"},
  {"type":"function_call_output","call_id":"call-1","output":"ok"},
  {"type":"message","role":"assistant","content":"done"}
]}

=== RESPONSE ===
Status: 200
Content-Type: text/event-stream

event: response.completed
data: {"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}}
`
}

func TestEvaluateSessionPass(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	keyDir := filepath.Join(root, "panda")
	if err := os.MkdirAll(keyDir, 0o750); err != nil {
		t.Fatal(err)
	}
	src := writeGateLog(t, keyDir, "ok.log", passSessionBody("sess-pass"))
	loc := time.FixedZone("CST", 8*3600)
	m := parseGateFile(src, loc, defaultGateRules())
	if m.SessionID != "sess-pass" {
		t.Fatalf("session_id=%q", m.SessionID)
	}
	if m.PromptRounds < 4 {
		t.Fatalf("prompt_rounds=%d", m.PromptRounds)
	}
	if m.ToolCalls < 1 {
		t.Fatalf("tool_calls=%d", m.ToolCalls)
	}
	if m.UnpairedToolCalls {
		t.Fatal("expected paired tools")
	}
	if !m.ResponseParsed || isToolCallType(m.LastResponseType) {
		t.Fatalf("response type=%q parsed=%v", m.LastResponseType, m.ResponseParsed)
	}
	v := evaluateOneSession("sess-pass", []gateFileMetrics{m}, defaultGateRules())
	if !v.OK {
		t.Fatalf("expected pass, reasons=%v", v.Reasons)
	}
}

func TestEvaluateEmptySessionID(t *testing.T) {
	t.Parallel()
	body := `=== REQUEST INFO ===
Timestamp: 2026-07-15T03:10:00+08:00

=== HEADERS ===
X-Codex-Turn-Metadata: {"request_kind":"turn"}

=== REQUEST BODY ===
{"model":"gpt-5.6-sol","input":[
  {"type":"message","role":"user","content":"q1"},
  {"type":"message","role":"user","content":"q2"},
  {"type":"message","role":"user","content":"q3"},
  {"type":"message","role":"user","content":"q4"},
  {"type":"function_call","call_id":"call-1","name":"shell","arguments":"{}"},
  {"type":"function_call_output","call_id":"call-1","output":"ok"}
]}

=== RESPONSE ===
data: {"type":"response.completed","response":{"output":[{"type":"message"}]}}
`
	root := t.TempDir()
	keyDir := filepath.Join(root, "panda")
	_ = os.MkdirAll(keyDir, 0o750)
	src := writeGateLog(t, keyDir, "nosess.log", body)
	m := parseGateFile(src, time.UTC, defaultGateRules())
	if m.SessionID != "" {
		t.Fatalf("session_id should be empty, got %q", m.SessionID)
	}
	v := evaluateOneSession("orphan:panda/nosess.log", []gateFileMetrics{m}, defaultGateRules())
	if v.OK {
		t.Fatal("expected fail")
	}
	found := false
	for _, r := range v.Reasons {
		if r == "empty_session_id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons=%v", v.Reasons)
	}
}

func TestEvaluateUnpairedToolCalls(t *testing.T) {
	t.Parallel()
	body := `=== REQUEST INFO ===
Timestamp: 2026-07-15T03:10:00+08:00

=== HEADERS ===
Session-Id: sess-bad
X-Codex-Turn-Metadata: {"session_id":"sess-bad","request_kind":"turn"}

=== REQUEST BODY ===
{"model":"gpt-5.6-sol","input":[
  {"type":"message","role":"user","content":"q1"},
  {"type":"message","role":"user","content":"q2"},
  {"type":"message","role":"user","content":"q3"},
  {"type":"message","role":"user","content":"q4"},
  {"type":"function_call","call_id":"call-1","name":"shell","arguments":"{}"}
]}

=== RESPONSE ===
data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}}
`
	root := t.TempDir()
	keyDir := filepath.Join(root, "panda")
	_ = os.MkdirAll(keyDir, 0o750)
	src := writeGateLog(t, keyDir, "unpaired.log", body)
	m := parseGateFile(src, time.UTC, defaultGateRules())
	if !m.UnpairedToolCalls {
		t.Fatal("expected unpaired")
	}
	v := evaluateOneSession("sess-bad", []gateFileMetrics{m}, defaultGateRules())
	if v.OK {
		t.Fatal("expected fail")
	}
}

func TestEvaluateEndsWithToolCall(t *testing.T) {
	t.Parallel()
	body := `=== REQUEST INFO ===
Timestamp: 2026-07-15T03:10:00+08:00

=== HEADERS ===
Session-Id: sess-tool
X-Codex-Turn-Metadata: {"session_id":"sess-tool","request_kind":"turn"}

=== REQUEST BODY ===
{"model":"gpt-5.6-sol","input":[
  {"type":"message","role":"user","content":"q1"},
  {"type":"message","role":"user","content":"q2"},
  {"type":"message","role":"user","content":"q3"},
  {"type":"message","role":"user","content":"q4"},
  {"type":"function_call","call_id":"call-1","name":"shell","arguments":"{}"},
  {"type":"function_call_output","call_id":"call-1","output":"ok"}
]}

=== RESPONSE ===
data: {"type":"response.completed","response":{"output":[{"type":"function_call","call_id":"call-2","name":"shell"}]}}
`
	root := t.TempDir()
	keyDir := filepath.Join(root, "panda")
	_ = os.MkdirAll(keyDir, 0o750)
	src := writeGateLog(t, keyDir, "endtool.log", body)
	m := parseGateFile(src, time.UTC, defaultGateRules())
	v := evaluateOneSession("sess-tool", []gateFileMetrics{m}, defaultGateRules())
	if v.OK {
		t.Fatalf("expected hold, reasons=%v", v.Reasons)
	}
	found := false
	for _, r := range v.Reasons {
		if r == "ends_with_tool_call" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons=%v lastType=%q", v.Reasons, m.LastResponseType)
	}
}

func TestEvaluateLowPromptRounds(t *testing.T) {
	t.Parallel()
	body := `=== REQUEST INFO ===
Timestamp: 2026-07-15T03:10:00+08:00

=== HEADERS ===
Session-Id: sess-low
X-Codex-Turn-Metadata: {"session_id":"sess-low","request_kind":"turn"}

=== REQUEST BODY ===
{"model":"gpt-5.6-sol","input":[
  {"type":"message","role":"user","content":"q1"},
  {"type":"function_call","call_id":"call-1","name":"shell","arguments":"{}"},
  {"type":"function_call_output","call_id":"call-1","output":"ok"}
]}

=== RESPONSE ===
data: {"type":"response.completed","response":{"output":[{"type":"message"}]}}
`
	root := t.TempDir()
	keyDir := filepath.Join(root, "panda")
	_ = os.MkdirAll(keyDir, 0o750)
	src := writeGateLog(t, keyDir, "low.log", body)
	m := parseGateFile(src, time.UTC, defaultGateRules())
	v := evaluateOneSession("sess-low", []gateFileMetrics{m}, defaultGateRules())
	if v.OK {
		t.Fatal("expected fail")
	}
}

func TestLoadExampleConfigWithSessionGate(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "log-uploader.example.yaml")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SessionGate.Enabled {
		t.Fatal("example should keep session-gate disabled for safe rollout")
	}
	if cfg.SessionGate.MinPromptRounds != 4 {
		t.Fatalf("min-prompt-rounds=%d", cfg.SessionGate.MinPromptRounds)
	}
	if cfg.SessionGate.DeferredPackaging != "eligibility-hour" {
		t.Fatalf("packaging=%q", cfg.SessionGate.DeferredPackaging)
	}
	if cfg.SessionGate.MaxHoldAge != 48*time.Hour {
		t.Fatalf("max-hold-age=%s", cfg.SessionGate.MaxHoldAge)
	}
}
