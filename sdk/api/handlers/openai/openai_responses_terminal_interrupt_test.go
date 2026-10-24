package openai

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

type terminalInterruptTimeline func(string, []byte, time.Time)

func (appendEvent terminalInterruptTimeline) Append(event string, payload []byte, timestamp time.Time) {
	appendEvent(event, payload, timestamp)
}

// Keep the forwarder alive after its terminal payload, just as an upstream
// stream may take time to close. The local HTTP interrupt gate must be closed
// before publishing that payload and rearmed for the next duplex response.
func TestResponsesTerminalClosesLocalInterruptBeforePublication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, terminal := range []string{"response.completed", "response.done", "response.incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			local := newResponsesLocalInterrupt()
			data := make(chan []byte, 4)
			// Private usage events must be filtered without disturbing interrupt state.
			data <- []byte(`{"type":"response.subscription_usage","subscription":{"tier":"private"}}`)
			data <- []byte(`{"type":"response.created","response":{"id":"r1"}}`)
			data <- []byte(`{"type":"response.subscription_usage","subscription":{"tier":"private"}}`)
			data <- []byte(fmt.Sprintf(`{"type":%q,"response":{"id":"r1","output":[]}}`, terminal))
			atTerminal, releaseTerminal := make(chan struct{}), make(chan struct{})
			atLocalTerminal, releaseLocalTerminal := make(chan struct{}), make(chan struct{})
			var releaseOnce, releaseLocalOnce sync.Once
			var terminalOnce, localTerminalOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseTerminal) }) }
			releaseLocal := func() { releaseLocalOnce.Do(func() { close(releaseLocalTerminal) }) }
			finished := make(chan error, 1)
			timeline := terminalInterruptTimeline(func(event string, payload []byte, _ time.Time) {
				if event == "response" && gjson.GetBytes(payload, "type").String() == terminal &&
					gjson.GetBytes(payload, "response.id").String() == "r1" {
					terminalOnce.Do(func() { close(atTerminal) })
					<-releaseTerminal
				}
				if event == "response" && gjson.GetBytes(payload, "type").String() == "response.incomplete" &&
					gjson.GetBytes(payload, "response.id").String() == "r2" {
					localTerminalOnce.Do(func() { close(atLocalTerminal) })
					<-releaseLocalTerminal
				}
			})
			router := gin.New()
			router.GET("/v1/responses", func(c *gin.Context) {
				conn, err := (&websocket.Upgrader{}).Upgrade(c.Writer, c.Request, nil)
				if err != nil {
					finished <- err
					return
				}
				ctx, cancel := context.WithCancelCause(c.Request.Context())
				defer cancel(nil)
				c.Request = c.Request.WithContext(ctx)
				writer := newResponsesWebsocketWriter(conn)
				_, readerDone := readResponsesWebsocketInput(ctx, cancel, conn, func([]byte) error {
					return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
				}, local, writer, nil)
				_, _, _, errMsg, errForward := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
					c, writer, func(...interface{}) { cancel(nil) }, data, nil, timeline, "terminal-regression",
					responsesWebsocketForwardOptions{localInterrupt: local, duplexStream: func() bool { return true }},
				)
				_ = conn.Close()
				<-readerDone
				if errMsg != nil && errForward == nil {
					errForward = errMsg.Error
				}
				finished <- errForward
			})
			server := httptest.NewServer(router)
			defer server.Close()
			defer release()
			defer releaseLocal()
			client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			deadline := time.Now().Add(5 * time.Second)
			if err := client.SetReadDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			expect := func(event, id string) {
				t.Helper()
				_, payload, err := client.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(payload, "type").String() != event || gjson.GetBytes(payload, "response.id").String() != id {
					t.Fatalf("expected %s for %s, got %s", event, id, payload)
				}
			}
			expect("response.created", "r1")
			timer := time.NewTimer(time.Until(deadline))
			defer timer.Stop()
			select {
			case <-atTerminal:
			case <-timer.C:
				t.Fatal("terminal payload did not reach publication boundary")
			}
			if local.deliver([]byte(`{"type":"response.interrupt","response_id":"unknown"}`)) != responsesLocalInterruptRejected {
				t.Fatal("terminal turn still accepts local interrupts before downstream publication")
			}
			if local.deliver([]byte(`{"type":"response.interrupt","response_id":"r1"}`)) != responsesLocalInterruptAlreadyTerminal {
				t.Fatal("upstream terminal response was not recorded before downstream publication")
			}
			release()
			expect(terminal, "r1")
			if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.interrupt","response_id":"unknown"}`)); err != nil {
				t.Fatal(err)
			}
			expect("error", "")

			// The same still-open forwarder must accept a real HTTP interrupt again
			// once its next response.created is published, without closing the socket.
			data <- []byte(`{"type":"response.created","response":{"id":"r2"}}`)
			expect("response.created", "r2")
			if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.interrupt","response_id":"r2"}`)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-atLocalTerminal:
			case <-timer.C:
				t.Fatal("local interrupt did not reach terminal publication boundary")
			}
			if local.deliver([]byte(`{"type":"response.interrupt","response_id":"unknown"}`)) != responsesLocalInterruptRejected {
				t.Fatal("local interrupt still accepts interrupts before its terminal publication")
			}
			if local.deliver([]byte(`{"type":"response.interrupt","response_id":"r2"}`)) != responsesLocalInterruptAlreadyTerminal {
				t.Fatal("locally interrupted response was not recorded before downstream publication")
			}
			releaseLocal()
			expect("response.incomplete", "r2")
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-timer.C:
				t.Fatal("interrupted duplex forwarder did not exit")
			}
		})
	}
}
