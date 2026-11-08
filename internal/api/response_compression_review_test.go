package api

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
)

func TestResponseCompressionVary(t *testing.T) {
	for _, tc := range []struct {
		name          string
		accept        string
		encoding      string
		minBytes      int
		vary          []string
		addVary       []string
		contentLength bool
		forceHeader   bool
	}{
		{name: "below_min", accept: "gzip"},
		{name: "below_min_origin", accept: "gzip", vary: []string{"Origin"}},
		{name: "below_min_known_length", accept: "gzip", contentLength: true},
		{name: "below_min_header_now", accept: "gzip", forceHeader: true},
		{name: "no_acceptable_encoding", accept: "br"},
		{name: "identity", accept: "identity"},
		{name: "identity_origin", accept: "identity", vary: []string{"Origin"}},
		{name: "br_added_token", accept: "br", addVary: []string{"accept-encoding"}},
		{name: "identity_duplicate_tokens", accept: "identity", vary: []string{"Origin, Accept-Encoding"}, addVary: []string{"accept-encoding, ACCEPT-ENCODING"}},
		{name: "excluded", accept: "gzip;q=0"},
		{name: "identity_refused_no_encoding", accept: "*;q=0, identity;q=0"},
		{name: "empty_accept"},
		{name: "no_accept_origin", vary: []string{"Origin"}},
		{name: "gzip_origin", accept: "gzip", minBytes: 1, encoding: "gzip", vary: []string{"Origin"}},
		{name: "zstd_origin", accept: "zstd", minBytes: 1, encoding: "zstd", vary: []string{"Origin"}},
		{name: "gzip_existing_token", accept: "gzip", minBytes: 1, encoding: "gzip", vary: []string{"Origin", "accept-encoding"}},
		{name: "gzip_duplicate_tokens", accept: "gzip", minBytes: 1, encoding: "gzip", vary: []string{"Origin, Accept-Encoding"}, addVary: []string{"accept-encoding"}},
		{name: "zstd_duplicate_tokens", accept: "zstd", minBytes: 1, encoding: "zstd", vary: []string{"Origin, Accept-Encoding"}, addVary: []string{"ACCEPT-ENCODING"}},
		{name: "plaintext_existing_token", accept: "gzip", vary: []string{"Origin, ACCEPT-ENCODING"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.Use(responseCompressionMiddleware(&config.Config{ServerResponseCompression: config.ResponseCompressionConfig{MinBytes: tc.minBytes}}))
			engine.GET("/", func(c *gin.Context) {
				c.Header("Content-Type", "application/json")
				if len(tc.vary) > 0 {
					c.Header("Vary", tc.vary[0])
					for _, value := range tc.vary[1:] {
						c.Writer.Header().Add("Vary", value)
					}
				}
				for _, value := range tc.addVary {
					c.Writer.Header().Add("Vary", value)
				}
				if tc.contentLength {
					c.Header("Content-Length", "2")
				}
				if tc.forceHeader {
					c.Writer.WriteHeaderNow()
				}
				if _, errWrite := c.Writer.WriteString(`{}`); errWrite != nil {
					t.Fatal(errWrite)
				}
			})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, req)
			header := response.Result().Header
			if got := header.Get("Content-Encoding"); got != tc.encoding {
				t.Errorf("Content-Encoding=%q, want %q", got, tc.encoding)
			}
			var encodingTokens, originTokens int
			for _, value := range header.Values("Vary") {
				for _, token := range strings.Split(value, ",") {
					switch strings.ToLower(strings.TrimSpace(token)) {
					case "accept-encoding":
						encodingTokens++
					case "origin":
						originTokens++
					}
				}
			}
			if encodingTokens != 1 {
				t.Errorf("Vary=%q, want exactly one Accept-Encoding token", header.Values("Vary"))
			}
			if len(tc.vary) > 0 && originTokens != 1 {
				t.Errorf("Vary=%q, want Origin preserved", header.Values("Vary"))
			}
			if got := string(compressionTestBody(t, tc.encoding, response.Body.Bytes())); got != `{}` {
				t.Errorf("body=%q, want {}", got)
			}
		})
	}
}

func TestResponseCompressionVaryPaths(t *testing.T) {
	for _, name := range []string{"disabled", "head", "websocket", "no_accept", "already_encoded", "binary", "no_content", "not_modified", "empty"} {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{}
			if name == "disabled" {
				disabled := false
				cfg.ServerResponseCompression.Enabled = &disabled
			}
			engine := gin.New()
			engine.Use(responseCompressionMiddleware(cfg))
			engine.Any("/", func(c *gin.Context) {
				c.Header("Content-Type", "application/json")
				c.Header("Vary", "Origin")
				if name == "already_encoded" {
					c.Header("Content-Encoding", "br")
				}
				if name == "binary" {
					c.Header("Content-Type", "application/octet-stream")
				}
				switch name {
				case "no_content":
					c.Status(http.StatusNoContent)
					return
				case "not_modified":
					c.Status(http.StatusNotModified)
					return
				case "empty":
					return
				}
				if _, errWrite := c.Writer.WriteString(`{}`); errWrite != nil {
					t.Fatal(errWrite)
				}
			})
			method := http.MethodGet
			if name == "head" {
				method = http.MethodHead
			}
			req := httptest.NewRequest(method, "/", nil)
			if name != "no_accept" {
				req.Header.Set("Accept-Encoding", "gzip")
			}
			if name == "websocket" {
				req.Header.Set("Upgrade", "websocket")
			}
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, req)
			wantVary := "Origin, Accept-Encoding"
			if name == "disabled" || name == "websocket" {
				wantVary = "Origin"
			}
			if got := response.Result().Header.Get("Vary"); got != wantVary {
				t.Errorf("Vary=%q, want %q", got, wantVary)
			}
		})
	}
}

func TestResponseCompressionVaryCommitPaths(t *testing.T) {
	for _, accept := range []string{"", "identity", "br", "gzip", "zstd"} {
		for _, commit := range []string{"write", "write_string", "header_now", "flush", "flush_error", "implicit"} {
			t.Run(accept+"/"+commit, func(t *testing.T) {
				engine := gin.New()
				engine.Use(responseCompressionMiddleware(&config.Config{}))
				engine.GET("/", func(c *gin.Context) {
					c.Header("Content-Type", "application/json")
					c.Writer.WriteHeader(http.StatusAccepted)
					c.Header("Vary", "Origin")
					c.Writer.Header().Add("Vary", "Accept-Encoding, accept-encoding")
					switch commit {
					case "write":
						if _, errWrite := c.Writer.Write([]byte(`{}`)); errWrite != nil {
							t.Fatal(errWrite)
						}
					case "write_string":
						if _, errWriteString := c.Writer.WriteString(`{}`); errWriteString != nil {
							t.Fatal(errWriteString)
						}
					case "header_now":
						c.Writer.WriteHeaderNow()
					case "flush":
						c.Writer.Flush()
					case "flush_error":
						if errFlush := httpwire.FlushResponse(c.Writer); errFlush != nil {
							t.Fatal(errFlush)
						}
					}
				})
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				if accept != "" {
					req.Header.Set("Accept-Encoding", accept)
				}
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, req)
				if got := response.Result().Header.Get("Vary"); got != "Origin, Accept-Encoding" {
					t.Errorf("Vary=%q, want Origin and exactly one Accept-Encoding", response.Result().Header.Values("Vary"))
				}
				if response.Code != http.StatusAccepted {
					t.Errorf("status=%d, want %d", response.Code, http.StatusAccepted)
				}
			})
		}
	}
}

func TestResponseCompressionVaryChangedWhileBuffered(t *testing.T) {
	for _, accept := range []string{"gzip", "zstd"} {
		t.Run(accept, func(t *testing.T) {
			engine := gin.New()
			engine.Use(responseCompressionMiddleware(&config.Config{}))
			engine.GET("/", func(c *gin.Context) {
				c.Header("Content-Type", "application/json")
				if _, errWriteString := c.Writer.WriteString(`{}`); errWriteString != nil {
					t.Fatal(errWriteString)
				}
				c.Header("Vary", "Origin")
			})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Accept-Encoding", accept)
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, req)
			if got := response.Result().Header.Get("Vary"); got != "Origin, Accept-Encoding" {
				t.Errorf("Vary=%q, want Origin, Accept-Encoding", got)
			}
		})
	}
}

func TestResponseCompressionPlaintextWriterContract(t *testing.T) {
	for _, accept := range []string{"", "identity", "br"} {
		t.Run(accept, func(t *testing.T) {
			engine := gin.New()
			engine.Use(responseCompressionMiddleware(&config.Config{}))
			response := httptest.NewRecorder()
			engine.GET("/", func(c *gin.Context) {
				if c.Writer.Written() || c.Writer.Size() != -1 || c.Writer.Status() != http.StatusOK {
					t.Errorf("initial state: written=%v size=%d status=%d", c.Writer.Written(), c.Writer.Size(), c.Writer.Status())
				}
				c.Writer.WriteHeader(http.StatusAccepted)
				c.Header("Vary", "Origin")
				if _, errWrite := c.Writer.Write([]byte("first")); errWrite != nil {
					t.Fatal(errWrite)
				}
				if response.Body.String() != "first" {
					t.Error("plaintext bytes were buffered")
				}
				if !c.Writer.Written() || c.Writer.Size() != len("first") || c.Writer.Status() != http.StatusAccepted {
					t.Errorf("after write: written=%v size=%d status=%d", c.Writer.Written(), c.Writer.Size(), c.Writer.Status())
				}
				c.Writer.WriteHeader(http.StatusInternalServerError)
				if _, errWriteString := c.Writer.WriteString("second"); errWriteString != nil {
					t.Fatal(errWriteString)
				}
				if response.Body.String() != "firstsecond" || c.Writer.Size() != len("firstsecond") || c.Writer.Status() != http.StatusAccepted {
					t.Error("plaintext WriteString or late WriteHeader changed writer semantics")
				}
				c.Writer.Flush()
				if !response.Flushed {
					t.Error("plaintext Flush was not forwarded")
				}
			})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if accept != "" {
				req.Header.Set("Accept-Encoding", accept)
			}
			engine.ServeHTTP(response, req)
			if got := response.Result().Header.Get("Vary"); got != "Origin, Accept-Encoding" {
				t.Errorf("Vary=%q, want Origin, Accept-Encoding", got)
			}
			if got := response.Result().Header.Get("Content-Encoding"); got != "" {
				t.Errorf("plaintext Content-Encoding=%q", got)
			}
		})
	}
}

type compressionPlaintextHijackWriter struct {
	*httptest.ResponseRecorder
	hijackErr error
	hijacked  bool
}

func (w *compressionPlaintextHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return nil, nil, w.hijackErr
}

func TestResponseCompressionPlaintextHijack(t *testing.T) {
	engine := gin.New()
	engine.Use(responseCompressionMiddleware(&config.Config{}))
	response := &compressionPlaintextHijackWriter{
		ResponseRecorder: httptest.NewRecorder(),
		hijackErr:        errors.New("test hijack result"),
	}
	engine.GET("/", func(c *gin.Context) {
		hijacker, ok := any(c.Writer).(http.Hijacker)
		if !ok {
			t.Fatal("plaintext writer does not implement http.Hijacker")
		}
		if _, _, errHijack := hijacker.Hijack(); !errors.Is(errHijack, response.hijackErr) {
			t.Errorf("Hijack error=%v, want %v", errHijack, response.hijackErr)
		}
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "identity")
	engine.ServeHTTP(response, req)
	if !response.hijacked {
		t.Error("plaintext Hijack was not forwarded")
	}
}

func TestResponseCompressionWriterContract(t *testing.T) {
	for _, minBytes := range []int{1, 1024} {
		for _, flush := range []bool{false, true} {
			t.Run("min="+strconv.Itoa(minBytes)+"/flush="+strconv.FormatBool(flush), func(t *testing.T) {
				engine := gin.New()
				engine.Use(responseCompressionMiddleware(&config.Config{ServerResponseCompression: config.ResponseCompressionConfig{MinBytes: minBytes}}))
				response := httptest.NewRecorder()
				engine.GET("/", func(c *gin.Context) {
					if c.Writer.Written() || c.Writer.Size() != -1 || c.Writer.Status() != http.StatusOK {
						t.Errorf("initial state: written=%v size=%d status=%d", c.Writer.Written(), c.Writer.Size(), c.Writer.Status())
					}
					c.Header("Content-Type", "application/json")
					c.Writer.WriteHeader(http.StatusAccepted)
					if _, errWrite := c.Writer.Write([]byte(`{"partial":`)); errWrite != nil {
						t.Fatal(errWrite)
					}
					if !c.Writer.Written() || c.Writer.Size() != len(`{"partial":`) || c.Writer.Status() != http.StatusAccepted {
						t.Errorf("after write: written=%v size=%d status=%d", c.Writer.Written(), c.Writer.Size(), c.Writer.Status())
					}
					c.Writer.WriteHeader(http.StatusInternalServerError)
					if got := c.Writer.Status(); got != http.StatusAccepted {
						t.Errorf("late WriteHeader changed status to %d", got)
					}
					if flush {
						c.Writer.Flush()
						if !response.Flushed || response.Body.Len() == 0 {
							t.Error("Flush did not commit buffered data")
						}
					}
					if _, errWriteString := c.Writer.WriteString(`true}`); errWriteString != nil {
						t.Fatal(errWriteString)
					}
					if got := c.Writer.Size(); got != len(`{"partial":true}`) {
						t.Errorf("logical size=%d, want %d", got, len(`{"partial":true}`))
					}
				})
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("Accept-Encoding", "gzip")
				engine.ServeHTTP(response, req)
				if response.Code != http.StatusAccepted {
					t.Errorf("final status=%d, want %d", response.Code, http.StatusAccepted)
				}
				if got := string(compressionTestBody(t, response.Header().Get("Content-Encoding"), response.Body.Bytes())); got != `{"partial":true}` {
					t.Errorf("body=%q", got)
				}
			})
		}
	}
}

func TestResponseCompressionWriterHeaderNowContract(t *testing.T) {
	engine := gin.New()
	engine.Use(responseCompressionMiddleware(&config.Config{}))
	engine.GET("/", func(c *gin.Context) {
		c.Writer.WriteHeader(http.StatusCreated)
		c.Writer.WriteHeaderNow()
		if !c.Writer.Written() || c.Writer.Size() != 0 || c.Writer.Status() != http.StatusCreated {
			t.Errorf("committed headers: written=%v size=%d status=%d", c.Writer.Written(), c.Writer.Size(), c.Writer.Status())
		}
		c.Writer.WriteHeader(http.StatusInternalServerError)
		if c.Writer.Status() != http.StatusCreated {
			t.Error("WriteHeader mutated committed headers")
		}
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	engine.ServeHTTP(httptest.NewRecorder(), req)
}

func TestResponseCompressionPanicFinalization(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd"} {
		for _, stage := range []string{"empty", "buffered", "committed", "flushed"} {
			t.Run(encoding+"/"+stage, func(t *testing.T) {
				engine := gin.New()
				var original gin.ResponseWriter
				var recovered bool
				engine.Use(func(c *gin.Context) { original = c.Writer; c.Next() })
				engine.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, value any) {
					recovered = true
					if value != "test compression panic" {
						t.Errorf("panic value=%v", value)
					}
					if stage == "empty" || stage == "buffered" {
						if c.Writer != original {
							t.Error("uncommitted panic did not restore original writer for recovery")
						}
					}
					c.AbortWithStatus(http.StatusInternalServerError)
				}))
				engine.Use(responseCompressionMiddleware(&config.Config{}))
				partial := `{"partial":"`
				if stage == "committed" || stage == "flushed" {
					partial += strings.Repeat("compressible data ", 400)
				}
				engine.GET("/", func(c *gin.Context) {
					c.Header("Content-Type", "application/json")
					if stage != "empty" {
						if _, errWriteString := c.Writer.WriteString(partial); errWriteString != nil {
							t.Fatal(errWriteString)
						}
					}
					if stage == "flushed" {
						c.Writer.Flush()
					}
					panic("test compression panic")
				})
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("Accept-Encoding", encoding)
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, req)
				if !recovered {
					t.Fatal("compression middleware swallowed the panic")
				}
				if stage == "empty" || stage == "buffered" {
					if response.Code != http.StatusInternalServerError || response.Body.Len() != 0 || response.Header().Get("Content-Encoding") != "" {
						t.Fatalf("expected clean 500: status=%d body=%q encoding=%q", response.Code, response.Body.Bytes(), response.Header().Get("Content-Encoding"))
					}
					return
				}
				if got := response.Header().Get("Content-Encoding"); got != encoding {
					t.Fatalf("Content-Encoding=%q, want %q", got, encoding)
				}
				if got := string(compressionTestBody(t, encoding, response.Body.Bytes())); got != partial {
					t.Fatalf("decoded partial body differs: got %d bytes, want %d", len(got), len(partial))
				}
			})
		}
	}
}
