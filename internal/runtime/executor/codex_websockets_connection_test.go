package executor

import (
	"bufio"
	"bytes"
	"context"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// compressionFrameRecorder observes the first WebSocket frame after HTTP hijacking.
type compressionFrameRecorder struct {
	net.Conn
	firstByte chan byte
	once      sync.Once
}

func (c *compressionFrameRecorder) Read(data []byte) (int, error) {
	n, errRead := c.Conn.Read(data)
	if n > 0 {
		c.once.Do(func() { c.firstByte <- data[0] })
	}
	return n, errRead
}

type compressionHijackRecorder struct {
	http.ResponseWriter
	firstByte chan byte
}

func (w *compressionHijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffered, errHijack := w.ResponseWriter.(http.Hijacker).Hijack()
	if errHijack != nil {
		return nil, nil, errHijack
	}
	recorder := &compressionFrameRecorder{Conn: conn, firstByte: w.firstByte}
	return recorder, bufio.NewReadWriter(bufio.NewReader(recorder), buffered.Writer), nil
}

func TestWebsocketCompressionRoundTrip(t *testing.T) {
	enabled, disabled := true, false
	for _, provider := range []string{"codex", "xai"} {
		for _, tc := range []struct {
			name string
			cfg  *config.Config
			want bool
		}{
			{name: "nil_config", want: true},
			{name: "default", cfg: &config.Config{}, want: true},
			{name: "enabled", cfg: &config.Config{Codex: config.CodexConfig{WebsocketCompression: &enabled}}, want: true},
			{name: "disabled", cfg: &config.Config{Codex: config.CodexConfig{WebsocketCompression: &disabled}}},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				firstByte := make(chan byte, 1)
				serverErr := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upgrader := websocket.Upgrader{EnableCompression: true, ReadBufferSize: 1024}
					conn, errUpgrade := upgrader.Upgrade(&compressionHijackRecorder{ResponseWriter: w, firstByte: firstByte}, r, nil)
					if errUpgrade != nil {
						serverErr <- errUpgrade
						return
					}
					defer func() {
						if errClose := conn.Close(); errClose != nil {
							log.Errorf("close compression test server websocket: %v", errClose)
						}
					}()
					messageType, payload, errReadMessage := conn.ReadMessage()
					if errReadMessage != nil {
						serverErr <- errReadMessage
						return
					}
					serverErr <- conn.WriteMessage(messageType, payload)
				}))
				defer server.Close()
				wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
				auth := &cliproxyauth.Auth{ProxyURL: "direct"}
				var conn *websocket.Conn
				var closer *websocketConnectionCloser
				var response *http.Response
				var errDial error
				if provider == "codex" {
					conn, closer, response, errDial = NewCodexWebsocketsExecutor(tc.cfg).dialCodexWebsocket(context.Background(), auth, wsURL, nil)
				} else {
					conn, closer, response, errDial = NewXAIWebsocketsExecutor(tc.cfg).dialXAIWebsocket(context.Background(), auth, wsURL, nil)
				}
				if errDial != nil {
					t.Fatal(errDial)
				}
				defer func() {
					if errClose := closer.Close(); errClose != nil {
						log.Errorf("close compression test client websocket: %v", errClose)
					}
				}()
				if !strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
					t.Fatal("compression was not negotiated")
				}
				payload := make([]byte, 96*1024)
				random := rand.New(rand.NewSource(6905))
				for i := range payload {
					payload[i] = byte(' ' + random.Intn(95))
				}
				if errWriteMessage := conn.WriteMessage(websocket.TextMessage, payload); errWriteMessage != nil {
					t.Fatal(errWriteMessage)
				}
				_, echoed, errReadMessage := conn.ReadMessage()
				if errReadMessage != nil {
					t.Fatal(errReadMessage)
				}
				if !bytes.Equal(echoed, payload) {
					t.Fatal("large payload echo differs")
				}
				if errServer := <-serverErr; errServer != nil {
					t.Fatal(errServer)
				}
				if got := (<-firstByte)&0x40 != 0; got != tc.want {
					t.Fatalf("frame RSV1 = %v, want %v", got, tc.want)
				}
			})
		}
	}
}
