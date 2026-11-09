package payloadcompress

import (
	"regexp"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func ccrTestBody(text string, withTool bool) []byte {
	b := []byte(`{"model":"m","messages":[{"role":"user","content":`)
	b = appendJSONString(b, text)
	b = append(b, `}]`...)
	if withTool {
		b = append(b, `,"tools":[{"type":"function","function":{"name":"omniroute_ccr_retrieve"}}]`...)
	}
	return append(b, '}')
}

func TestCCREngineIdentity(t *testing.T) {
	if got := newCCREngine().name(); got != "ccr" {
		t.Fatalf("unexpected engine name %q", got)
	}
}

func TestCCRSkipsWithoutRetrieveTool(t *testing.T) {
	ccrResetStore()
	body := ccrTestBody(strings.Repeat("long block ", 100), false)
	if got := newCCREngine().applyBody(body); string(got) != string(body) {
		t.Fatalf("expected body unchanged without retrieve tool")
	}
}

func TestCCRReplacesLargeBlock(t *testing.T) {
	ccrResetStore()
	text := strings.Repeat("long block ", 100)
	body := ccrTestBody(text, true)
	got := newCCREngine().applyBody(body)

	doc := ojson.NewDocString(string(got))
	messages := doc.Get("messages")
	if !messages.IsArray() || len(messages.Array()) != 2 {
		t.Fatalf("expected protocol message plus user message, got %s", got)
	}
	first := messages.Array()[0]
	if first.Get("role").String() != "system" || !strings.Contains(first.Get("content").String(), ccrProtocolMarker) {
		t.Fatalf("expected injected protocol instruction, got %s", first.Raw)
	}

	user := messages.Array()[1].Get("content").String()
	marker := regexp.MustCompile(`\[CCR retrieve hash=[0-9a-f]{24} chars=\d+\]`)
	match := marker.FindString(user)
	if match == "" {
		t.Fatalf("expected CCR marker, got %q", user)
	}
	if !strings.Contains(match, "chars="+itoa(len(text))) {
		t.Fatalf("unexpected char count in %q", match)
	}
	if !strings.HasPrefix(user, strings.TrimRightFunc(text[:200], func(r rune) bool { return r == ' ' })) {
		t.Fatalf("expected a 200-char preamble, got %q", user)
	}
	if len(user) >= len(text) {
		t.Fatalf("replacement did not shrink text")
	}
}

func TestCCRDeterministicID(t *testing.T) {
	ccrResetStore()
	text := strings.Repeat("deterministic content ", 50)
	id1, ok1 := ccrTryStore(text)
	id2, ok2 := ccrTryStore(text)
	if !ok1 || !ok2 || id1 != id2 || len(id1) != 24 {
		t.Fatalf("expected stable 24-hex id, got %q %q", id1, id2)
	}

	body := ccrTestBody(text, true)
	first := newCCREngine().applyBody(body)
	second := newCCREngine().applyBody(body)
	if string(first) != string(second) {
		t.Fatalf("applyBody is not deterministic")
	}
}

func TestCCRSkipsSmallAndToolMessages(t *testing.T) {
	ccrResetStore()
	small := ccrTestBody("too small", true)
	if got := newCCREngine().applyBody(small); string(got) != string(small) {
		t.Fatalf("expected small body unchanged")
	}

	large := strings.Repeat("tool output ", 100)
	body := []byte(`{"model":"m","messages":[{"role":"tool","content":`)
	body = appendJSONString(body, large)
	body = append(body, `}],"tools":[{"name":"omniroute_ccr_retrieve"}]}`...)
	if got := newCCREngine().applyBody(body); string(got) != string(body) {
		t.Fatalf("expected tool message unchanged, got %s", got)
	}
}

func TestCCRNoDoubleInstruction(t *testing.T) {
	ccrResetStore()
	body := ccrTestBody(strings.Repeat("long block ", 100), true)
	once := newCCREngine().applyBody(body)
	twice := newCCREngine().applyBody(once)
	if strings.Count(string(twice), ccrProtocolMarker) != 1 {
		t.Fatalf("protocol instruction injected more than once: %s", twice)
	}
}

func TestCCRFailsOpenOnUnexpectedBody(t *testing.T) {
	for _, body := range [][]byte{
		nil,
		[]byte(``),
		[]byte(`{"messages":"not an array"}`),
		[]byte(`{"tools":[{"name":"omniroute_ccr_retrieve"}],"messages":[{"role":"user","content":{}}]}`),
	} {
		if got := newCCREngine().applyBody(body); string(got) != string(body) {
			t.Fatalf("expected fail-open for %q, got %q", body, got)
		}
	}
}

func TestCCRStoreRejectsOversizedBlock(t *testing.T) {
	ccrResetStore()
	text := strings.Repeat("x", ccrMaxBlockBytes+1)
	if _, ok := ccrTryStore(text); ok {
		t.Fatalf("expected oversized block to be rejected")
	}
}
