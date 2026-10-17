package middleware

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httpcompression"
)

func TestResponseCompressionSocketFlush(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, enc := range []string{"identity", "gzip", "br", "zstd"} {
		t.Run(enc, func(t *testing.T) {
			release := make(chan struct{})
			r := gin.New()
			r.Use(ResponseCompression())
			r.GET("/", func(c *gin.Context) {
				c.Header("Content-Type", "text/event-stream")
				_, _ = c.Writer.WriteString("data: first\n\n")
				c.Writer.Flush()
				<-release
				_, _ = c.Writer.WriteString("data: last\n\n")
			})
			s := httptest.NewServer(r)
			defer s.CloseClientConnections()
			defer s.Close()
			// Release before closing the server even on a failed assertion.
			done := make(chan error, 1)
			go func() {
				q, _ := http.NewRequest("GET", s.URL, nil)
				q.Header.Set("Accept-Encoding", enc)
				p, e := http.DefaultClient.Do(q)
				if e != nil {
					done <- e
					return
				}
				defer p.Body.Close()
				var rd io.Reader = p.Body
				if enc != "identity" {
					d, err := httpcompression.Decode(p.Body, p.Header.Get("Content-Encoding"))
					defer func() {
						if d != nil {
							_ = d.Close()
						}
					}()
					if err != nil {
						done <- err
						return
					}
					rd = d
				}
				line, errRead := bufio.NewReader(rd).ReadString('\n')
				if errRead == nil && line != "data: first\n" {
					errRead = fmt.Errorf("first SSE line = %q", line)
				}
				done <- errRead
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("first SSE event did not flush before completion")
			}
			// httptest.Close waits for handlers, so unblock before it runs.
			close(release)
		})
	}
}
func TestResponseCompressionNegotiation(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"gzip;q=0, br;q=0.5", "br"}, {"*;q=1,gzip;q=0", "zstd"}, {"gzip;q=0.5,identity;q=1", ""}, {"gzip;q=invalid", ""}} {
		if got, _ := negotiateEncoding(tc.input); got != tc.want {
			t.Errorf("%q: %q != %q", tc.input, got, tc.want)
		}
	}
}
func TestUpstreamCompressionSocket(t *testing.T) {
	for _, enc := range []string{"gzip", "br", "zstd"} {
		t.Run(enc, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("operator identity header changed")
				}
				w.Header().Set("Content-Encoding", enc)
				z, _ := httpcompression.Encode(w, enc)
				_, _ = z.Write([]byte(`{"ok":true}`))
				_ = z.Close()
			}))
			defer s.Close()
			client := &http.Client{Transport: httpcompression.Transport{}}
			q, _ := http.NewRequest("POST", s.URL, bytes.NewBufferString(`{"input":true}`))
			q.Header.Set("Accept-Encoding", "identity")
			p, e := client.Do(q)
			if e != nil {
				t.Fatal(e)
			}
			defer p.Body.Close()
			b, e := io.ReadAll(p.Body)
			if e != nil || string(b) != `{"ok":true}` || p.Header.Get("Content-Encoding") != "" {
				t.Fatalf("decoded %q: %v", b, e)
			}
		})
	}
}

func TestResponseCompressionExclusions(t *testing.T) {
	for _, tc := range []struct {
		method        string
		status        int
		header, value string
	}{
		{"HEAD", 200, "", ""}, {"GET", 204, "", ""}, {"GET", 205, "", ""}, {"GET", 304, "", ""},
		{"GET", 200, "Cache-Control", "no-transform"}, {"GET", 200, "Content-Encoding", "identity"}, {"GET", 200, "Upgrade", "websocket"},
	} {
		r := gin.New()
		r.Use(ResponseCompression())
		r.Any("/", func(c *gin.Context) {
			if tc.header != "" && tc.header != "Upgrade" {
				c.Header(tc.header, tc.value)
			}
			c.Status(tc.status)
			if tc.method != "HEAD" && tc.status == 200 {
				_, _ = c.Writer.WriteString("hello")
			}
		})
		q := httptest.NewRequest(tc.method, "/", nil)
		q.Header.Set("Accept-Encoding", "gzip")
		if tc.header == "Upgrade" {
			q.Header.Set(tc.header, tc.value)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		if got := w.Header().Get("Content-Encoding"); got != "" && got != "identity" {
			t.Errorf("%+v: encoding %q", tc, got)
		}
	}
}

func TestResponseCompressionNotAcceptable(t *testing.T) {
	r := gin.New()
	r.Use(ResponseCompression())
	r.GET("/", func(c *gin.Context) { c.String(200, "not allowed") })
	w := httptest.NewRecorder()
	q := httptest.NewRequest("GET", "/", nil)
	q.Header.Set("Accept-Encoding", "*;q=0, identity;q=0")
	r.ServeHTTP(w, q)
	if w.Code != http.StatusNotAcceptable || w.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestCapturedCompressionCodecs(t *testing.T) {
	for _, enc := range []string{"gzip", "br", "zstd"} {
		var b bytes.Buffer
		z, _ := httpcompression.Encode(&b, enc)
		_, _ = z.Write(bytes.Repeat([]byte("a"), 4096))
		_ = z.Close()
		got := decodeCapturedRequestBodyForLogWithLimit(b.Bytes(), enc, 64)
		if len(got) > 128 || !bytes.Contains(got, []byte("TRUNCATED")) {
			t.Errorf("%s log is not bounded: %q", enc, got)
		}
	}
}

func TestResponseCompressionWebSocketSocket(t *testing.T) {
	r := gin.New()
	r.Use(ResponseCompression())
	r.GET("/", func(c *gin.Context) {
		upgrader := websocket.Upgrader{}
		connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		kind, body, err := connection.ReadMessage()
		if err == nil {
			_ = connection.WriteMessage(kind, body)
		}
	})
	s := httptest.NewServer(r)
	defer s.Close()
	header := http.Header{"Accept-Encoding": []string{"gzip, br, zstd"}}
	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), header)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if response.Header.Get("Content-Encoding") != "" {
		t.Fatal("HTTP coding applied to WebSocket upgrade")
	}
	want := []byte(`{"type":"response.create"}`)
	if err := connection.WriteMessage(websocket.TextMessage, want); err != nil {
		t.Fatal(err)
	}
	_, got, err := connection.ReadMessage()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("frame=%q err=%v", got, err)
	}
}
