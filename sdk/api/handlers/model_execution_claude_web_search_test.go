package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const claudeWebSearchExecutionModel = "claude-opus-5-5"

// claudeWebSearchExecutionUpstream answers a single-message request with a web
// search whose citation is minted for the calling key, and anything else with
// plain text, recording every request body.
func claudeWebSearchExecutionUpstream(t *testing.T, bodies *[][]byte, mu *sync.Mutex) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream body: %v", errRead)
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		mu.Lock()
		*bodies = append(*bodies, body)
		mu.Unlock()
		key := r.Header.Get("x-api-key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		citation := `{"type":"web_search_result_location","url":"https://example.com/a","title":"A","cited_text":"alpha","encrypted_index":"idx-minted-for-` + key + `"}`
		result := `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01exec","content":[{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"enc-minted-for-` + key + `","page_age":null}]}`
		search := len(ojson.GetBytes(body, "messages").Array()) == 1
		if !ojson.GetBytes(body, "stream").Bool() {
			content := `{"type":"text","text":"done"}`
			if search {
				content = `{"type":"server_tool_use","id":"srvtoolu_01exec","name":"web_search","input":{"query":"q"}},` +
					result + `,` +
					`{"type":"text","text":"alpha is cited","citations":[` + citation + `]}`
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_exec","type":"message","role":"assistant","model":"` + claudeWebSearchExecutionModel + `","content":[` + content + `],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}}`))
			return
		}
		events := []string{`{"type":"message_start","message":{"id":"msg_exec","type":"message","role":"assistant","model":"` + claudeWebSearchExecutionModel + `","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`}
		text := "0"
		if search {
			events = append(events,
				`{"type":"content_block_start","index":0,"content_block":`+result+`}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"","citations":[]}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"citations_delta","citation":`+citation+`}}`,
			)
			text = "1"
		} else {
			events = append(events, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		}
		events = append(events,
			`{"type":"content_block_delta","index":`+text+`,"delta":{"type":"text_delta","text":"alpha is cited"}}`,
			`{"type":"content_block_stop","index":`+text+`}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ojson.Get(event, "type").String(), event)
		}
	})
}

// TestExecuteModelClaudeExitKeepsWebSearchCitationsOnMintingCredential runs a
// Responses-format request whose exit protocol is Claude, so the Claude
// executor forwards the upstream response natively, and then replays the cited
// answer as Responses history on the same credential. The citation must survive
// the replay, which requires its owner to be recorded on the native path.
func TestExecuteModelClaudeExitKeepsWebSearchCitationsOnMintingCredential(t *testing.T) {
	user := `{"type":"message","role":"user","content":[{"type":"input_text","text":"search for alpha"}]}`
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-stream", true: "stream"}[stream], func(t *testing.T) {
			body := fmt.Sprintf(`{"model":%q,"stream":%t,"input":[%s]}`, claudeWebSearchExecutionModel, stream, user)
			runClaudeWebSearchExecutionReplay(t, stream, "openai-response", "claude", body)
		})
	}
}

// TestExecuteModelClaudeEntryKeepsWebSearchCitationsOnMintingCredential runs a
// Claude-format request whose exit protocol is OpenAI Responses, so the
// returned output carries the citation as an annotation, and then replays that
// answer as Responses history on the same credential. Ownership is a property
// of the credential, so it must be recorded whatever the entry protocol was.
func TestExecuteModelClaudeEntryKeepsWebSearchCitationsOnMintingCredential(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-stream", true: "stream"}[stream], func(t *testing.T) {
			body := fmt.Sprintf(`{"model":%q,"stream":%t,"max_tokens":1024,"messages":[{"role":"user","content":"search for alpha"}]}`, claudeWebSearchExecutionModel, stream)
			runClaudeWebSearchExecutionReplay(t, stream, "claude", "openai-response", body)
		})
	}
}

func runClaudeWebSearchExecutionReplay(t *testing.T, stream bool, entry, exit, firstBody string) {
	t.Helper()
	cache.ClearClaudeWebSearchOwners()
	t.Cleanup(cache.ClearClaudeWebSearchOwners)
	var (
		bodies [][]byte
		mu     sync.Mutex
	)
	server := httptest.NewServer(claudeWebSearchExecutionUpstream(t, &bodies, &mu))
	defer server.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
	auth := &coreauth.Auth{ID: t.Name(), Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "key-a", "base_url": server.URL}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: claudeWebSearchExecutionModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

	user := `{"type":"message","role":"user","content":[{"type":"input_text","text":"search for alpha"}]}`
	first := ModelExecutionRequest{
		EntryProtocol: entry,
		ExitProtocol:  exit,
		Model:         claudeWebSearchExecutionModel,
		Stream:        stream,
		Body:          []byte(firstBody),
	}
	var returned string
	if stream {
		result, errMsg := handler.ExecuteModelStream(context.Background(), first)
		if errMsg != nil {
			t.Fatalf("ExecuteModelStream() error = %v", errMsg.Error)
		}
		var collected strings.Builder
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error: %v", chunk.Err)
			}
			collected.Write(chunk.Payload)
		}
		returned = collected.String()
	} else {
		result, errMsg := handler.ExecuteModel(context.Background(), first)
		if errMsg != nil {
			t.Fatalf("ExecuteModel() error = %v", errMsg.Error)
		}
		returned = string(result.Body)
	}
	if !strings.Contains(returned, "idx-minted-for-key-a") {
		t.Fatalf("%s exit response lost the citation: %s", exit, returned)
	}

	history := user +
		`,{"type":"web_search_call","id":"ws_srvtoolu_01exec","status":"completed","action":{"type":"search","query":"q"},"results":[{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"enc-minted-for-key-a","page_age":null}]}` +
		`,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"alpha is cited","annotations":[{"type":"web_search_result_location","url":"https://example.com/a","title":"A","cited_text":"alpha","encrypted_index":"idx-minted-for-key-a"}]}]}` +
		`,{"type":"message","role":"user","content":[{"type":"input_text","text":"and then?"}]}`
	if _, errMsg := handler.ExecuteModel(context.Background(), ModelExecutionRequest{
		EntryProtocol: "openai-response",
		ExitProtocol:  "openai-response",
		Model:         claudeWebSearchExecutionModel,
		Body:          []byte(fmt.Sprintf(`{"model":%q,"input":[%s]}`, claudeWebSearchExecutionModel, history)),
	}); errMsg != nil {
		t.Fatalf("replay ExecuteModel() error = %v", errMsg.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(bodies))
	}
	if got := ojson.GetBytes(bodies[1], "messages.1.content.2.citations.0.encrypted_index").String(); got != "idx-minted-for-key-a" {
		t.Fatalf("minting credential lost its citation on replay: %s", ojson.GetBytes(bodies[1], "messages.1").Raw)
	}
}
