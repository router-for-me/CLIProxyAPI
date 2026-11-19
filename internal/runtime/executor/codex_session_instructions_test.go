package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func managedInstructionsOptions(t *testing.T) core.Options {
	return core.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Headers:      http.Header{"Session-Id": {t.Name()}},
		Metadata:     map[string]any{core.CallerScopeMetadataKey: "test-caller", core.ExecutionSessionMetadataKey: t.Name()},
	}
}

func TestCodexSessionInstructionsHTTP(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth func(string) *cliproxyauth.Auth
	}{{"oauth", codexOAuthTestAuth}, {"api-key", codexAPIKeyTestAuth}} {
		t.Run(tc.name, func(t *testing.T) {
			tcAuth := tc.auth
			server, captured, _ := newCodexHostedImageTestServer(t, "http")
			cfg := &config.Config{}
			cfg.Payload.Override = []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"input.0.content.0.text": " A ", "instructions": "override"}}}},
				Params: map[string]any{"input.0.content.0.text": "configured", "instructions": "configured"},
			}}
			exec := NewCodexExecutor(cfg)
			opts := managedInstructionsOptions(t)
			for i, tc := range []struct {
				body, baseline, instructions string
				count                        int
			}{
				{`{"input":"hi","instructions":" A ","client_metadata":{"cpa_session_instructions":"start","other":"keep"}}`, " A ", "", 2},
				{`{"input":"hi","instructions":" A ","client_metadata":{"cpa_session_instructions":"reuse"}}`, " A ", "", 2},
				{`{"input":"hi","instructions":"A","client_metadata":{"cpa_session_instructions":"reuse"}}`, " A ", "A", 2},
				{`{"input":"hi","client_metadata":{"cpa_session_instructions":"reuse"}}`, " A ", "", 2},
				{`{"input":"hi","instructions":null,"client_metadata":{"cpa_session_instructions":"reuse"}}`, " A ", "", 2},
				{`{"input":"hi","instructions":"","client_metadata":{"cpa_session_instructions":"reuse"}}`, " A ", "", 2},
				{`{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":" A "}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"client_metadata":{"cpa_session_instructions":"reuse"}}`, " A ", "", 2},
				{`{"input":"hi","instructions":"override","client_metadata":{"cpa_session_instructions":"reuse"}}`, "configured", "configured", 2},
				{`{"input":"hi","instructions":" A "}`, "hi", " A ", 1},
			} {
				req := core.Request{Model: "gpt-5.4", Payload: []byte(tc.body)}
				if i%2 == 0 {
					if _, errExecute := exec.Execute(t.Context(), tcAuth(server.URL), req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
				} else {
					res, errStream := exec.ExecuteStream(t.Context(), tcAuth(server.URL), req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				}
				body := ojson.NewDoc((<-captured).body)
				if body.Get("input.0.content.0.text").String() != tc.baseline || body.Get("input.#").Int() != int64(tc.count) || body.Get("instructions").String() != tc.instructions || body.Get("client_metadata.cpa_session_instructions").Exists() {
					t.Fatalf("turn %d: unexpected upstream request: %s", i, body.Bytes())
				}
				if i == 0 && body.Get("client_metadata.other").String() != "keep" {
					t.Fatal("unrelated client metadata was removed")
				}
			}
			req := core.Request{Model: "gpt-5.4", Payload: []byte(`{"instructions":"B","input":[],"client_metadata":{"cpa_session_instructions":"reuse"}}`)}
			check := func(req core.Request, opts core.Options, status int) {
				t.Helper()
				_, errExecute := exec.Execute(t.Context(), tcAuth(server.URL), req, opts)
				var errStatus interface{ StatusCode() int }
				if !errors.As(errExecute, &errStatus) || errStatus.StatusCode() != status {
					t.Fatalf("expected %d, got %v", status, errExecute)
				}
				select {
				case <-captured:
					t.Fatal("invalid request reached upstream")
				default:
				}
			}
			other := managedInstructionsOptions(t)
			other.Metadata[core.CallerScopeMetadataKey] = "different-caller"
			check(req, other, http.StatusConflict)
			delete(other.Metadata, core.CallerScopeMetadataKey)
			check(req, other, http.StatusBadRequest)
			req.Payload = []byte(`{"instructions":"B","input":[],"previous_response_id":"r0","client_metadata":{"cpa_session_instructions":"start"}}`)
			check(req, opts, http.StatusConflict)
			unreconstructed := opts
			unreconstructed.OriginalRequest = req.Payload
			req.Payload = []byte(`{"instructions":"B","input":[],"client_metadata":{"cpa_session_instructions":"reuse"}}`)
			check(req, unreconstructed, http.StatusConflict)
			req.Payload = []byte(`{"input":[],"client_metadata":{"other":"ok"},"client_metadata":{"cpa_session_instructions":"reuse"}}`)
			check(req, opts, http.StatusBadRequest)
		})
	}
}

func newManagedInstructionsWSServer(t *testing.T) (*httptest.Server, <-chan []byte) {
	t.Helper()
	captured := make(chan []byte, 10)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Log(errClose)
			}
		}()
		for i := 0; ; i++ {
			_, frame, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			captured <- frame
			for _, event := range []string{"response.created", "response.completed"} {
				msg := fmt.Sprintf(`{"type":%q,"response":{"id":"r%d","status":"completed","output":[]}}`, event, i)
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(msg)); errWrite != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func TestCodexSessionInstructionsWebsocket(t *testing.T) {
	for _, mode := range []string{"stream", "nonstream", "duplex", "filtered-prefix", "filtered-parent"} {
		t.Run(mode, func(t *testing.T) {
			server, captured := newManagedInstructionsWSServer(t)
			cfg := &config.Config{Codex: config.CodexConfig{ResponseSteering: mode == "duplex"}}
			if mode == "filtered-prefix" {
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"input.0.content.0.text": "filtered"}}}
			} else if mode == "filtered-parent" {
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"previous_response_id": "r0"}}}}, Params: map[string]any{"previous_response_id": "configured"}}}
			}
			exec := NewCodexWebsocketsExecutor(cfg)
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			defer exec.CloseExecutionSession(t.Name())
			opts := managedInstructionsOptions(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			input := make(chan core.WebsocketInput, 1)
			var duplex *core.StreamResult
			if mode == "duplex" {
				ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			}
			auth := codexAPIKeyTestAuth(server.URL)
			turn := func(payload string, opts core.Options, reject bool) {
				t.Helper()
				req := core.Request{Model: "gpt-5.4", Payload: []byte(payload)}
				var err error
				switch {
				case mode == "nonstream":
					_, err = exec.Execute(ctx, auth, req, opts)
				case mode == "duplex":
					if duplex == nil {
						duplex, err = exec.ExecuteStream(ctx, auth, req, opts)
					} else {
						input <- core.WebsocketInput{Payload: req.Payload}
					}
					if err == nil {
						for chunk := range duplex.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							kind := ojson.GetBytes(chunk.Payload, "type").String()
							if kind == "error" {
								if !reject || ojson.GetBytes(chunk.Payload, "error.code").String() != "previous_response_not_found" {
									t.Fatalf("unexpected error %s", chunk.Payload)
								}
								return
							}
							if kind == "response.completed" {
								if reject {
									t.Fatal("invalid delta reached upstream")
								}
								return
							}
						}
						t.Fatal("duplex stream ended early")
					}
				default:
					var result *core.StreamResult
					result, err = exec.ExecuteStream(ctx, auth, req, opts)
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					}
				}
				if reject {
					if !core.IsUpstreamWebsocketTurnError(err) {
						t.Fatalf("want full replay error, got %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			turn(`{"type":"response.create","input":"root","instructions":"A","client_metadata":{"cpa_session_instructions":"start"}}`, opts, false)
			root := ojson.NewDoc(<-captured)
			wantPrefix := "A"
			if mode == "filtered-prefix" {
				wantPrefix = "filtered"
			}
			if root.Get("input.0.role").String() != "developer" || root.Get("input.0.content.0.text").String() != wantPrefix || root.Get("instructions").String() != "" || root.Get("client_metadata.cpa_session_instructions").Exists() {
				t.Fatalf("root was not rewritten: %s", root.Bytes())
			}
			delta := `{"type":"response.append","input":"next","client_metadata":{"cpa_session_instructions":"reuse"},"instructions":"B"}`
			unsafe := mode == "filtered-prefix" || mode == "filtered-parent"
			turn(delta, opts, unsafe)
			if !unsafe {
				body := ojson.NewDoc(<-captured)
				if body.Get("previous_response_id").String() != "r0" || body.Get("input.#").Int() != 1 || body.Get("input.0.role").String() != "user" || body.Get("instructions").String() != "B" || body.Get("client_metadata.cpa_session_instructions").Exists() {
					t.Fatalf("delta was incorrectly rewritten: %s", body.Bytes())
				}
				turn(`{"type":"response.append","input":"unmarked"}`, opts, false)
				body = ojson.NewDoc(<-captured)
				if body.Get("previous_response_id").String() != "r1" || body.Get("input.#").Int() != 1 || body.Get("instructions").String() != "" {
					t.Fatalf("unmarked delta did not inherit managed chain: %s", body.Bytes())
				}
				if mode != "duplex" {
					other := managedInstructionsOptions(t)
					other.Metadata[core.CallerScopeMetadataKey] = "different-caller"
					turn(`{"type":"response.create","previous_response_id":"r2","input":"cross caller"}`, other, true)
					other = managedInstructionsOptions(t)
					other.Headers.Set("Session-Id", "different-logical-session")
					turn(`{"type":"response.create","previous_response_id":"r2","input":"cross session"}`, other, true)
				}
			}
			turn(`{"type":"response.create","previous_response_id":"unknown","input":"next"}`, opts, true)
			if mode == "stream" {
				sess := exec.getOrCreateSession(t.Name())
				sess.connMu.Lock()
				original := sess.conn
				sess.connMu.Unlock()
				exec.invalidateUpstreamConnWithoutDisconnectNotify(sess, original, "test_reconnect", nil)
				turn(`{"type":"response.create","previous_response_id":"r2","input":"next"}`, opts, true)
				turn(`{"type":"response.create","input":"replay","client_metadata":{"cpa_session_instructions":"reuse"}}`, opts, false)
				body := ojson.NewDoc(<-captured)
				if body.Get("input.0.content.0.text").String() != "A" || body.Get("previous_response_id").Exists() {
					t.Fatalf("full replay did not restore the baseline: %s", body.Bytes())
				}
			}
			select {
			case frame := <-captured:
				t.Fatalf("unexpected send: %s", strings.TrimSpace(string(frame)))
			default:
			}
			cancel()
			if duplex != nil {
				for range duplex.Chunks {
				}
			}
		})
	}
}
