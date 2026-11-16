package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

// A native duplex quota frame must consult the same replay guard as an error
// returned by the executor. response.created does not make quota recoverable on
// the exhausted socket. The caller still decides whether an alternative exists.
func TestForwardResponsesWebsocketDuplexQuotaConsultsReplayGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, payload      string
		quota, alternative bool
	}{
		{"quota_with_alternative", `{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"exhausted"}}`, true, true},
		{"quota_without_alternative", `{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"exhausted"}}`, true, false},
		{"cooldown_with_alternative", `{"type":"error","status":429,"error":{"code":"model_cooldown","message":"cooldown"}}`, true, true},
		{"request_error", `{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"invalid"}}`, false, false},
		{"client_access_limit", `{"type":"error","status":429,"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"client limit"}}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				data := make(chan []byte, 2)
				data <- []byte(`{"type":"response.created","response":{"id":"r-quota"}}`)
				data <- []byte(tc.payload)
				close(data)
				errs := make(chan *interfaces.ErrorMessage)
				close(errs)
				consulted := false
				_, _, _, errMsg, errForward := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(c,
					newResponsesWebsocketWriter(conn), func(...interface{}) {}, data, errs, newInMemoryWebsocketTimelineLog(), "quota-test",
					responsesWebsocketForwardOptions{duplexStream: func() bool { return true }, suppressError: func(msg *interfaces.ErrorMessage) bool {
						consulted = true
						return tc.alternative
					}})
				if consulted != tc.quota {
					done <- errors.New("native quota frame bypassed replay guard or request error invoked it")
					return
				}
				if tc.quota && errMsg == nil {
					done <- errors.New("quota frame lost terminal error")
					return
				}
				if tc.alternative && errForward != nil {
					done <- errForward
					return
				}
				done <- nil
			}))
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, _, err = conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			_, frame, secondErr := conn.ReadMessage()
			if tc.alternative && secondErr == nil {
				t.Fatalf("quota leaked despite alternative: %s", frame)
			}
			if !tc.alternative && secondErr != nil {
				t.Fatalf("terminal/request error was hidden: %v", secondErr)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
