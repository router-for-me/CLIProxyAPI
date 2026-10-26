package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	log "github.com/sirupsen/logrus"
)

func TestResponseCompressionServerHotReload(t *testing.T) {
	cfg := &config.Config{}
	server := newTestServerWithConfig(t, cfg)
	body := strings.Repeat("response text ", 128)
	server.engine.GET("/compression-test", func(c *gin.Context) { c.String(http.StatusOK, body) })
	for _, tc := range []struct {
		enabled  bool
		minBytes int
		encoding string
	}{
		{enabled: true, encoding: "zstd"},
		{enabled: false},
		{enabled: true, minBytes: len(body) + 1},
		{enabled: true, minBytes: 1, encoding: "zstd"},
	} {
		cfg.ServerResponseCompression.Enabled = &tc.enabled
		cfg.ServerResponseCompression.MinBytes = tc.minBytes
		server.UpdateClients(cfg)
		request := httptest.NewRequest(http.MethodGet, "/compression-test", nil)
		request.Header.Set("Accept-Encoding", "zstd")
		response := httptest.NewRecorder()
		server.engine.ServeHTTP(response, request)
		if got := response.Header().Get("Content-Encoding"); got != tc.encoding {
			t.Fatalf("after reload Content-Encoding = %q, want %q", got, tc.encoding)
		}
		if got := string(compressionTestBody(t, tc.encoding, response.Body.Bytes())); got != body {
			t.Fatal("body differs after config reload")
		}
	}
}

func TestWebsocketCompressionServerHotReload(t *testing.T) {
	cfg := &config.Config{}
	server := newTestServerWithConfig(t, cfg)
	httpServer := httptest.NewServer(server.engine)
	defer httpServer.Close()
	dialer := websocket.Dialer{EnableCompression: true}
	for _, enabled := range []bool{true, false, true} {
		cfg.Codex.WebsocketCompression = &enabled
		server.UpdateClients(cfg)
		conn, response, errDial := dialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v1/responses", nil)
		if errDial != nil {
			t.Fatal(errDial)
		}
		negotiated := strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate")
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("close server compression test websocket: %v", errClose)
		}
		if negotiated != enabled {
			t.Fatalf("after reload compression negotiated = %v, want %v", negotiated, enabled)
		}
	}
}

type compressionErrorWriter struct {
	gin.ResponseWriter
}

func (w *compressionErrorWriter) Write(data []byte) (int, error) { return 0, io.ErrClosedPipe }
func (w *compressionErrorWriter) FlushError() error              { return io.ErrClosedPipe }

func TestResponseCompressionFlushError(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Header("Content-Type", "text/event-stream")
			writer := &responseCompressionWriter{
				ResponseWriter: &compressionErrorWriter{ResponseWriter: context.Writer},
				encoding:       encoding,
				minBytes:       1024,
			}
			_, _ = writer.WriteString("data: test\n\n")
			if errFlush := httpwire.FlushResponse(writer); !errors.Is(errFlush, io.ErrClosedPipe) {
				t.Fatalf("flush error = %v", errFlush)
			}
			if _, errWrite := writer.WriteString("data: later\n\n"); !errors.Is(errWrite, io.ErrClosedPipe) {
				t.Fatalf("subsequent write error = %v", errWrite)
			}
			writer.finish()
		})
	}
}
