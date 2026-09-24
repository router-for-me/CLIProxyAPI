package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// Explicit creates can arrive before pending notifications. They must not
// supply the settings of an automatic successor while waiting to be sent.
func TestCodexDuplexAutomaticSuccessorMetadata(t *testing.T) {
	for _, scenario := range []string{"completed", "incomplete", "early_tool_result", "failed_steering", "multiple_steers", "create_before_steer"} {
		t.Run(scenario, func(t *testing.T) {
			queued := make(chan struct{})
			beforeSteer := make(chan struct{})
			done := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = c.Close() }()
				_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
				read := func() []byte {
					_, p, e := c.ReadMessage()
					if e != nil {
						t.Error(e)
					}
					return p
				}
				write := func(p string) {
					if e := c.WriteMessage(websocket.TextMessage, []byte(p)); e != nil {
						t.Error(e)
					}
				}
				response := func(kind, id, key string) {
					parent := ""
					if id == "automatic" {
						parent = "first"
					}
					write(fmt.Sprintf(`{"type":%q,"response":{"id":%q,"previous_response_id":%q,"prompt_cache_key":%q,"output":[],"incomplete_details":{"reason":"steered"}}}`, kind, id, parent, key))
				}
				first := read()
				firstKey := gjson.GetBytes(first, "prompt_cache_key").String()
				response("response.created", "first", firstKey)
				if scenario == "create_before_steer" {
					middle := read()
					if gjson.GetBytes(middle, "type").String() != "response.create" {
						t.Errorf("expected middle create: %s", middle)
						return
					}
					select {
					case <-beforeSteer:
					case <-time.After(3 * time.Second):
						t.Error("steering was not queued")
						return
					}
					middleKey := gjson.GetBytes(middle, "prompt_cache_key").String()
					response("response.completed", "first", firstKey)
					response("response.created", "middle", middleKey)
					response("response.completed", "middle", middleKey)
				}
				if p := read(); gjson.GetBytes(p, "type").String() != "response.steer" {
					t.Errorf("expected steering: %s", p)
					return
				}
				write(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"first"}}`)
				if scenario == "multiple_steers" {
					if p := read(); gjson.GetBytes(p, "type").String() != "response.steer" {
						t.Errorf("expected second steering: %s", p)
						return
					}
					write(`{"type":"response.steer.accepted","steer":{"id":"s2","previous_response_id":"first"}}`)
					write(`{"type":"response.steer.failed","steer":{"id":"s2","previous_response_id":"first","input":"second"},"error":{"code":"invalid_input","message":"second failed"}}`)
				}
				select {
				case <-queued:
				case <-time.After(3 * time.Second):
					t.Error("create was not queued")
					return
				}
				boundary := "response.completed"
				if scenario == "incomplete" {
					boundary = "response.incomplete"
				}
				if scenario != "create_before_steer" {
					response(boundary, "first", firstKey)
				}
				switch scenario {
				case "early_tool_result":
					write(`{"type":"response.steer.pending","steer":{"id":"s1","previous_response_id":"first"},"reason":"waiting_for_required_input","required_input":[{"type":"function_call_output","call_id":"c1"}]}`)
				case "failed_steering":
					write(`{"type":"response.steer.failed","steer":{"id":"s1","previous_response_id":"first","input":"first"},"error":{"code":"successor_creation_failed","message":"failed"}}`)
				default:
					response("response.created", "automatic", firstKey)
					response("response.completed", "automatic", firstKey)
				}
				next := read()
				if gjson.GetBytes(next, "type").String() != "response.create" {
					t.Errorf("expected explicit create: %s", next)
					return
				}
				nextKey := gjson.GetBytes(next, "prompt_cache_key").String()
				response("response.created", "explicit", nextKey)
				response("response.completed", "explicit", nextKey)
				_, _, _ = c.ReadMessage()
			}))
			defer upstream.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			input := make(chan core.WebsocketInput)
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = true
			cfg.CodexResponseSteering = true
			cfg.Codex.IdentityConfuse = true
			cfg.Routing.SessionAffinity = true
			executor := NewCodexWebsocketsExecutor(cfg)
			executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			credential := &auth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL, "websockets": "true"}}
			request := func(key string) []byte {
				return []byte(fmt.Sprintf(`{"type":"response.create","model":"gpt-6-astra","prompt_cache_key":%q,"previous_response_id":"first","input":[]}`, key))
			}
			result, err := executor.ExecuteStream(ctx, credential, core.Request{Model: "gpt-6-astra", Payload: request("first-key")}, core.Options{SourceFormat: translator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: t.Name()}})
			if err != nil {
				t.Fatal(err)
			}
			queuedCreate, automatic, explicit := false, false, false
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				event := gjson.GetBytes(chunk.Payload, "type").String()
				id := gjson.GetBytes(chunk.Payload, "response.id").String()
				if event == "response.created" && id == "first" {
					if scenario == "create_before_steer" {
						input <- core.WebsocketInput{Payload: request("middle-key")}
					}
					input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","previous_response_id":"first","input":"first"}`)}
					if scenario == "create_before_steer" {
						close(beforeSteer)
					}
				}
				if event == "response.steer.accepted" {
					steerID := gjson.GetBytes(chunk.Payload, "steer.id").String()
					if scenario == "multiple_steers" && steerID == "s1" {
						input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","previous_response_id":"first","input":"second"}`)}
					} else if !queuedCreate {
						queuedCreate = true
						input <- core.WebsocketInput{Payload: request("explicit-key")}
						close(queued)
					}
				}
				if event == "response.created" || event == "response.completed" {
					want := "first-key"
					if id == "explicit" {
						want = "explicit-key"
					}
					if id == "middle" {
						want = "middle-key"
					}
					if got := gjson.GetBytes(chunk.Payload, "response.prompt_cache_key").String(); got != want {
						t.Errorf("%s %s settings = %q, want %q", event, id, got, want)
					}
					if id == "automatic" && event == "response.completed" {
						automatic = true
					}
					if id == "explicit" && event == "response.completed" {
						explicit = true
						cancel()
					}
				}
			}
			wantAuto := scenario != "early_tool_result" && scenario != "failed_steering"
			if !explicit || automatic != wantAuto {
				t.Fatalf("automatic=%t want=%t explicit=%t", automatic, wantAuto, explicit)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream cleanup stalled")
			}
		})
	}
}
