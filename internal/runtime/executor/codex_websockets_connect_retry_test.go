package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexWebsocketConnectRetriesHandshakeEOF(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failures  int32
		status    int
		wantDials int32
		wantErr   bool
	}{
		{name: "third_retry_succeeds", failures: 3, wantDials: 4},
		{name: "exhausted", failures: 10, wantDials: 4, wantErr: true},
		{name: "status_not_retried", status: http.StatusBadGateway, wantDials: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dials atomic.Int32
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := dials.Add(1)
				if tc.status != 0 {
					http.Error(w, "rejected", tc.status)
					return
				}
				if attempt <= tc.failures {
					raw, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = raw.Close()
					return
				}
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			exec := NewCodexWebsocketsExecutor(&config.Config{})
			conn, closer, resp, err := exec.dialCodexWebsocket(ctx, &cliproxyauth.Auth{ProxyURL: "direct"}, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if resp != nil {
				defer func() {
					if errClose := resp.Body.Close(); errClose != nil {
						t.Errorf("close response body: %v", errClose)
					}
				}()
			}
			if closer != nil {
				defer func() {
					if errClose := closer.Close(); errClose != nil {
						t.Errorf("close websocket: %v", errClose)
					}
				}()
			}
			if (err != nil) != tc.wantErr || (err == nil && conn == nil) {
				t.Errorf("dial error = %v, wantErr %v", err, tc.wantErr)
			}
			if got := dials.Load(); got != tc.wantDials {
				t.Errorf("dial attempts = %d, want %d", got, tc.wantDials)
			}
		})
	}
}
