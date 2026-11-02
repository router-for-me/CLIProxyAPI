package executor

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
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// claudeAccountBoundSearchUpstream behaves like Anthropic for web search: the
// first turn of a conversation runs a search whose encrypted_content and
// citation encrypted_index are minted for the calling key, and a later request
// carrying a value minted for another key is rejected with a 400.
func claudeAccountBoundSearchUpstream(t *testing.T, bodies *[][]byte, mu *sync.Mutex) http.Handler {
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
		minted := "minted-for-" + key
		for i, message := range ojson.GetBytes(body, "messages").Array() {
			for j, block := range message.Get("content").Array() {
				for k, citation := range block.Get("citations").Array() {
					if value := citation.Get("encrypted_index").String(); value != "" && !strings.HasSuffix(value, minted) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = fmt.Fprintf(w, "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"messages.%d.content.%d.citations.%d: Invalid `encrypted_index` in `text` block\"}}", i, j, k)
						return
					}
				}
				if block.Get("type").String() != "web_search_tool_result" {
					continue
				}
				for _, result := range block.Get("content").Array() {
					if value := result.Get("encrypted_content").String(); value != "" && !strings.HasSuffix(value, minted) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = fmt.Fprintf(w, "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"messages.%d.content.%d: Invalid `encrypted_content` in `search_result` block\"}}", i, j)
						return
					}
				}
			}
		}

		citation := `{"type":"web_search_result_location","url":"https://example.com/a","title":"A","cited_text":"alpha","encrypted_index":"idx-` + minted + `"}`
		search := len(ojson.GetBytes(body, "messages").Array()) == 1
		stream := ojson.GetBytes(body, "stream").Bool()
		if !stream {
			content := `{"type":"text","text":"done"}`
			if search {
				content = `{"type":"server_tool_use","id":"srvtoolu_01owner","name":"web_search","input":{"query":"q"}},` +
					`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01owner","content":[{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"enc-` + minted + `","page_age":null}]},` +
					`{"type":"text","text":"alpha is cited","citations":[` + citation + `]}`
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_owner","type":"message","role":"assistant","model":"claude-opus-5-5","content":[` + content + `],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}}`))
			return
		}
		events := []string{`{"type":"message_start","message":{"id":"msg_owner","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`}
		index := 0
		if search {
			events = append(events,
				`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_01owner","name":"web_search","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"q\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01owner","content":[{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"enc-`+minted+`","page_age":null}]}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"","citations":[]}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":`+citation+`}}`,
			)
			index = 2
		} else {
			events = append(events, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		}
		events = append(events,
			fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":"done"}}`, index),
			fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index),
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ojson.Get(event, "type").String(), event)
		}
	})
}

// TestClaudeExecutor_WebSearchReplayStaysOnMintingCredential replays a history
// that kept a cited answer, first on the credential that ran the search and
// then on another one, as round-robin routing does once session affinity
// lapses. Codex keeps the annotations but not the search results; a client that
// also keeps the results replays their encrypted_content as well.
func TestClaudeExecutor_WebSearchReplayStaysOnMintingCredential(t *testing.T) {
	const results = `,"results":[{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"enc-minted-for-key-a","page_age":null}]`
	cases := []struct {
		name    string
		results string
	}{
		{name: "annotations only", results: ""},
		{name: "annotations and results", results: results},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(tc.name+"/"+map[bool]string{false: "non-stream", true: "stream"}[stream], func(t *testing.T) {
				runClaudeWebSearchOwnerReplay(t, stream, tc.results)
			})
		}
	}
}

func runClaudeWebSearchOwnerReplay(t *testing.T, stream bool, results string) {
	t.Helper()
	cache.ClearClaudeWebSearchOwners()
	t.Cleanup(cache.ClearClaudeWebSearchOwners)
	var (
		bodies [][]byte
		mu     sync.Mutex
	)
	server := httptest.NewServer(claudeAccountBoundSearchUpstream(t, &bodies, &mu))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	credA := &cliproxyauth.Auth{ID: "claude-a", Attributes: map[string]string{"api_key": "key-a", "base_url": server.URL}}
	credB := &cliproxyauth.Auth{ID: "claude-b", Attributes: map[string]string{"api_key": "key-b", "base_url": server.URL}}
	call := func(auth *cliproxyauth.Auth, input string) error {
		payload := []byte(fmt.Sprintf(`{"model":"claude-opus-5-5","stream":%t,"input":[%s]}`, stream, input))
		req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
		opts := cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			ResponseFormat:  sdktranslator.FormatOpenAIResponse,
			Stream:          stream,
			OriginalRequest: payload,
		}
		if !stream {
			_, errExecute := executor.Execute(context.Background(), auth, req, opts)
			return errExecute
		}
		result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
		if errStream != nil {
			return errStream
		}
		var errChunk error
		for chunk := range result.Chunks {
			if chunk.Err != nil && errChunk == nil {
				errChunk = chunk.Err
			}
		}
		return errChunk
	}

	user := `{"type":"message","role":"user","content":[{"type":"input_text","text":"search for alpha"}]}`
	if errFirst := call(credA, user); errFirst != nil {
		t.Fatalf("search turn on credential A: %v", errFirst)
	}
	history := user +
		`,{"type":"web_search_call","id":"ws_srvtoolu_01owner","status":"completed","action":{"type":"search","query":"q"}` + results + `}` +
		`,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"alpha is cited","annotations":[{"type":"web_search_result_location","url":"https://example.com/a","title":"A","cited_text":"alpha","encrypted_index":"idx-minted-for-key-a"}]}]}` +
		`,{"type":"message","role":"user","content":[{"type":"input_text","text":"and then?"}]}`

	if errSame := call(credA, history); errSame != nil {
		t.Fatalf("replay on the minting credential: %v", errSame)
	}
	if errOther := call(credB, history); errOther != nil {
		t.Fatalf("replay on another credential: %v", errOther)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("upstream requests = %d, want 3", len(bodies))
	}
	// Without its results the request translator strips the citations as well
	// (#7213), so only a history that keeps the results still carries them.
	if got := ojson.GetBytes(bodies[1], "messages.1.content.2.citations.0.encrypted_index").String(); results != "" && got != "idx-minted-for-key-a" {
		t.Fatalf("minting credential lost its citation: %s", ojson.GetBytes(bodies[1], "messages.1").Raw)
	}
	if got := ojson.GetBytes(bodies[1], "messages.1.content.1.content.0.encrypted_content").String(); results != "" && got != "enc-minted-for-key-a" {
		t.Fatalf("minting credential lost its search result: %s", ojson.GetBytes(bodies[1], "messages.1").Raw)
	}
	other := ojson.GetBytes(bodies[2], "messages.1")
	if other.Get("content.2.text").String() != "alpha is cited" || other.Get("content.2.citations").Exists() {
		t.Fatalf("other credential: text block = %s, want the text without citations", other.Get("content.2").Raw)
	}
	if got := other.Get("content.1.content").Raw; got != "[]" {
		t.Fatalf("other credential: web_search_tool_result content = %s, want []", got)
	}
}
