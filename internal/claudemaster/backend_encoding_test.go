package claudemaster

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const anthropicNotFoundJSON = `{"type":"error","error":{"type":"not_found_error","message":"model: claude-test-model"}}`

func gzipped(t *testing.T, s string) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func brotlied(t *testing.T, s string) []byte {
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A compressed Anthropic error (the native passthrough hands it over as sent, Content-Encoding
// and all) is decoded in the record: readable in the log, recognised as Anthropic's, relayed
// plain without the stale encoding and length headers.
func TestCompressedUpstreamErrorIsDecodedOnce(t *testing.T) {
	for name, enc := range map[string]func(*testing.T, string) []byte{"gzip": gzipped, "br": brotlied} {
		t.Run(name, func(t *testing.T) {
			headers := http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {name}, "Content-Length": {"99"}, "Cf-Ray": {"ray-1"}}
			record := backendUpstreamRecord(&bodilessUpstreamErr{status: 404, headers: headers, body: enc(t, anthropicNotFoundJSON)})
			errorType, message, ok := record.anthropicError()
			if !ok || errorType != "not_found_error" || !strings.Contains(message, "claude-test-model") {
				t.Fatalf("decoded body not read as Anthropic's error: ok=%v type=%q body=%q", ok, errorType, record.body)
			}
			if record.notFoundWithoutAnthropicError() {
				t.Fatal("a decoded Anthropic 404 must not count as bodiless")
			}
			if record.encoding != name || record.decodeError != "" {
				t.Fatalf("encoding %q decodeError %q", record.encoding, record.decodeError)
			}
			if record.headers.Get("Content-Encoding") != "" || record.headers.Get("Content-Length") != "" || record.headers.Get("Cf-Ray") != "ray-1" {
				t.Fatalf("stale representation headers must go, the rest stay: %v", record.headers)
			}
			if string(record.body) != anthropicNotFoundJSON {
				t.Fatalf("body not plain: %q", record.body)
			}
		})
	}
}

// Magic bytes decode a gzip body that arrived without a header; a declared encoding that does
// not decode leaves the body raw and says why; an unknown encoding is reported, not guessed.
func TestDecodeBodyEdges(t *testing.T) {
	plain, err := backendDecodeBody(gzipped(t, anthropicNotFoundJSON), "")
	if err != nil || string(plain) != anthropicNotFoundJSON {
		t.Fatalf("magic-byte gzip: %v %q", err, plain)
	}
	if plain, err := backendDecodeBody([]byte(anthropicNotFoundJSON), ""); err != nil || plain != nil {
		t.Fatalf("a plain body is left alone: %v %q", err, plain)
	}
	record := backendUpstreamRecord(&bodilessUpstreamErr{status: 404, headers: http.Header{"Content-Encoding": {"gzip"}}, body: []byte("not gzip at all")})
	if record.decodeError == "" || string(record.body) != "not gzip at all" || record.headers.Get("Content-Encoding") != "gzip" {
		t.Fatalf("an undecodable body is kept raw with its headers and the reason: %q %q", record.decodeError, record.body)
	}
	if _, err := backendDecodeBody([]byte("x"), "sdch"); err == nil {
		t.Fatal("an unknown encoding must be reported")
	}
	fields := backendUpstreamLogFields("auth-1", "claude-test-model", record)
	joined := ""
	for i := 0; i+1 < len(fields); i += 2 {
		joined += fields[i].(string) + "=" + strings.TrimSpace(strings.ReplaceAll(toString(fields[i+1]), "\n", " ")) + " "
	}
	if !strings.Contains(joined, "content_encoding=gzip") || !strings.Contains(joined, "decode_error=gzip:") {
		t.Fatalf("log must carry the encoding and the decode error: %s", joined)
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int:
		return strings.TrimSpace(strings.Repeat(" ", 0) + itoa(x))
	}
	return ""
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// A compressed Anthropic not_found_error is a verdict: no retry, and the refusal keeps the
// "does not serve" wording when the message names the model; when the message is about
// something else in the request, the refusal says so instead of blaming the model.
func TestCompressedAnthropicNotFoundIsNotRetriedAndWordedByItsMessage(t *testing.T) {
	quickRetry(t)
	headers := http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}}
	inner := &scriptedExecutor{answers: []error{&bodilessUpstreamErr{status: 404, headers: headers, body: gzipped(t, anthropicNotFoundJSON)}, nil}}
	ex := withBackendUpstreamRecording(inner)
	ctx := withBackendAttempt(context.Background())
	if _, err := ex.Execute(ctx, backendSeriesTestAuth("profile-a", "claude"), coreexecutor.Request{Model: "claude-test-model"}, coreexecutor.Options{}); err == nil || inner.calls != 1 {
		t.Fatalf("a compressed Anthropic not_found_error must come back at once: calls=%d err=%v", inner.calls, err)
	}
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude", names: map[string]string{"profile-a": "alpha"}}
	t.Cleanup(selector.Stop)
	recordBackendAttempt(ctx, coreauth.Result{AuthID: "profile-a", Model: "claude-test-model", Error: &coreauth.Error{HTTPStatus: http.StatusNotFound}})
	if reason := selector.boundUnavailableErrorLocked(ctx, "profile-a", "claude-test-model").Error(); !strings.Contains(reason, "does not serve model claude-test-model (HTTP 404)") {
		t.Fatalf("a not_found_error naming the model keeps its wording: %s", reason)
	}

	other := `{"type":"error","error":{"type":"not_found_error","message":"file_id file_abc not found"}}`
	ctx2 := withBackendAttempt(context.Background())
	inner2 := &scriptedExecutor{answers: []error{&bodilessUpstreamErr{status: 404, headers: headers, body: gzipped(t, other)}}}
	_, _ = withBackendUpstreamRecording(inner2).Execute(ctx2, backendSeriesTestAuth("profile-a", "claude"), coreexecutor.Request{Model: "claude-test-model"}, coreexecutor.Options{})
	recordBackendAttempt(ctx2, coreauth.Result{AuthID: "profile-a", Model: "claude-test-model", Error: &coreauth.Error{HTTPStatus: http.StatusNotFound}})
	reason := selector.boundUnavailableErrorLocked(ctx2, "profile-a", "claude-test-model").Error()
	if strings.Contains(reason, "does not serve") || !strings.Contains(reason, "about something in the request, not the model") || strings.Contains(reason, "file_abc") {
		t.Fatalf("a not_found_error about the request must say so, without upstream text: %s", reason)
	}
}
