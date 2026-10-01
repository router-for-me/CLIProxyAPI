package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// aistudioResponsesSplitTerminalSSE is the Gemini stream shape that leaves the
// Responses terminal pending: the finish reason arrives without usage metadata,
// so the converter waits for a usage tail or a [DONE] marker. The relay only
// reports stream_end, which is why the executor has to close the stream itself.
const aistudioResponsesSplitTerminalSSE = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"first"}]}}],"responseId":"resp-aistudio"}` + "\n\n" +
	`data: {"candidates":[{"finishReason":"STOP"}],"responseId":"resp-aistudio"}` + "\n\n"

// TestAIStudioStreamEndClosesResponsesTerminal pins the terminal event on the
// relay stream_end path. Without it the Responses client is told the upstream
// closed before a terminal event even though the model finished normally.
func TestAIStudioStreamEndClosesResponsesTerminal(t *testing.T) {
	const authID = "aistudio-terminal-auth"
	connected := make(chan struct{})
	var connectedOnce sync.Once
	relay := wsrelay.NewManager(wsrelay.Options{
		ProviderFactory: func(*http.Request) (string, error) {
			return authID, nil
		},
		OnConnected: func(provider string) {
			if provider == authID {
				connectedOnce.Do(func() { close(connected) })
			}
		},
	})
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	defer func() {
		if errStop := relay.Stop(context.Background()); errStop != nil {
			t.Errorf("relay stop error = %v", errStop)
		}
	}()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + relay.Path()
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Errorf("websocket close error = %v", errClose)
		}
	}()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay connection")
	}

	relayErr := make(chan error, 1)
	go func() {
		var msg wsrelay.Message
		if errRead := conn.ReadJSON(&msg); errRead != nil {
			relayErr <- fmt.Errorf("read relay request: %w", errRead)
			return
		}
		if msg.Type != wsrelay.MessageTypeHTTPReq {
			relayErr <- fmt.Errorf("relay message type = %q, want %q", msg.Type, wsrelay.MessageTypeHTTPReq)
			return
		}
		write := func(reply wsrelay.Message) error {
			reply.ID = msg.ID
			return conn.WriteJSON(reply)
		}
		if errWrite := write(wsrelay.Message{
			Type:    wsrelay.MessageTypeStreamStart,
			Payload: map[string]any{"status": float64(http.StatusOK), "headers": map[string]any{"Content-Type": "text/event-stream"}},
		}); errWrite != nil {
			relayErr <- fmt.Errorf("write stream start: %w", errWrite)
			return
		}
		if errWrite := write(wsrelay.Message{
			Type:    wsrelay.MessageTypeStreamChunk,
			Payload: map[string]any{"data": aistudioResponsesSplitTerminalSSE},
		}); errWrite != nil {
			relayErr <- fmt.Errorf("write stream chunk: %w", errWrite)
			return
		}
		if errWrite := write(wsrelay.Message{Type: wsrelay.MessageTypeStreamEnd}); errWrite != nil {
			relayErr <- fmt.Errorf("write stream end: %w", errWrite)
			return
		}
		relayErr <- nil
	}()

	exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
	result, errExecute := exec.ExecuteStream(context.Background(), &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}, cliproxyexecutor.Request{
		Model:   "gemini-3.1-pro-preview",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var terminals []gjson.Result
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			switch data.Get("type").String() {
			case "response.completed", "response.incomplete":
				terminals = append(terminals, data)
			}
		}
	}
	if errRelay := <-relayErr; errRelay != nil {
		t.Fatal(errRelay)
	}
	if len(terminals) != 1 {
		t.Fatalf("terminal events = %d, want 1", len(terminals))
	}
	terminal := terminals[0]
	if terminal.Get("type").String() != "response.completed" || terminal.Get("response.status").String() != "completed" {
		t.Fatalf("terminal = %s", terminal.Raw)
	}
	if got := terminal.Get("response.output.0.content.0.text").String(); got != "first" {
		t.Fatalf("terminal text = %q, want %q: %s", got, "first", terminal.Raw)
	}
}
