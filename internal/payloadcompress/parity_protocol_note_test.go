package payloadcompress

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// Upstream 616dc5bebc: the marker-protocol note moved out of the CCR engine into
// a pipeline step, so a `[dedup:ref sha=...]` marker left by dedup alone is now
// explained, and the note names the retrieve tool exactly as the caller
// advertised it.

func noteTestBody(messages string, tools string) []byte {
	body := `{"model":"m","messages":[` + messages + `]`
	if tools != "" {
		body += `,"tools":[` + tools + `]`
	}
	return []byte(body + `}`)
}

func systemMessages(t *testing.T, body []byte) []string {
	t.Helper()
	doc := ojson.NewDocString(string(body))
	messages := doc.Get("messages")
	if !messages.IsArray() {
		t.Fatalf("messages is not an array: %s", body)
	}
	var out []string
	for _, m := range messages.Array() {
		if m.Get("role").String() == "system" {
			out = append(out, m.Get("content").String())
		}
	}
	return out
}

func TestMarkerNoteExplainsDedupOnlyMarker(t *testing.T) {
	body := noteTestBody(
		`{"role":"user","content":"read [dedup:ref sha=deadbeef] again"}`,
		"",
	)
	out := applyMarkerProtocolNote(body)
	systems := systemMessages(t, out)
	if len(systems) != 1 {
		t.Fatalf("expected one note, got %d: %s", len(systems), out)
	}
	if !strings.Contains(systems[0], ccrProtocolSentinel) {
		t.Fatalf("note lacks the sentinel: %q", systems[0])
	}
	if !strings.Contains(systems[0], "[dedup:ref sha=...]") {
		t.Fatalf("note does not explain the dedup marker: %q", systems[0])
	}
	// A caller that cannot reach the retrieve tool must not be told to call it.
	if strings.Contains(systems[0], ccrRetrieveToolName) {
		t.Fatalf("dedup-only note announces an unreachable tool: %q", systems[0])
	}
}

// A dedup marker with the retrieve tool advertised must get the full contract
// (and the explicit warning not to call the tool for a dedup reference) even
// when no CCR marker is present: upstream selects the note by tool reachability,
// not by which marker happened to survive.
func TestMarkerNoteDedupWithToolGetsFullContract(t *testing.T) {
	body := noteTestBody(
		`{"role":"user","content":"read [dedup:ref sha=deadbeef] again"}`,
		`{"name":"omniroute_ccr_retrieve"}`,
	)
	systems := systemMessages(t, applyMarkerProtocolNote(body))
	if len(systems) != 1 {
		t.Fatalf("expected one note, got %d", len(systems))
	}
	if !strings.Contains(systems[0], "content-compression-retrieve") {
		t.Fatalf("note is not the full contract: %q", systems[0])
	}
	if !strings.Contains(systems[0], "Do NOT call omniroute_ccr_retrieve for a dedup reference") {
		t.Fatalf("full contract lacks the dedup warning: %q", systems[0])
	}
}

// Every separator upstream accepts must resolve the tool: gateways namespace
// re-exposed tools with "__", ".", "/", ":", or a single "_".
func TestMarkerNoteAcceptsEveryGatewaySeparator(t *testing.T) {
	for _, sep := range []string{"__", ".", "/", ":", "_"} {
		name := "gateway" + sep + ccrRetrieveToolName
		if !ccrMatchesRetrieveToolName(name) {
			t.Errorf("separator %q does not resolve %q", sep, name)
		}
	}
	for _, name := range []string{ccrRetrieveToolName + "_v2", "x" + ccrRetrieveToolName, "omniroute_ccr_retriev"} {
		if ccrMatchesRetrieveToolName(name) {
			t.Errorf("near-miss %q must not resolve", name)
		}
	}
}

// The dedup-only note wording is upstream's; it names markers as replacements
// rather than as a dedup engine the model must learn.
func TestMarkerNoteDedupOnlyMatchesUpstreamWording(t *testing.T) {
	body := noteTestBody(`{"role":"user","content":"read [dedup:ref sha=deadbeef] again"}`, "")
	systems := systemMessages(t, applyMarkerProtocolNote(body))
	if got, want := systems[0],
		ccrProtocolSentinel+" Some content in this conversation is replaced by markers like "+
			"`[dedup:ref sha=...]`, which means that content already appeared earlier — look back in the message history for it."; got != want {
		t.Fatalf("dedup-only note drifted from upstream:\n got %q\nwant %q", got, want)
	}
}

// A Responses `input[]` body must get the note too: upstream's body adapter
// rewrites input[] into messages before the engines and restores the injected
// note as a typed item, but the port has no adapter, so the note pass has to
// read and write input[] itself.
func TestMarkerNoteCoversResponsesInput(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"see [dedup:ref sha=deadbeef]"}]}` +
		`]}`)
	out := applyMarkerProtocolNote(body)
	doc := ojson.NewDocString(string(out))
	items := doc.Get("input")
	if !items.IsArray() {
		t.Fatalf("input is not an array: %s", out)
	}
	first := items.Array()[0]
	if got := first.Get("type").String(); got != "message" {
		t.Fatalf("injected item is not a Responses message item: %s", first.Raw)
	}
	if got := first.Get("role").String(); got != "system" {
		t.Fatalf("injected item is not a system note: %s", first.Raw)
	}
	if !strings.Contains(first.Get("content").String(), ccrProtocolSentinel) {
		t.Fatalf("injected item lacks the note: %s", first.Raw)
	}
	if !strings.Contains(string(out), "see [dedup:ref sha=deadbeef]") {
		t.Fatalf("original input item was not preserved: %s", out)
	}
}

func TestMarkerNoteNamesAdvertisedTool(t *testing.T) {
	const namespaced = "mcp__docker__omniroute__omniroute_ccr_retrieve"
	body := noteTestBody(
		`{"role":"user","content":"see [CCR retrieve hash=0123456789abcdef01234567 chars=9000]"}`,
		`{"name":"`+namespaced+`"}`,
	)
	out := applyMarkerProtocolNote(body)
	systems := systemMessages(t, out)
	if len(systems) != 1 {
		t.Fatalf("expected one note, got %d: %s", len(systems), out)
	}
	if !strings.Contains(systems[0], namespaced) {
		t.Fatalf("note does not name the advertised tool: %q", systems[0])
	}
	if !strings.Contains(systems[0], ccrDedupClause) {
		t.Fatalf("full note lacks the dedup clause: %q", systems[0])
	}
}

// A CCR marker without a reachable tool has nothing to teach, and no marker at
// all must leave the body byte-identical.
func TestMarkerNoteNoOpWithoutExplainableMarker(t *testing.T) {
	cases := map[string][]byte{
		"no marker": noteTestBody(`{"role":"user","content":"plain"}`,
			`{"name":"omniroute_ccr_retrieve"}`),
		"ccr marker, no tool": noteTestBody(
			`{"role":"user","content":"[CCR retrieve hash=0123456789abcdef01234567 chars=9000]"}`, ""),
	}
	for name, body := range cases {
		if got := applyMarkerProtocolNote(body); string(got) != string(body) {
			t.Fatalf("%s: expected no-op, got %s", name, got)
		}
	}
}

// The note runs on every compressed body, so a body without a marker must cost
// nothing: no parse, no allocation.
func TestMarkerNoteNoAllocsWithoutMarker(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"plain"}]}`)
	allocs := testing.AllocsPerRun(100, func() {
		applyMarkerProtocolNote(body)
	})
	if allocs != 0 {
		t.Fatalf("expected 0 allocs without a marker, got %v", allocs)
	}
}

func TestMarkerNoteIsIdempotent(t *testing.T) {
	body := noteTestBody(
		`{"role":"user","content":"read [dedup:ref sha=deadbeef] again"}`,
		"",
	)
	once := applyMarkerProtocolNote(body)
	twice := applyMarkerProtocolNote(once)
	if got := strings.Count(string(twice), ccrProtocolSentinel); got != 1 {
		t.Fatalf("note stacked: %d occurrences\n%s", got, twice)
	}
}

// The note is prepended as its own message; the original messages must survive
// byte-for-byte and in order after it.
func TestMarkerNotePreservesMessages(t *testing.T) {
	const messages = `{"role":"user","content":"a"},{"role":"assistant","content":"b"},` +
		`{"role":"user","content":"[dedup:ref sha=deadbeef]"}`
	body := noteTestBody(messages, "")
	out := applyMarkerProtocolNote(body)

	doc := ojson.NewDocString(string(out))
	got := doc.Get("messages").Array()
	if len(got) != 4 {
		t.Fatalf("message count = %d, want 4: %s", len(got), out)
	}
	if got[0].Get("role").String() != "system" {
		t.Fatalf("first message is not the note: %s", got[0].Raw)
	}
	want := ojson.NewDocString(`{"messages":[` + messages + `]}`)
	for i, original := range want.Get("messages").Array() {
		if got[i+1].Raw != original.Raw {
			t.Fatalf("message %d changed\nwant: %s\ngot:  %s", i, original.Raw, got[i+1].Raw)
		}
	}
}

// A body that cannot be parsed must pass through untouched: the note is never
// worth failing a request over.
func TestMarkerNoteFailsOpen(t *testing.T) {
	for _, body := range [][]byte{
		nil,
		[]byte(``),
		[]byte(`{"messages":"not an array"}`),
		[]byte(`{"messages":[{"role":"user","content":{"opaque":true}}],"tools":[{"name":"omniroute_ccr_retrieve"}]}`),
	} {
		if got := applyMarkerProtocolNote(body); string(got) != string(body) {
			t.Fatalf("expected fail-open for %q, got %q", body, got)
		}
	}
}
