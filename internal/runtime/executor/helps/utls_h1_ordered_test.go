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
	err := writeOrderedRequest(bufio.NewWriter(&buf), req)

	// Assert
	if !errors.Is(err, errUnframableBody) {
		t.Fatalf("err = %v, want errUnframableBody", err)
	}
}

// emittedHeaderNames writes req with writeOrderedRequest and returns the header
// names in exact wire order (excluding only the request line), plus the body.
func emittedHeaderNames(t *testing.T, req *http.Request) ([]string, string) {
	t.Helper()
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	if err := writeOrderedRequest(bw, req); err != nil {
		t.Fatalf("writeOrderedRequest: %v", err)
	}
	raw := buf.String()
	head, body, _ := strings.Cut(raw, "\r\n\r\n")
	lines := strings.Split(head, "\r\n")
	var names []string
	for _, line := range lines[1:] { // skip request line
		if name, _, ok := strings.Cut(line, ": "); ok {
			names = append(names, name)
		}
	}
	return names, body
}

// TestWriteOrderedRequest_MatchesRealCaptureOrder asserts the emitted order and
// wire casing exactly reproduce a live capture of the real claude-cli 2.1.153:
// application headers case-sensitively sorted, then the transport trailer.
func TestWriteOrderedRequest_MatchesRealCaptureOrder(t *testing.T) {
	body := `{"model":"claude"}`
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", strings.NewReader(body))
	// Set in a deliberately scrambled order; Go canonicalizes the keys.
	set := map[string]string{
		"Anthropic-Version":   "2023-06-01",
		"X-Stainless-Timeout": "900",
		"anthropic-beta":      "oauth-2025-04-20",
		"Authorization":       "Bearer tok",
		"X-Stainless-Arch":    "arm64",
		"Accept":              "application/json",
		"X-Stainless-Runtime": "node",
		"anthropic-dangerous-direct-browser-access": "true",
		"User-Agent":                  "claude-cli/2.1.153 (external, cli)",
		"X-Stainless-Lang":            "js",
		"X-App":                       "cli",
		"X-Stainless-OS":              "MacOS",
		"X-Claude-Code-Session-Id":    "sid",
		"X-Stainless-Package-Version": "0.94.0",
		"X-Stainless-Retry-Count":     "0",
		"X-Stainless-Runtime-Version": "v24.3.0",
		"Content-Type":                "application/json",
		"Connection":                  "keep-alive",
		"Accept-Encoding":             "gzip, deflate, br, zstd",
	}
	for k, v := range set {
		req.Header.Set(k, v)
	}

	names, gotBody := emittedHeaderNames(t, req)
	if gotBody != body {
		t.Fatalf("body = %q, want %q", gotBody, body)
	}

	want := []string{
		"Accept", "Authorization", "Content-Type", "User-Agent",
		"X-Claude-Code-Session-Id",
		"X-Stainless-Arch", "X-Stainless-Lang", "X-Stainless-OS",
		"X-Stainless-Package-Version", "X-Stainless-Retry-Count",
		"X-Stainless-Runtime", "X-Stainless-Runtime-Version", "X-Stainless-Timeout",
		"anthropic-beta", "anthropic-dangerous-direct-browser-access", "anthropic-version",
		"x-app",
		// transport trailer
		"Connection", "Host", "Accept-Encoding", "Content-Length",
	}
	if len(names) != len(want) {
		t.Fatalf("emitted %d headers, want %d\n got: %v\nwant: %v", len(names), len(want), names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("header[%d] = %q, want %q\n got: %v\nwant: %v", i, names[i], want[i], names, want)
		}
	}
}

func TestWriteOrderedRequest_ContentLengthAndCasing(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader("abc"))
	req.Header.Set("Content-Type", "application/json")

	var buf bytes.Buffer
	if err := writeOrderedRequest(bufio.NewWriter(&buf), req); err != nil {
		t.Fatalf("writeOrderedRequest: %v", err)
	}
	out := buf.String()
	// Capitalized transport casing (matches real client), and Content-Length synthesized.
	if !strings.Contains(out, "Content-Length: 3\r\n") {
		t.Fatalf("missing Content-Length: 3 in\n%s", out)
	}
	if !strings.Contains(out, "Host: api.anthropic.com\r\n") {
		t.Fatalf("missing Host in\n%s", out)
	}
}

func TestWriteOrderedRequest_LowercaseCustomHeaders(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Anthropic-Beta", "x")
	req.Header.Set("X-App", "cli")
	var buf bytes.Buffer
	if err := writeOrderedRequest(bufio.NewWriter(&buf), req); err != nil {
		t.Fatalf("writeOrderedRequest: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"anthropic-beta: x\r\n", "x-app: cli\r\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing lowercase %q in\n%s", want, out)
		}
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
	_, _ = io.Copy(io.Discard, req.Body)
	_ = req.Body.Close()
	if !rewindBody(req) {
		t.Fatal("in-memory body should be rewindable via GetBody")
	}
	data, _ := io.ReadAll(req.Body)
	if string(data) != "payload" {
		t.Fatalf("rewound body = %q, want %q", data, "payload")
	}

	// Streaming body without GetBody → retry must be refused.
	req, _ = http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", io.NopCloser(strings.NewReader("x")))
	req.GetBody = nil
	if rewindBody(req) {
		t.Fatal("non-rewindable body must not be retried")
	}
}

func TestWriteOrderedRequest_NoBody(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.153 (external, cli)")

	var buf bytes.Buffer
	if err := writeOrderedRequest(bufio.NewWriter(&buf), req); err != nil {
		t.Fatalf("writeOrderedRequest: %v", err)
	}
	out := buf.String()
	// Request line, then the single app header, then Host in the transport trailer.
	if !strings.HasPrefix(out, "GET /v1/models HTTP/1.1\r\nUser-Agent: claude-cli/2.1.153 (external, cli)\r\nHost: api.anthropic.com\r\n") {
		t.Fatalf("unexpected request head:\n%s", out)
	}
	if !strings.HasSuffix(out, "\r\n\r\n") {
		t.Fatalf("request must end with blank line:\n%q", out)
	}
}
