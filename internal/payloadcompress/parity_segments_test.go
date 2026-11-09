package payloadcompress

import "testing"

// Parity: the scanner must see assistant turns and Gemini native contents so
// session dedup can consume them (upstream bodyAdapter.ts + session-dedup).
func TestScanAssistantMessages(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":"hello there"},` +
		`{"role":"user","content":"again"}]}`
	s := &scanner{body: []byte(body)}
	if !s.scan() {
		t.Fatal("scan failed")
	}
	want := []segmentKind{kindUser, kindAssistant, kindUserLatest}
	if len(s.segs) != len(want) {
		t.Fatalf("got %d segments, want %d: %+v", len(s.segs), len(want), s.segs)
	}
	for i, kind := range want {
		if s.segs[i].kind != kind {
			t.Fatalf("segment %d kind = %d, want %d", i, s.segs[i].kind, kind)
		}
	}
}

func TestScanResponsesAssistantParts(t *testing.T) {
	body := `{"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"q2"}]}]}`
	s := &scanner{body: []byte(body)}
	if !s.scan() {
		t.Fatal("scan failed")
	}
	if len(s.segs) != 3 {
		t.Fatalf("got %d segments, want 3: %+v", len(s.segs), s.segs)
	}
	if s.segs[1].kind != kindAssistant || s.segs[1].value.String() != "a" {
		t.Fatalf("assistant part not scanned as kindAssistant: %+v", s.segs[1])
	}
}

func TestScanGeminiContents(t *testing.T) {
	body := `{"contents":[` +
		`{"role":"user","parts":[{"text":"first question"}]},` +
		`{"role":"model","parts":[` +
		`{"text":"first answer"},` +
		`{"functionCall":{"name":"f"}},` +
		`{"functionResponse":{"name":"f","response":{"content":"tool out"}}}]},` +
		`{"role":"user","parts":[{"text":"second question"}]}]}`
	s := &scanner{body: []byte(body)}
	if !s.scan() {
		t.Fatal("scan failed")
	}
	want := []segmentKind{kindUser, kindAssistant, kindUserLatest}
	if len(s.segs) != len(want) {
		t.Fatalf("got %d segments, want %d: %+v", len(s.segs), len(want), s.segs)
	}
	for i, kind := range want {
		if s.segs[i].kind != kind {
			t.Fatalf("segment %d kind = %d, want %d", i, s.segs[i].kind, kind)
		}
	}
	// Same current-turn semantics as chat: the trailing user turn is current,
	// earlier turns are not.
	if s.segs[0].current || s.segs[1].current {
		t.Fatalf("earlier turns marked current: %+v", s.segs)
	}
	if !s.segs[2].current {
		t.Fatalf("trailing user turn not current: %+v", s.segs)
	}
}

func TestScanDeveloperRoleAsSystem(t *testing.T) {
	body := `{"messages":[` +
		`{"role":"developer","content":"dev rules"},` +
		`{"role":"user","content":"hi"}]}`

	s := &scanner{body: []byte(body)}
	if !s.scan() {
		t.Fatal("scan failed")
	}
	for _, seg := range s.segs {
		if seg.kind == kindSystem {
			t.Fatalf("developer text scanned without includeSystem: %+v", seg)
		}
	}

	s = &scanner{body: []byte(body), includeSystem: true}
	if !s.scan() {
		t.Fatal("scan failed with includeSystem")
	}
	found := false
	for _, seg := range s.segs {
		if seg.kind == kindSystem && seg.value.String() == "dev rules" {
			found = true
		}
	}
	if !found {
		t.Fatalf("developer text not classified as kindSystem: %+v", s.segs)
	}
}

// Gemini thinking parts are signed over their exact text; the scanner must not
// expose them for rewriting.
func TestScanGeminiSkipsThoughtParts(t *testing.T) {
	thought := "repeat\nrepeat\nrepeat"
	body := `{"contents":[{"role":"model","parts":[` +
		`{"text":"` + thought + `","thought":true,"thoughtSignature":"sig"},` +
		`{"text":"visible answer"}]}]}`
	s := &scanner{body: []byte(body), includeSystem: true}
	if !s.scan() {
		t.Fatal("scan failed")
	}
	foundVisible := false
	for _, seg := range s.segs {
		switch seg.value.String() {
		case thought:
			t.Fatal("thinking part must not be scanned")
		case "visible answer":
			foundVisible = true
		}
	}
	if !foundVisible {
		t.Fatal("plain text part was not scanned")
	}
}
