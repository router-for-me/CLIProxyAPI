package api

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	log "github.com/sirupsen/logrus"
)

func compressionTestBody(t *testing.T, encoding string, body []byte) []byte {
	t.Helper()
	var reader io.Reader = bytes.NewReader(body)
	switch encoding {
	case "gzip":
		decoder, errNewReader := gzip.NewReader(reader)
		if errNewReader != nil {
			t.Fatal(errNewReader)
		}
		defer func() {
			if errClose := decoder.Close(); errClose != nil {
				log.Errorf("close test gzip decoder: %v", errClose)
			}
		}()
		reader = decoder
	case "zstd":
		decoder, errNewReader := zstd.NewReader(reader)
		if errNewReader != nil {
			t.Fatal(errNewReader)
		}
		defer decoder.Close()
		reader = decoder
	}
	decoded, errReadAll := io.ReadAll(reader)
	if errReadAll != nil {
		t.Fatal(errReadAll)
	}
	return decoded
}

func TestResponseCompression(t *testing.T) {
	body := "{\"message\":\"" + strings.Repeat("compressible text ", 128) + "\"}"
	for _, tc := range []struct {
		name             string
		accept           string
		encoding         string
		body             string
		contentType      string
		existingEncoding string
		status           int
		method           string
		contentLength    bool
		writeString      bool
		forceHeader      bool
	}{
		{name: "zstd", accept: "zstd", encoding: "zstd"},
		{name: "gzip", accept: "gzip", encoding: "gzip"},
		{name: "large_known_length", accept: "gzip", encoding: "gzip", contentLength: true},
		{name: "no_accept"},
		{name: "identity", accept: "identity"},
		{name: "excluded", accept: "gzip;q=0"},
		{name: "preference", accept: "gzip;q=1,zstd;q=0.5", encoding: "gzip"},
		{name: "preference_tie", accept: "zstd;q=0.5,gzip;q=0.5", encoding: "zstd"},
		{name: "small_identity_refused", accept: "gzip, identity;q=0", body: "{}", encoding: "gzip"},
		{name: "small_known_length_identity_refused", accept: "gzip, identity;q=0", body: "{}", encoding: "gzip", contentLength: true},
		{name: "small_wildcard_identity_refused", accept: "gzip, *;q=0", body: "{}", encoding: "gzip"},
		{name: "small_identity_overrides_wildcard", accept: "gzip, *;q=0, identity;q=1", body: "{}"},
		{name: "identity_refused_no_encoding", accept: "identity;q=0,*;q=0", body: "{}"},
		{name: "wildcard", accept: "*;q=0.5", encoding: "zstd"},
		{name: "wildcard_explicit_exclusion", accept: "*;q=1,zstd;q=0", encoding: "gzip"},
		{name: "all_excluded", accept: "*;q=1,zstd;q=0,gzip;q=0"},
		{name: "invalid_quality", accept: "zstd;q=invalid,gzip;q=2"},
		{name: "small", accept: "zstd", body: "{}"},
		{name: "small_known_length", accept: "gzip", body: "{}", contentLength: true},
		{name: "existing_encoding", accept: "gzip", existingEncoding: "br"},
		{name: "binary", accept: "zstd", contentType: "image/png"},
		{name: "json_suffix", accept: "gzip", contentType: "application/problem+json; charset=utf-8", encoding: "gzip"},
		{name: "partial", accept: "gzip", status: http.StatusPartialContent},
		{name: "no_content", accept: "gzip", status: http.StatusNoContent},
		{name: "not_modified", accept: "gzip", status: http.StatusNotModified},
		{name: "informational", accept: "gzip", status: http.StatusEarlyHints},
		{name: "head", accept: "gzip", method: http.MethodHead},
		{name: "string_writer", accept: "gzip", encoding: "gzip", writeString: true},
		{name: "explicit_header", accept: "gzip", body: "{}", forceHeader: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := body
			if tc.body != "" {
				payload = tc.body
			}
			engine := gin.New()
			engine.Use(responseCompressionMiddleware(&config.Config{}))
			engine.Any("/", func(c *gin.Context) {
				contentType := tc.contentType
				if contentType == "" {
					contentType = "application/json"
				}
				c.Header("Content-Type", contentType)
				c.Header("Vary", "Origin")
				if tc.existingEncoding != "" {
					c.Header("Content-Encoding", tc.existingEncoding)
				}
				if tc.contentLength {
					c.Header("Content-Length", strconv.Itoa(len(payload)))
				}
				if tc.status != 0 {
					c.Status(tc.status)
				}
				if tc.status != 0 && (tc.status < 200 || tc.status == http.StatusNoContent || tc.status == http.StatusNotModified) {
					c.Writer.WriteHeaderNow()
					return
				}
				if tc.forceHeader {
					c.Writer.WriteHeaderNow()
				}
				if tc.writeString {
					_, _ = io.WriteString(c.Writer, payload)
				} else {
					if _, errWrite := c.Writer.Write([]byte(payload[:len(payload)/2])); errWrite != nil {
						t.Errorf("first write: %v", errWrite)
					}
					if _, errWrite := c.Writer.Write([]byte(payload[len(payload)/2:])); errWrite != nil {
						t.Errorf("second write: %v", errWrite)
					}
				}
			})
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, "/", nil)
			req.Header.Set("Accept-Encoding", tc.accept)
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, req)
			wantEncoding := tc.encoding
			if tc.existingEncoding != "" {
				wantEncoding = tc.existingEncoding
			}
			if got := response.Header().Get("Content-Encoding"); got != wantEncoding {
				t.Fatalf("Content-Encoding = %q, want %q", got, wantEncoding)
			}
			if tc.encoding != "" {
				if got := response.Header().Get("Vary"); !strings.Contains(got, "Origin") || !strings.Contains(got, "Accept-Encoding") {
					t.Fatalf("Vary = %q", got)
				}
				if got := response.Header().Get("Content-Length"); got != "" {
					t.Fatalf("Content-Length = %q", got)
				}
			}
			if tc.contentLength && tc.encoding == "" && response.Header().Get("Content-Length") != strconv.Itoa(len(payload)) {
				t.Fatal("plaintext Content-Length was changed")
			}
			wantBody := payload
			if tc.status != 0 && (tc.status < 200 || tc.status == http.StatusNoContent || tc.status == http.StatusNotModified) {
				wantBody = ""
			}
			if got := string(compressionTestBody(t, tc.encoding, response.Body.Bytes())); got != wantBody {
				t.Fatalf("decoded body differs: got %d bytes, want %d", len(got), len(wantBody))
			}
		})
	}
}

func TestResponseCompressionDisabled(t *testing.T) {
	disabled := false
	for _, cfg := range []*config.Config{nil, {ServerResponseCompression: config.ResponseCompressionConfig{Enabled: &disabled}}} {
		engine := gin.New()
		engine.Use(responseCompressionMiddleware(cfg))
		engine.GET("/", func(c *gin.Context) { c.String(http.StatusOK, strings.Repeat("text", 1024)) })
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", "zstd")
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, req)
		if response.Header().Get("Content-Encoding") != "" {
			t.Fatal("disabled middleware compressed response")
		}
		if got := response.Result().Header.Values("Vary"); len(got) != 0 {
			t.Fatalf("disabled middleware added Vary=%q", got)
		}
	}
}

func TestResponseCompressionWebsocketUpgradeSkipped(t *testing.T) {
	engine := gin.New()
	var original gin.ResponseWriter
	engine.Use(func(c *gin.Context) { original = c.Writer; c.Next() })
	engine.Use(responseCompressionMiddleware(&config.Config{}))
	engine.GET("/", func(c *gin.Context) {
		if c.Writer != original {
			t.Error("WebSocket upgrade writer was wrapped")
		}
		c.String(http.StatusOK, strings.Repeat("text", 1024))
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, req)
	if response.Header().Get("Content-Encoding") != "" {
		t.Fatal("WebSocket upgrade response was compressed")
	}
	if got := response.Result().Header.Values("Vary"); len(got) != 0 {
		t.Fatalf("WebSocket upgrade middleware added Vary=%q", got)
	}
}

func TestResponseCompressionSSEIncremental(t *testing.T) {
	for _, encoding := range []string{"zstd", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			advance := make(chan struct{})
			engine := gin.New()
			engine.Use(responseCompressionMiddleware(&config.Config{}))
			events := []string{"data: first\n\n", "data: second\n\n", "data: third\n\n"}
			engine.GET("/", func(c *gin.Context) {
				c.Header("Content-Type", "text/event-stream")
				for _, event := range events {
					if _, errWrite := io.WriteString(c.Writer, event); errWrite != nil {
						return
					}
					if errFlush := httpwire.FlushResponse(c.Writer); errFlush != nil {
						return
					}
					select {
					case <-advance:
					case <-c.Request.Context().Done():
						return
					}
				}
			})
			server := httptest.NewServer(engine)
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, errNewRequest := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			if errNewRequest != nil {
				t.Fatal(errNewRequest)
			}
			req.Header.Set("Accept-Encoding", encoding)
			response, errDo := server.Client().Do(req)
			if errDo != nil {
				t.Fatal(errDo)
			}
			defer func() {
				if errClose := response.Body.Close(); errClose != nil {
					log.Errorf("close SSE test response: %v", errClose)
				}
			}()
			if got := response.Header.Get("Content-Encoding"); got != encoding {
				t.Fatalf("Content-Encoding = %q, want %q", got, encoding)
			}
			var reader io.Reader = response.Body
			if encoding == "gzip" {
				decoder, errNewReader := gzip.NewReader(reader)
				if errNewReader != nil {
					t.Fatal(errNewReader)
				}
				defer func() {
					if errClose := decoder.Close(); errClose != nil {
						log.Errorf("close SSE gzip decoder: %v", errClose)
					}
				}()
				reader = decoder
			} else {
				decoder, errNewReader := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1))
				if errNewReader != nil {
					t.Fatal(errNewReader)
				}
				defer decoder.Close()
				reader = decoder
			}
			buffered := bufio.NewReader(reader)
			var decoded strings.Builder
			for _, event := range events {
				line, errReadString := buffered.ReadString('\n')
				if errReadString != nil {
					t.Fatal(errReadString)
				}
				blank, errReadBlank := buffered.ReadString('\n')
				if errReadBlank != nil {
					t.Fatal(errReadBlank)
				}
				if line+blank != event {
					t.Fatalf("event = %q, want %q", line+blank, event)
				}
				decoded.WriteString(line + blank)
				advance <- struct{}{}
			}
			tail, errReadAll := io.ReadAll(buffered)
			if errReadAll != nil {
				t.Fatal(errReadAll)
			}
			decoded.Write(tail)
			if decoded.String() != strings.Join(events, "") {
				t.Fatal("decoded SSE differs")
			}
		})
	}
}
