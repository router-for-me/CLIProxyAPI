package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexHTTPMessagesWebsocketCloseDuringHandshake(t *testing.T) {
	for _, closeAll := range []bool{false, true} {
		t.Run(fmt.Sprintf("all=%v", closeAll), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			arrived, proceed := make(chan struct{}), make(chan struct{})
			read := make(chan error, 1)
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(arrived)
				select {
				case <-proceed:
				case <-ctx.Done():
					return
				}
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					read <- err
					return
				}
				defer func() { _ = conn.Close() }()
				_, _, err = conn.ReadMessage()
				read <- err
			}))
			t.Cleanup(server.Close)
			exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ServeHTTPMessagesOverWebsockets: true}})
			entry := codexOAuthTestAuth(server.URL)
			entry.ID = t.Name()
			entry.Attributes["websockets"] = "true"
			done := make(chan bool, 1)
			go func() {
				req := core.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`)}
				opts := core.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"X-Session-Id": {"session"}}, Metadata: map[string]any{core.CallerScopeMetadataKey: t.Name()}}
				_, err, _ := exec.serveHTTPMessages(ctx, entry, req, opts)
				done <- err != nil
			}()
			select {
			case <-arrived:
			case <-ctx.Done():
				t.Fatal("upstream handshake did not start")
			}
			if closeAll {
				exec.CloseExecutionSession(cliproxyauth.CloseAllExecutionSessionsID)
			} else {
				exec.CloseAuthExecutionSessions(entry.ID)
			}
			close(proceed)
			select {
			case err := <-read:
				if err == nil {
					t.Fatal("closed session sent a create after handshake")
				}
			case <-ctx.Done():
				t.Fatal("late socket was not closed")
			}
			select {
			case rejected := <-done:
				if !rejected {
					t.Fatal("closed session accepted the request")
				}
			case <-ctx.Done():
				t.Fatal("request did not finish")
			}
		})
	}
}

func TestCodexHTTPMessagesWebsocketToolContinuation(t *testing.T) {
	var upgrades atomic.Int32
	signatureData := make([]byte, 89)
	signatureData[0] = 0x80
	for i := 9; i < len(signatureData); i++ {
		signatureData[i] = byte(i)
	}
	signature := base64.RawURLEncoding.EncodeToString(signatureData)
	type turn struct {
		connection int32
		frame      []byte
	}
	turns := make(chan turn, 4)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			http.Error(w, "expected an upstream websocket", http.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		id := upgrades.Add(1)
		for n := 1; ; n++ {
			_, frame, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			turns <- turn{id, frame}
			respID := fmt.Sprintf("resp_%d_%d", id, n)
			item := `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}`
			if n == 1 {
				item = fmt.Sprintf(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":%q},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"probe","arguments":"{}","status":"completed"}`, signature)
			}
			events := []string{
				fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"model":"gpt-5.5","output":[]}}`, respID),
			}
			for i, output := range ojson.Parse("[" + item + "]").Array() {
				for _, kind := range []string{"response.output_item.added", "response.output_item.done"} {
					events = append(events, fmt.Sprintf(`{"type":%q,"output_index":%d,"item":%s}`, kind, i, output.Raw))
				}
			}
			events = append(events, fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"model":"gpt-5.5","status":"completed","output":[%s],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`, respID, item))
			for _, event := range events {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	cfg, err := config.LoadConfigOptional(writeCodexHTTPMessagesTestConfig(t), false)
	if err != nil {
		t.Fatal(err)
	}
	exec := NewCodexAutoExecutor(cfg)
	exec.wsExec.store = &codexWebsocketSessionStore{}
	t.Cleanup(func() { exec.wsExec.closeAllExecutionSessions("test_done") })
	auth := codexOAuthTestAuth(server.URL)
	auth.ID = "oauth-1"
	auth.Metadata["websockets"] = true
	opts := core.Options{Stream: true, SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"X-Session-Id": {"conversation-1"}}, Metadata: map[string]any{core.CallerScopeMetadataKey: "authenticated-client"}}
	run := func(messages string) string {
		t.Helper()
		payload := `{"model":"gpt-5.5","max_tokens":128,"stream":true,"tools":[{"name":"probe","description":"probe","input_schema":{"type":"object"}}],"messages":` + messages + `}`
		res, errStream := exec.ExecuteStream(t.Context(), auth, core.Request{Model: "gpt-5.5", Payload: []byte(payload)}, opts)
		if errStream != nil {
			t.Fatal(errStream)
		}
		var downstream strings.Builder
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
			downstream.Write(chunk.Payload)
		}
		return downstream.String()
	}
	const user = `{"role":"user","content":"run probe"}`
	assistant := fmt.Sprintf(`{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":%q},{"type":"tool_use","id":"call_1","name":"probe","input":{}}]}`, signature)
	const result = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"probe succeeded"}]}`
	if got := run(`[` + user + `]`); !strings.Contains(got, `"type":"tool_use"`) || !strings.Contains(got, `"type":"message_stop"`) {
		t.Fatalf("first HTTP stream is not a Messages tool call: %s", got)
	}
	first := <-turns
	if got := run(`[` + user + `,` + assistant + `,` + result + `]`); !strings.Contains(got, `"type":"message_stop"`) {
		t.Fatalf("second HTTP stream did not complete: %s", got)
	}
	second := <-turns
	if first.connection != second.connection {
		t.Fatalf("tool follow-up reconnected: first=%d second=%d", first.connection, second.connection)
	}
	frame := ojson.NewDoc(second.frame)
	if frame.Get("previous_response_id").String() != "resp_1_1" || frame.Get("input.#").Int() != 1 || frame.Get("input.0.type").String() != "function_call_output" || frame.Get("input.0.call_id").String() != "call_1" {
		t.Fatalf("follow-up must send only the tool result with its response ID: %s", second.frame)
	}
	opts.Metadata[core.CallerScopeMetadataKey] = "another-authenticated-client"
	run(`[` + user + `,` + assistant + `,` + result + `]`)
	isolated := <-turns
	if isolated.connection == first.connection || ojson.GetBytesNoCopy(isolated.frame, "previous_response_id").Exists() || ojson.GetBytesNoCopy(isolated.frame, "input.#").Int() != 4 {
		t.Fatalf("same session identifier must not cross caller boundaries: %+v", isolated)
	}
	opts.Metadata[core.CallerScopeMetadataKey] = "authenticated-client"
	opts.Headers.Set("X-Claude-Code-Agent-Id", "subagent-1")
	run(`[` + user + `]`)
	if subagent := <-turns; subagent.connection == first.connection || subagent.connection == isolated.connection {
		t.Fatal("subagent must use its own socket")
	}
}

func writeCodexHTTPMessagesTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\nupstream:\n  codex:\n    serve-http-messages-over-websockets: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
