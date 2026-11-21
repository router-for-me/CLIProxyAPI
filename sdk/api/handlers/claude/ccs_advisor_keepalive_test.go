package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type advisorFlushRecorder struct {
	*httptest.ResponseRecorder
	firstFlush chan struct{}
	once       sync.Once
}

func (w *advisorFlushRecorder) Flush() {
	w.ResponseRecorder.Flush()
	w.once.Do(func() { close(w.firstFlush) })
}

func TestCCSAdvisorKeepAlive(t *testing.T) {
	t.Setenv("CCS_CODEX_ADVISOR_MODEL", ccsAdvisor)
	for _, tc := range []struct {
		name                           string
		stream, waitForHeartbeat, fail bool
	}{
		{"stream success after heartbeat", true, true, false},
		{"stream error after heartbeat", true, true, true},
		{"stream error before heartbeat", true, false, true},
		{"nonstream success after heartbeat", false, true, false},
		{"nonstream error after heartbeat", false, true, true},
		{"nonstream error before heartbeat", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, e := ccsHandler(t, "codex", ccsWorker, ccsAdvisor)
			cfg := h.CurrentConfig()
			cfg.Streaming.KeepAliveSeconds = 1
			cfg.Streaming.SendKeepAliveBeforeFT = true
			cfg.NonStreamKeepAliveInterval = 1
			w := &advisorFlushRecorder{ResponseRecorder: httptest.NewRecorder(), firstFlush: make(chan struct{})}
			execute := e.execute
			e.execute = func(ctx context.Context, a *coreauth.Auth, r coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
				if tc.waitForHeartbeat {
					select {
					case <-w.firstFlush:
					case <-time.After(3 * time.Second):
						t.Error("upstream waited without a flushed heartbeat")
					}
				}
				if tc.fail {
					return coreexecutor.Response{Payload: []byte(`{"type":"message","content":[null]}`)}, nil
				}
				return execute(ctx, a, r, opts)
			}
			body := ccsBody
			if !tc.stream {
				body = strings.Replace(body, `"stream":true`, `"stream":false`, 1)
			}
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			c.Request.Header.Set("X-Session-Test", "roundtrip")
			h.ClaudeMessages(c)

			got := w.Body.String()
			contentType := w.Result().Header.Get("Content-Type")
			if !tc.waitForHeartbeat {
				if w.Code != http.StatusBadGateway || contentType != "application/json" || ojson.Get(got, "type").String() != "error" {
					t.Fatalf("early error: %d %s %q", w.Code, contentType, got)
				}
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("expected status committed by heartbeat, got %d: %s", w.Code, got)
			}
			if tc.stream {
				const heartbeat = ": keep-alive\n\n"
				if contentType != "text/event-stream" || !strings.HasPrefix(got, heartbeat) {
					t.Fatalf("expected SSE heartbeat, got %s %q", contentType, got)
				}
				events := got
				for strings.HasPrefix(events, heartbeat) {
					events = strings.TrimPrefix(events, heartbeat)
				}
				if tc.fail {
					data, ok := strings.CutPrefix(events, "event: error\ndata: ")
					if !ok || ojson.Get(data, "type").String() != "error" || !strings.HasSuffix(data, "\n\n") {
						t.Fatalf("invalid terminal error: %q", events)
					}
				} else if !strings.HasPrefix(events, "event: message_start\n") || !strings.Contains(events, "event: message_stop\n") {
					t.Fatalf("invalid message stream: %q", events)
				}
			} else {
				wantType := "message"
				if tc.fail {
					wantType = "error"
				}
				if contentType != "application/json" || !strings.HasPrefix(got, "\n") || ojson.Get(got, "type").String() != wantType {
					t.Fatalf("expected heartbeat then %s, got %s %q", wantType, contentType, got)
				}
			}
		})
	}
}
