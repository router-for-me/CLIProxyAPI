package payloadcompress

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func withLLMBackend(t *testing.T, backend func(string) string) {
	t.Helper()
	old := llmCompressorBackend
	llmCompressorBackend = backend
	t.Cleanup(func() { llmCompressorBackend = old })
}

func llmMessageContent(t *testing.T, body []byte, index int) string {
	t.Helper()
	doc := ojson.NewDocString(string(body))
	messages := doc.Get("messages")
	if !messages.IsArray() {
		t.Fatalf("messages is not an array: %s", body)
	}
	return messages.Array()[index].Get("content").String()
}

// Out of the box the backend is nil, so the engine is a pass-through.
func TestLLMDefaultBackendIsIdentity(t *testing.T) {
	withLLMBackend(t, nil)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"a long prose sentence that could shrink"}]}`)
	got := newLLMEngine().applyBody(body)
	if string(got) != string(body) {
		t.Fatalf("expected byte-identical body, got %s", got)
	}
	if newLLMEngine().name() != "llm" {
		t.Fatalf("unexpected engine name %q", newLLMEngine().name())
	}
}

// System messages are never sent to the backend; prose of other roles is.
func TestLLMCompressesProseButNotSystem(t *testing.T) {
	var seen []string
	withLLMBackend(t, func(text string) string {
		seen = append(seen, text)
		return "ok"
	})
	body := []byte(`{"messages":[` +
		`{"role":"system","content":"system instructions stay exactly as they are"},` +
		`{"role":"user","content":"a long user prose sentence that should shrink"}]}`)
	got := newLLMEngine().applyBody(body)
	if c := llmMessageContent(t, got, 0); c != "system instructions stay exactly as they are" {
		t.Fatalf("system message changed: %q", c)
	}
	if c := llmMessageContent(t, got, 1); c != "ok" {
		t.Fatalf("user message not compressed: %q", c)
	}
	for _, s := range seen {
		if strings.Contains(s, "system instructions") {
			t.Fatalf("system text reached the backend: %q", s)
		}
	}
}

// Fenced code and URLs are preserved byte for byte.
func TestLLMPreservesCodeAndURLs(t *testing.T) {
	var seen []string
	withLLMBackend(t, func(text string) string {
		seen = append(seen, text)
		return "ok"
	})
	in := "Please explain this:\n```go\nfmt.Println(x)\n```\nsee https://example.com/path for details"
	body := []byte(`{"messages":[{"role":"user","content":` + string(mustJSONString(in)) + `}]}`)
	got := newLLMEngine().applyBody(body)
	out := llmMessageContent(t, got, 0)
	if !strings.Contains(out, "```go\nfmt.Println(x)\n```") {
		t.Fatalf("fenced code was not preserved: %q", out)
	}
	if !strings.Contains(out, "https://example.com/path") {
		t.Fatalf("URL was not preserved: %q", out)
	}
	for _, s := range seen {
		if strings.Contains(s, "fmt.Println") || strings.Contains(s, "https://") {
			t.Fatalf("opaque span reached the backend: %q", s)
		}
	}
}

// Text blocks inside an array content are compressed; non-text parts survive.
func TestLLMArrayContentKeepsNonTextParts(t *testing.T) {
	withLLMBackend(t, func(string) string { return "ok" })
	body := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"a long prose sentence that should shrink"},` +
		`{"type":"image_url","image_url":{"url":"https://example.com/i.png"}}]}]}`)
	got := newLLMEngine().applyBody(body)
	doc := ojson.NewDocString(string(got))
	parts := doc.Get("messages").Array()[0].Get("content")
	if !parts.IsArray() {
		t.Fatalf("content array lost: %s", got)
	}
	items := parts.Array()
	if items[0].Get("text").String() != "ok" {
		t.Fatalf("text block not compressed: %s", items[0].Raw)
	}
	if items[1].Get("type").String() != "image_url" {
		t.Fatalf("non-text block changed: %s", items[1].Raw)
	}
}

// A shorter text is required; an expanding or equal backend leaves the body alone.
func TestLLMKeepsTextWhenBackendDoesNotShorten(t *testing.T) {
	withLLMBackend(t, func(text string) string { return text + " and more" })
	body := []byte(`{"messages":[{"role":"user","content":"short prose"}]}`)
	got := newLLMEngine().applyBody(body)
	if string(got) != string(body) {
		t.Fatalf("expected unchanged body, got %s", got)
	}
}

// Panics and malformed shapes fail open to the original body.
func TestLLMFailsOpen(t *testing.T) {
	withLLMBackend(t, func(string) string { panic("boom") })
	body := []byte(`{"messages":[{"role":"user","content":"a long prose sentence that should shrink"}]}`)
	if got := newLLMEngine().applyBody(body); string(got) != string(body) {
		t.Fatalf("panic must fail open, got %s", got)
	}
	for _, bad := range [][]byte{[]byte(`not json`), []byte(`{"messages":"nope"}`), []byte(`{"model":"m"}`)} {
		if got := newLLMEngine().applyBody(bad); string(got) != string(bad) {
			t.Fatalf("unexpected rewrite of %s: %s", bad, got)
		}
	}
}

func mustJSONString(s string) []byte {
	return appendJSONString(nil, s)
}
