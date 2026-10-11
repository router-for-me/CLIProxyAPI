package claudemaster

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func compressFor(t *testing.T, encoding, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.WriteCloser
	switch encoding {
	case "gzip":
		w = gzip.NewWriter(&buf)
	case "br":
		w = brotli.NewWriter(&buf)
	case "deflate":
		w = zlib.NewWriter(&buf)
	case "deflate-raw":
		fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
		if err != nil {
			t.Fatal(err)
		}
		w = fw
	case "zstd":
		enc, err := zstd.NewWriter(nil)
		if err != nil {
			t.Fatal(err)
		}
		return enc.EncodeAll([]byte(body), nil)
	default:
		t.Fatalf("no encoder for %s", encoding)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func scanEncoded(status int, contentType, encoding string, chunks ...[]byte) (tokenCounts, bool) {
	var u usageScanner
	u.start(status, contentType, encoding)
	for _, chunk := range chunks {
		u.write(chunk)
	}
	return u.result()
}

func splitEvery(b []byte, n int) [][]byte {
	var out [][]byte
	for len(b) > n {
		out = append(out, b[:n])
		b = b[n:]
	}
	return append(out, b)
}

// Anthropic answers compressed when the client allows it and the native path forwards the bytes as
// sent: every encoding is decoded for the count, wherever the compressed bytes are split.
func TestUsageScannerReadsCompressedBodies(t *testing.T) {
	for _, enc := range []string{"gzip", "br", "zstd", "deflate", "deflate-raw"} {
		header := enc
		if enc == "deflate-raw" {
			header = "deflate"
		}
		t.Run(enc, func(t *testing.T) {
			stream := compressFor(t, enc, tokenTestSSE)
			for _, size := range []int{1, 7, 64, len(stream)} {
				if got, ok := scanEncoded(200, "text/event-stream", header, splitEvery(stream, size)...); !ok || got != tokenTestWant {
					t.Fatalf("stream in %d-byte chunks: %+v, %v", size, got, ok)
				}
			}
			body := compressFor(t, enc, tokenTestJSON)
			if got, ok := scanEncoded(200, "application/json", header, splitEvery(body, 5)...); !ok || got != tokenTestWant {
				t.Fatalf("json: %+v, %v", got, ok)
			}
		})
	}
	// a chain is undone in reverse: gzip applied first, then br
	chained := compressFor(t, "br", string(compressFor(t, "gzip", tokenTestSSE)))
	if got, ok := scanEncoded(200, "text/event-stream", "gzip, br", chained); !ok || got != tokenTestWant {
		t.Fatalf("chain: %+v, %v", got, ok)
	}
}

// A body that claims an encoding it does not have, or one we cannot decode, counts nothing and
// never holds up the writes that carry it to the client.
func TestUsageScannerNeverBlocksOnABodyThatDoesNotDecode(t *testing.T) {
	garbage := bytes.Repeat([]byte("not compressed at all\n"), 50000) // about 1 MiB
	for _, enc := range []string{"gzip", "br", "zstd", "deflate", "sdch"} {
		t.Run(enc, func(t *testing.T) {
			done := make(chan struct{})
			var ok bool
			go func() {
				_, ok = scanEncoded(200, "text/event-stream", enc, splitEvery(garbage, 4096)...)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("writes blocked on a body that does not decode")
			}
			if ok {
				t.Fatal("an undecodable body was counted")
			}
		})
	}
	// identity is no encoding at all
	if got, ok := scanEncoded(200, "text/event-stream", "identity", []byte(tokenTestSSE)); !ok || got != tokenTestWant {
		t.Fatalf("identity: %+v, %v", got, ok)
	}
}

// compressedTokenUpstream answers like Anthropic does for a client that accepts compression.
type compressedTokenUpstream struct {
	t        *testing.T
	encoding string
}

func (u *compressedTokenUpstream) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return u }

func (u *compressedTokenUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	contentType, plain := "application/json", tokenTestJSON
	if strings.Contains(string(body), `"stream":true`) {
		contentType, plain = "text/event-stream", tokenTestSSE
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}, "Content-Encoding": {u.encoding}},
		Body: io.NopCloser(bytes.NewReader(compressFor(u.t, u.encoding, plain))), Request: r}, nil
}

// Through the real backend and native executor with a compressed upstream answer: the client gets
// Anthropic's bytes and encoding unchanged, and the tokens are counted.
func TestTokensOfACompressedAnswerAreCounted(t *testing.T) {
	for _, enc := range []string{"gzip", "br"} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%v", enc, stream), func(t *testing.T) {
				reader := startMetrics(t, map[string]string{testAccountUUID: "colton"})
				backend := newBackendNativeResponseFixture(t)
				backend.seriesSelector.names = map[string]string{backend.authIDs[0]: "claude-connor"}
				backend.manager.SetRoundTripperProvider(&compressedTokenUpstream{t: t, encoding: enc})
				rec := serve(t, backend, inferenceRequest(testAccountUUID, stream))
				plain := tokenTestJSON
				if stream {
					plain = tokenTestSSE
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("status %d", rec.Code)
				}
				if got := rec.Header().Get("Content-Encoding"); got == enc {
					if decoded := decodeForTest(t, enc, rec.Body.Bytes()); decoded != plain {
						t.Fatalf("the client's body does not decode to Anthropic's: %q", decoded)
					}
				} else if rec.Body.String() != plain {
					t.Fatalf("encoding %q dropped but the body is not the plain answer: %q", got, rec.Body.String())
				}
				rm := collect(t, reader)
				for typ, n := range map[string]int64{"input": 11, "output": 25, "cache_read": 300, "cache_creation": 40} {
					if got := sumOf(rm, "claude_master.inference.tokens", map[string]string{"profile": "claude-connor", "type": typ}); got != n {
						t.Errorf("tokens{type=%s} = %d, want %d", typ, got, n)
					}
				}
			})
		}
	}
}

func decodeForTest(t *testing.T, enc string, b []byte) string {
	t.Helper()
	var r io.Reader
	switch enc {
	case "gzip":
		gz, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		r = gz
	case "br":
		r = brotli.NewReader(bytes.NewReader(b))
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
