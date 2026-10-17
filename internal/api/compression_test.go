package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httpcompression"
)

// Exercise the production middleware stack and API route over TCP, not a recorder.
func TestProductionHTTPCompressionSocket(t *testing.T) {
	server := newTestServer(t)
	socket := httptest.NewServer(server.engine)
	defer socket.Close()
	var identityBody []byte
	var identityStatus int
	for _, enc := range []string{"identity", "gzip", "br", "zstd"} {
		t.Run(enc, func(t *testing.T) {
			payload := []byte(`{"messages":[]}`)
			var body bytes.Buffer
			if enc == "identity" {
				body.Write(payload)
			} else {
				writer, _ := httpcompression.Encode(&body, enc)
				_, _ = writer.Write(payload)
				_ = writer.Close()
			}
			req, _ := http.NewRequest("POST", socket.URL+"/v1/chat/completions", &body)
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", enc)
			req.Header.Set("Accept-Encoding", enc)
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := response.Body
			if enc != "identity" {
				if response.Header.Get("Content-Encoding") != enc {
					t.Fatalf("response encoding=%q", response.Header.Get("Content-Encoding"))
				}
				reader, err = httpcompression.Decode(response.Body, enc)
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if enc == "identity" {
				identityBody = got
				identityStatus = response.StatusCode
			} else if response.StatusCode != identityStatus || !bytes.Equal(got, identityBody) {
				t.Fatalf("status=%d body=%s; identity=%d %s", response.StatusCode, got, identityStatus, identityBody)
			}
			if response.Header.Get("Vary") == "" {
				t.Fatal("missing Vary")
			}
		})
	}
}
