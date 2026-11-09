package payloadcompress

import (
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

var codexTestJSON = "{\n  \"rows\": [\n" +
	strings.Repeat("    {\"id\": 1, \"name\": \"fixture\"},\n", 39) +
	"    {\"id\": 1, \"name\": \"fixture\"}\n" +
	"  ]\n}"

func codexTestBody(items ...string) []byte {
	return []byte(`{"input":[` + strings.Join(items, ",") + `]}`)
}

func TestCodexResponsesName(t *testing.T) {
	if got := newCodexResponsesEngine().name(); got != "codex-responses" {
		t.Fatalf("name = %q", got)
	}
}

func TestCodexResponsesMinifiesEligibleFunctionCallOutput(t *testing.T) {
	body := codexTestBody(
		`{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"c1","output":`+strconv.Quote(codexTestJSON)+`}`,
	)
	got := string(newCodexResponsesEngine().applyBody(body))
	if got == string(body) {
		t.Fatal("expected body to change")
	}
	out := ojson.Get(got, "input.1.output").String()
	if strings.Contains(out, "\n") || strings.Contains(out, "  ") {
		t.Fatalf("output not minified: %q", out)
	}
	if len(out) >= len(codexTestJSON) {
		t.Fatalf("output not smaller: %d >= %d", len(out), len(codexTestJSON))
	}
	// The function_call item must stay byte-identical.
	if call := ojson.Get(got, "input.0").Raw; call != `{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"}` {
		t.Fatalf("call item changed: %s", call)
	}
}

func TestCodexResponsesProtectsPreservedToolNames(t *testing.T) {
	body := codexTestBody(
		`{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"c1","output":`+strconv.Quote(codexTestJSON)+`}`,
	)
	if got := newCodexResponsesEngine().applyBody(body); string(got) != string(body) {
		t.Fatalf("protected tool output changed: %s", got)
	}
}

func TestCodexResponsesFunctionCallOutputNeedsKnownCall(t *testing.T) {
	body := codexTestBody(
		`{"type":"function_call_output","call_id":"missing","output":` + strconv.Quote(codexTestJSON) + `}`,
	)
	if got := newCodexResponsesEngine().applyBody(body); string(got) != string(body) {
		t.Fatalf("output without a function_call changed: %s", got)
	}
}

func TestCodexResponsesCollapsesApplyPatchLogs(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "fixture line " + strconv.Itoa(i)
	}
	lines[30] = "error: compile failed"
	logText := strings.Join(lines, "\n")
	body := codexTestBody(
		`{"type":"apply_patch_call_output","output":` + strconv.Quote(logText) + `}`,
	)
	got := string(newCodexResponsesEngine().applyBody(body))
	if got == string(body) {
		t.Fatal("expected log output to change")
	}
	out := ojson.Get(got, "input.0.output").String()
	if !strings.Contains(out, "[compressed log output: omitted") {
		t.Fatalf("missing omission marker: %q", out)
	}
	if !strings.Contains(out, "error: compile failed") {
		t.Fatal("important line dropped")
	}
	if strings.Contains(out, "fixture line 20") {
		t.Fatal("middle line kept")
	}
}

func TestCodexResponsesLocalShellSkipsLossyRewrites(t *testing.T) {
	diff := "@@ -1,3 +1,3 @@\n" +
		strings.Repeat("-old line\n+new line\n", 40)
	body := codexTestBody(
		`{"type":"local_shell_call_output","output":` + strconv.Quote(diff) + `}`,
	)
	if got := newCodexResponsesEngine().applyBody(body); string(got) != string(body) {
		t.Fatalf("local shell lossy rewrite applied: %s", got)
	}
}

func TestCodexResponsesLeavesMessageAndUnsupportedShapesAlone(t *testing.T) {
	message := `{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("x", 600) + `"}]}`
	body := codexTestBody(message)
	if got := newCodexResponsesEngine().applyBody(body); string(got) != string(body) {
		t.Fatalf("message item changed: %s", got)
	}
	for _, raw := range [][]byte{
		nil,
		[]byte(`not json`),
		[]byte(`{"messages":[{"role":"tool","content":"` + strings.Repeat("x", 600) + `"}]}`),
		[]byte(`{"input":"not-an-array"}`),
	} {
		if got := newCodexResponsesEngine().applyBody(raw); string(got) != string(raw) {
			t.Fatalf("unsupported shape changed: %q -> %q", raw, got)
		}
	}
}
