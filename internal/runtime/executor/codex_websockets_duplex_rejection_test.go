package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// The server waits for all explicit creates before rejecting one. This makes
// queue ownership deterministic without sleeps or production timeouts.
func TestCodexDuplexRejectedCreateMetadata(t *testing.T) {
	for _, scenario := range []struct {
		name                     string
		activeFailure, ambiguous bool
	}{
		{"rejected_create", false, false}, {"active_failure", true, false}, {"ambiguous_failure", true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			activeFailure, ambiguous := scenario.activeFailure, scenario.ambiguous
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = c.Close() }()
				_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
				read := func() string {
					_, p, e := c.ReadMessage()
					if e != nil {
						t.Error(e)
						return ""
					}
					return gjson.GetBytes(p, "prompt_cache_key").String()
				}
				write := func(kind, id, key string) {
					p := fmt.Sprintf(`{"type":%q,"response":{"id":%q,"prompt_cache_key":%q,"output":[],"error":{"type":"invalid_request_error","message":"rejected"}}}`, kind, id, key)
					if e := c.WriteMessage(websocket.TextMessage, []byte(p)); e != nil {
						t.Error(e)
					}
				}
				firstKey := read()
				write("response.created", "first", firstKey)
				rejectedKey, rejectedID := firstKey, "first"
				if !activeFailure {
					write("response.completed", "first", firstKey)
					rejectedKey, rejectedID = read(), "rejected"
				}
				goodKey := read()
				if ambiguous {
					write("response.failed", "", rejectedKey)
					_, _, _ = c.ReadMessage()
					return
				}
				write("response.failed", rejectedID, rejectedKey)
				write("response.created", "good", goodKey)
				write("response.completed", "good", goodKey)
				_, _, _ = c.ReadMessage()
			}))
			defer upstream.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			input := make(chan core.WebsocketInput, 2)
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = true
			cfg.CodexResponseSteering = true
			cfg.Codex.IdentityConfuse = true
			cfg.Routing.SessionAffinity = true
			exec := NewCodexWebsocketsExecutor(cfg)
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			credential := &auth.Auth{ID: "metadata-account", Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL, "websockets": "true"}}
			request := func(key string) []byte {
				return []byte(fmt.Sprintf(`{"type":"response.create","model":"gpt-6-astra","prompt_cache_key":%q,"input":[]}`, key))
			}
			result, err := exec.ExecuteStream(ctx, credential, core.Request{Model: "gpt-6-astra", Payload: request("first-key")}, core.Options{SourceFormat: translator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: t.Name()}})
			if err != nil {
				t.Fatal(err)
			}
			sent, failed, completed, terminated := false, false, false, false
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					var scoped interface{ IsRequestScoped() bool }
					if !ambiguous || !errors.As(chunk.Err, &scoped) || !scoped.IsRequestScoped() {
						t.Fatal(chunk.Err)
					}
					terminated = true
					continue
				}
				kind := gjson.GetBytes(chunk.Payload, "type").String()
				id := gjson.GetBytes(chunk.Payload, "response.id").String()
				if !sent && id == "first" && ((activeFailure && kind == "response.created") || (!activeFailure && kind == "response.completed")) {
					sent = true
					if !activeFailure {
						input <- core.WebsocketInput{Payload: request("rejected-key")}
					}
					input <- core.WebsocketInput{Payload: request("good-key")}
				}
				if kind == "response.failed" {
					failed = true
					if ambiguous {
						continue
					}
					want := "rejected-key"
					if activeFailure {
						want = "first-key"
					}
					if got := gjson.GetBytes(chunk.Payload, "response.prompt_cache_key").String(); got != want {
						t.Errorf("failure metadata = %q, want %q", got, want)
					}
				}
				if kind == "response.completed" && id == "good" {
					completed = true
					if got := gjson.GetBytes(chunk.Payload, "response.prompt_cache_key").String(); got != "good-key" {
						t.Errorf("success consumed another create's metadata: %q", got)
					}
					cancel()
				}
			}
			if ambiguous {
				if !failed || !terminated || completed {
					t.Fatalf("failed=%t terminated=%t completed=%t", failed, terminated, completed)
				}
			} else if !failed || !completed {
				t.Fatalf("failed=%t completed=%t", failed, completed)
			}
		})
	}
}

func TestCodexDuplexLaterInvalidSignatureClearsReplay(t *testing.T) {
	for _, kind := range []string{"response.failed", "error", "error_without_status"} {
		for _, started := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/started=%t", kind, started), func(t *testing.T) {
				internalcache.ClearCodexReasoningReplayCache()
				t.Cleanup(internalcache.ClearCodexReasoningReplayCache)
				encrypted := validCodexReasoningEncryptedContentForTestSeed(51)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
					read()
					write(`{"type":"response.created","response":{"id":"first","output":[]}}`)
					write(`{"type":"response.completed","response":{"id":"first","output":[]}}`)
					read()
					if started {
						write(`{"type":"response.created","response":{"id":"rejected","output":[]}}`)
					}
					switch kind {
					case "response.failed":
						write(`{"type":"response.failed","response":{"id":"rejected","error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}}`)
					case "error":
						write(`{"type":"error","status":400,"body":{"error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}}`)
					default:
						write(`{"type":"error","error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`)
					}
					corrected := read()
					if bytes.Contains(corrected, []byte(encrypted)) {
						t.Error("corrected create resent rejected encrypted reasoning")
					}
					write(`{"type":"response.created","response":{"id":"corrected","output":[]}}`)
					write(`{"type":"response.completed","response":{"id":"corrected","output":[]}}`)
					_, _, _ = c.ReadMessage()
				}))
				defer upstream.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				input := make(chan core.WebsocketInput, 1)
				ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
				cfg := &config.Config{}
				cfg.Codex.ResponseSteering = true
				cfg.CodexResponseSteering = true
				exec := NewCodexWebsocketsExecutor(cfg)
				exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				credential := &auth.Auth{ID: "replay-account", Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL, "websockets": "true"}}
				model := "gpt-6-astra"
				request := func(session string) []byte {
					metadata, _ := json.Marshal(map[string]string{"session_id": session})
					p, _ := json.Marshal(map[string]any{"type": "response.create", "model": model, "metadata": map[string]string{"user_id": string(metadata)}, "messages": []map[string]string{{"role": "user", "content": "continue"}}})
					return p
				}
				first, rejected := request("first"), request("rejected")
				opts := core.Options{SourceFormat: translator.FromString("claude"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: t.Name()}}
				for _, session := range []string{"first", "rejected", "unrelated"} {
					scope := "claude:" + session + ":agent:main"
					if !internalcache.CacheCodexReasoningReplayItem(model, scope, []byte(`{"type":"reasoning","summary":[],"encrypted_content":"`+encrypted+`"}`)) {
						t.Fatal("cache seed failed")
					}
				}
				result, err := exec.ExecuteStream(ctx, credential, core.Request{Model: model, Payload: first}, opts)
				if err != nil {
					t.Fatal(err)
				}
				failed, completed := false, false
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					event := gjson.GetBytes(chunk.Payload, "type").String()
					id := gjson.GetBytes(chunk.Payload, "response.id").String()
					if event == "response.completed" && id == "first" {
						input <- core.WebsocketInput{Payload: rejected}
					}
					if event == "response.failed" || event == "error" {
						failed = true
						if _, ok := internalcache.GetCodexReasoningReplayItem(model, "claude:rejected:agent:main"); ok {
							t.Error("rejected scope retained invalid reasoning")
						}
						for _, session := range []string{"first", "unrelated"} {
							if _, ok := internalcache.GetCodexReasoningReplayItem(model, "claude:"+session+":agent:main"); !ok {
								t.Errorf("cleared unrelated scope %s", session)
							}
						}
						input <- core.WebsocketInput{Payload: rejected}
					}
					if event == "response.completed" && id == "corrected" {
						completed = true
						cancel()
					}
				}
				if !failed || !completed {
					t.Fatalf("failed=%t corrected=%t", failed, completed)
				}
			})
		}
	}
}
