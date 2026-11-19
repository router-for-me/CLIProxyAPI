package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestResponsesWebsocketCaptureFinalizesFromCloseHandshake(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name           string
		clientClose    bool
		interruptedMsg bool
		failCloseReply bool
		state          string
	}{
		{name: "normal-close", clientClose: true, state: "complete"},
		{name: "interrupted-transport", state: "partial"},
		{name: "close-during-message", clientClose: true, interruptedMsg: true, state: "partial"},
		{name: "close-reply-failed", clientClose: true, failCloseReply: true, state: "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spool := logging.NewDurableCaptureSpool(t.TempDir(), randBytesForCaptureTest(t))
			if err := spool.Lease(true); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = spool.Close() })
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil))
			done := make(chan struct{})
			router := gin.New()
			router.Use(func(c *gin.Context) {
				defer close(done)
				logging.SetGinRequestID(c, "test-capture-trace")
				c.Next()
			}, middleware.RequestCaptureMiddleware(spool))
			router.GET("/v1/responses", func(c *gin.Context) {
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				middleware.CaptureAuthenticatedRequestBody(c)
				h.ResponsesWebsocket(c)
			})
			server := httptest.NewUnstartedServer(router)
			listener := &captureCloseTestListener{Listener: server.Listener}
			server.Listener = listener
			server.Start()
			t.Cleanup(server.Close)
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			payload := []byte{0, 0xff, 0x80}
			if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				t.Fatal(err)
			}
			const testWait = 5 * time.Second
			if err := conn.SetReadDeadline(time.Now().Add(testWait)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatal(err)
			}
			fragment := []byte{0xfe, 0x00, 0x80}
			if tc.interruptedMsg {
				// A non-final client binary frame with a zero mask key.
				header := []byte{websocket.BinaryMessage, 0x80 | byte(len(fragment)), 0, 0, 0, 0}
				if _, err := conn.UnderlyingConn().Write(append(header, fragment...)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.clientClose {
				listener.failWrites.Store(tc.failCloseReply)
				if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Time{}); err != nil {
					t.Fatal(err)
				}
				wantCode := websocket.CloseNormalClosure
				if tc.failCloseReply {
					wantCode = websocket.CloseAbnormalClosure
				}
				if _, _, err := conn.ReadMessage(); !websocket.IsCloseError(err, wantCode) {
					t.Fatalf("expected close code %d, got %v", wantCode, err)
				}
			} else {
				_ = conn.Close()
			}
			select {
			case <-done:
			case <-time.After(testWait):
				t.Fatal("capture did not finalize")
			}
			var events []logging.CaptureEvent
			var after uint64
			for {
				page := spool.Page(after)
				if page.Error != "" {
					t.Fatal(page.Error)
				}
				events = append(events, page.Records...)
				if len(page.Records) < logging.CapturePageRecords {
					break
				}
				after = events[len(events)-1].Sequence
			}
			if len(events) < 4 {
				t.Fatalf("expected completed journal, got %d records", len(events))
			}
			if terminal := events[len(events)-1]; terminal.Event != "end" || terminal.State != tc.state {
				t.Fatalf("expected %s terminal capture, got %s/%s (%s)", tc.state, terminal.Event, terminal.State, terminal.Reason)
			}
			if chunk := events[1].Chunk; chunk == nil || chunk.Type != "binary" || !bytes.Equal(chunk.Data, payload) {
				t.Fatalf("binary request was not preserved: %+v", chunk)
			}
			if tc.interruptedMsg {
				chunk := events[len(events)-2].Chunk
				if chunk == nil || chunk.Type != "binary" || chunk.End || !bytes.Equal(chunk.Data, fragment) {
					t.Fatalf("unfinished binary message was not preserved as partial: %+v", chunk)
				}
			}
		})
	}
}

type captureCloseTestListener struct {
	net.Listener
	failWrites atomic.Bool
}

func (l *captureCloseTestListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return captureCloseTestConn{Conn: conn, failWrites: &l.failWrites}, nil
}

type captureCloseTestConn struct {
	net.Conn
	failWrites *atomic.Bool
}

func (c captureCloseTestConn) Write(data []byte) (int, error) {
	if c.failWrites.Load() {
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(data)
}

func randBytesForCaptureTest(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestResponsesWebsocketCaptureOptInCompressedMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, enabled := range []bool{false, true} {
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil))
		capture := logging.NewCaptureMessages(nil)
		done := make(chan struct{})
		router := gin.New()
		router.GET("/v1/responses", func(c *gin.Context) {
			if enabled {
				c.Set(logging.CaptureMessagesContextKey, capture)
				c.Set("CPA_ARCHIVE_AUTHENTICATED", true)
			}
			h.ResponsesWebsocket(c)
			close(done)
		})
		server := httptest.NewServer(router)
		dialer := websocket.Dialer{EnableCompression: true}
		conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		conn.EnableWriteCompression(true)
		// Invalid message generates a real downstream protocol error without model/provider traffic.
		payload := []byte(`{"type":"unsupported","input":"readable Codex message"}`)
		if err = conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.Fatal(err)
		}
		_, response, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if len(response) == 0 {
			t.Fatal("expected protocol response")
		}
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Time{})
		_ = conn.Close()
		<-done
		server.Close()
		r := logging.NormalizeCapture(logging.CaptureRecord{Messages: capture})
		if enabled {
			if len(r.Frames) != 2 || r.Frames[0].Payload != string(payload) || r.Frames[1].Payload != string(response) || r.Frames[1].Direction != "response" {
				t.Fatalf("messages not captured: %+v", r.Frames)
			}
		} else if len(r.Frames) != 0 {
			t.Fatal("payload captured without opt-in")
		}
	}
}
