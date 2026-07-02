package helps

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWriteOrderedRequest_RejectsUnframableBody(t *testing.T) {
	// Arrange: a body with unknown length (no Content-Length, no chunked) would
	// corrupt keep-alive framing and must be rejected.
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", io.NopCloser(strings.NewReader("x")))
	req.ContentLength = -1

	// Act
	var buf bytes.Buffer
	err := writeOrderedRequest(bufio.NewWriter(&buf), req, claudeHeaderOrder)

	// Assert
	if !errors.Is(err, errUnframableBody) {
		t.Fatalf("err = %v, want errUnframableBody", err)
	}
}

// emittedHeaderNames writes req with writeOrderedRequest and returns the header
// names in wire order (excluding the request line and Host), plus the body.
func emittedHeaderNames(t *testing.T, req *http.Request, order []string) ([]string, string) {
	t.Helper()
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	if err := writeOrderedRequest(bw, req, order); err != nil {
		t.Fatalf("writeOrderedRequest: %v", err)
	}
	raw := buf.String()
	parts := strings.SplitN(raw, "\r\n\r\n", 2)
	head := parts[0]
	body := ""
	if len(parts) == 2 {
		body = parts[1]
	}
	lines := strings.Split(head, "\r\n")
	var names []string
	for _, line := range lines[1:] { // skip request line
		if name, _, ok := strings.Cut(line, ": "); ok && name != "Host" {
			names = append(names, name)
		}
	}
	return names, body
}

func indexOf(names []string, target string) int {
	for i, n := range names {
		if n == target {
			return i
		}
	}
	return -1
}

func TestWriteOrderedRequest_MatchesUndiciOrder(t *testing.T) {
	// Arrange: headers set in a deliberately non-priority order.
	body := `{"model":"claude"}`
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("User-Agent", "claude-cli/2.1.63 (external, cli)")
	req.Header.Set("X-Stainless-Lang", "js")
	req.Header.Set("X-Stainless-Runtime", "node")
	req.Header.Set("X-Client-Request-Id", "req-123")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Zzz-Custom", "z")
	req.Header.Set("X-Aaa-Custom", "a")

	// Act
	names, gotBody := emittedHeaderNames(t, req, claudeHeaderOrder)

	// Assert: body preserved.
	if gotBody != body {
		t.Fatalf("body = %q, want %q", gotBody, body)
	}

	// Assert: priority order is respected (SDK/undici insertion order).
	seq := []string{"Accept", "User-Agent", "X-Stainless-Lang", "Anthropic-Version", "Authorization", "Anthropic-Beta", "X-Client-Request-Id", "Content-Type", "Content-Length"}
	prev := -1
	for _, h := range seq {
		idx := indexOf(names, h)
		if idx < 0 {
			t.Fatalf("header %q missing from output %v", h, names)
		}
		if idx <= prev {
			t.Fatalf("header %q at %d out of order in %v", h, idx, names)
		}
		prev = idx
	}

	// Assert: NOT alphabetical — Anthropic-Beta sorts before User-Agent but must
	// appear after it here (proves ordering is intentional, not Go's sort).
	if indexOf(names, "Anthropic-Beta") < indexOf(names, "User-Agent") {
		t.Fatalf("output looks alphabetically sorted (Go net/http fingerprint), got %v", names)
	}

	// Assert: non-priority headers come last, in stable sorted order.
	aIdx, zIdx := indexOf(names, "X-Aaa-Custom"), indexOf(names, "X-Zzz-Custom")
	if aIdx < indexOf(names, "Content-Type") || zIdx < aIdx {
		t.Fatalf("non-priority headers misplaced: %v", names)
	}
}

func TestWriteOrderedRequest_ContentLengthFromRequest(t *testing.T) {
	// Arrange
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader("abc"))
	req.Header.Set("Content-Type", "application/json")

	// Act
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	if err := writeOrderedRequest(bw, req, claudeHeaderOrder); err != nil {
		t.Fatalf("writeOrderedRequest: %v", err)
	}

	// Assert
	if !strings.Contains(buf.String(), "Content-Length: 3\r\n") {
		t.Fatalf("missing Content-Length: 3 in\n%s", buf.String())
	}
}

func TestRewindBody(t *testing.T) {
	// No body → retry is safe.
	req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if !rewindBody(req) {
		t.Fatal("bodyless request should be rewindable")
	}

	// bytes/strings body → net/http sets GetBody; after consumption it rewinds.
	req, _ = http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader("payload"))
	_, _ = io.Copy(io.Discard, req.Body) // simulate writeOrderedRequest consuming it
	_ = req.Body.Close()
	if !rewindBody(req) {
		t.Fatal("in-memory body should be rewindable via GetBody")
	}
	data, _ := io.ReadAll(req.Body)
	if string(data) != "payload" {
		t.Fatalf("rewound body = %q, want %q", data, "payload")
	}

	// Streaming body without GetBody → retry must be refused (would send empty body).
	req, _ = http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", io.NopCloser(strings.NewReader("x")))
	req.GetBody = nil
	if rewindBody(req) {
		t.Fatal("non-rewindable body must not be retried")
	}
}

func TestWriteOrderedRequest_NoBody(t *testing.T) {
	// Arrange: a bodyless GET must still serialize and end cleanly.
	req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.63")

	// Act
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	if err := writeOrderedRequest(bw, req, claudeHeaderOrder); err != nil {
		t.Fatalf("writeOrderedRequest: %v", err)
	}
	out := buf.String()

	// Assert
	if !strings.HasPrefix(out, "GET /v1/models HTTP/1.1\r\nHost: api.anthropic.com\r\n") {
		t.Fatalf("unexpected request head:\n%s", out)
	}
	if !strings.HasSuffix(out, "\r\n\r\n") {
		t.Fatalf("request must end with blank line:\n%q", out)
	}
	_ = io.Discard
}
