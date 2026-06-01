package helps

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRepairInvalidJSONStringEscapes(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[{"role":"user","content":"- **归档**：\archive/20260516 and *破甲**\ufeff\v**"}]}`)
	if json.Valid(body) {
		t.Fatal("fixture should start as invalid JSON")
	}

	out, changed := RepairInvalidJSONStringEscapes(body)
	if !changed {
		t.Fatal("expected invalid escapes to be repaired")
	}
	if !json.Valid(out) {
		t.Fatalf("repaired body should be valid JSON: %s", string(out))
	}
	raw := string(out)
	for _, want := range []string{`\\archive/20260516`, `\ufeff\\v`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("repaired body missing %q: %s", want, raw)
		}
	}
}

func TestRepairInvalidJSONStringEscapesKeepsValidJSON(t *testing.T) {
	t.Parallel()

	body := []byte(`{"path":"C:\\repo\\README.md","text":"line\nnext","unicode":"\ufeff"}`)
	out, changed := RepairInvalidJSONStringEscapes(body)
	if changed {
		t.Fatalf("valid JSON should not change: %s", string(out))
	}
	if string(out) != string(body) {
		t.Fatalf("valid JSON output changed: %s", string(out))
	}
}

func TestRepairInvalidJSONStringEscapesRawControl(t *testing.T) {
	t.Parallel()

	body := []byte("{\"text\":\"hello \v world\"}")
	out, changed := RepairInvalidJSONStringEscapes(body)
	if !changed {
		t.Fatal("expected raw control character to be repaired")
	}
	if !json.Valid(out) {
		t.Fatalf("repaired control body should be valid JSON: %s", string(out))
	}
	if !strings.Contains(string(out), `\u000b`) {
		t.Fatalf("control character should be escaped: %s", string(out))
	}
}
