package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// runCodexDuplexPayloadTurns captures final WS and local-summary HTTP bodies in
// turn order, resolves synthetic parents, and verifies one rule evaluation per send.
func runCodexDuplexPayloadTurns(t *testing.T, rules config.PayloadConfig, turns []string, compat, useSIWC bool, clientHeaders ...http.Header) [][]byte {
	t.Helper()
	captured := make(chan []byte, len(turns))
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if useSIWC && r.Header.Get("Authorization") != "Bearer siwc-test" {
			t.Errorf("SIWC request used unexpected authorization")
		}
		if !websocket.IsWebSocketUpgrade(r) {
			body, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				t.Error(errRead)
				return
			}
			captured <- body
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"summary\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"COMPACT_CONTEXT\"}]}]}}\n\n")
			return
		}
		defer close(serverDone)
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		for turn := 0; ; turn++ {
			_, body, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			captured <- bytes.Clone(body)
			if ojson.GetBytes(body, "event_id").String() == "reject" {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","error":{"code":"invalid_request_error","message":"rejected"}}`)); errWrite != nil {
					t.Error(errWrite)
				}
				continue
			}
			if ojson.GetBytes(body, "type").String() == "response.steer" {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"r0"}}`)); errWrite != nil {
					t.Error(errWrite)
					return
				}
			}
			for _, kind := range []string{"response.created", "response.completed"} {
				response := fmt.Sprintf(`{"type":%q,"response":{"id":"r%d","status":"completed","output":[]}}`, kind, turn)
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(response)); errWrite != nil {
					t.Error(errWrite)
					return
				}
			}
		}
	}))
	defer server.Close()
	cfg := &config.Config{Codex: config.CodexConfig{ResponseSteering: true}, SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}, Payload: rules}
	model := "gpt-6-astra"
	if compat {
		model = "deepseek-v4.1-flash"
		cfg.CodexKey = []config.CodexKey{{APIKey: "test", BaseURL: server.URL, Models: []config.CodexModel{{Name: model, IsCompat: true}}}}
	}
	exec := NewCodexWebsocketsExecutor(cfg)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	defer exec.CloseExecutionSession(t.Name())
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": server.URL, "websockets": "true"}}
	if useSIWC {
		auth.Metadata = map[string]any{"use_siwc": true, "siwc": map[string]any{"access_token": "siwc-test"}}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input := make(chan core.WebsocketInput, 1)
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
	var finalized atomic.Int32
	opts := core.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{core.ExecutionSessionMetadataKey: t.Name(), core.FinalizedResponseFormatCallbackMetadataKey: func(string) { finalized.Add(1) }}}
	if len(clientHeaders) > 0 {
		opts.Headers = clientHeaders[0].Clone()
	}
	before := helps.PayloadConfigEvaluationCount()
	result, errExecute := exec.ExecuteStream(ctx, auth, core.Request{Model: model, Payload: []byte(turns[0])}, opts)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	var bodies [][]byte
	previousID := ""
	for i, turn := range turns {
		if i > 0 {
			doc := ojson.NewDocString(turn)
			if doc.Get("previous_response_id").String() == "$previous" {
				_ = doc.Set("previous_response_id", previousID)
			}
			input <- core.WebsocketInput{Payload: doc.Bytes()}
		}
		terminal := false
		for !terminal {
			chunk, ok := <-result.Chunks
			if !ok {
				t.Fatalf("stream closed at turn %d", i)
			}
			if chunk.Err != nil {
				t.Fatalf("turn %d: %v", i, chunk.Err)
			}
			for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
				payload := bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(line), []byte("data:")))
				switch ojson.GetBytes(payload, "type").String() {
				case "response.completed":
					previousID = ojson.GetBytes(payload, "response.id").String()
					terminal = true
				case "error":
					if ojson.Get(turn, "event_id").String() != "reject" {
						t.Fatalf("turn %d: unexpected error: %s", i, payload)
					}
					terminal = true
				}
			}
		}
		bodies = append(bodies, <-captured)
		if finalized.Load() != int32(i+1) || helps.PayloadConfigEvaluationCount()-before != int64(i+1) {
			t.Fatalf("turn %d: payload evaluated more than once: callbacks=%d evaluations=%d", i, finalized.Load(), helps.PayloadConfigEvaluationCount()-before)
		}
	}
	cancel()
	for range result.Chunks {
	}
	<-serverDone
	return bodies
}

func TestCodexDuplexPayloadInstructionsDoNotAccumulate(t *testing.T) {
	rules := config.PayloadConfig{Override: []config.PayloadRule{
		{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"instructions": "hero!"}}}}, Params: map[string]any{"instructions": "hero!!"}},
		{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"instructions": "hero"}}}}, Params: map[string]any{"instructions": "hero!"}},
	}}
	bodies := runCodexDuplexPayloadTurns(t, rules, []string{
		`{"instructions":"hero","input":[]}`,
		`{"type":"response.append","input":[]}`,
		`{"type":"response.append","input":[]}`,
		`{"type":"response.create","instructions":"new","input":[]}`,
		`{"type":"response.append","input":[]}`,
		`{"type":"response.create","instructions":"failed","event_id":"reject","input":[]}`,
		`{"type":"response.append","input":[]}`,
		`{"type":"response.append","instructions":"","input":[]}`,
		`{"type":"response.append","input":[]}`,
		`{"type":"response.append","instructions":null,"input":[]}`,
		`{"type":"response.append","input":[]}`,
	}, false, false)
	for i, want := range []string{`"hero!"`, `"hero!"`, `"hero!"`, `"new"`, `"new"`, `"failed"`, `"new"`, `""`, `""`, `""`, `""`} {
		if got := ojson.GetBytes(bodies[i], "instructions").Raw; got != want {
			t.Errorf("turn %d instructions=%s, want %s; body=%s", i, got, want, bodies[i])
		}
	}
}

func TestCodexDuplexPayloadAutomaticSuccessorRetainsUnconfiguredInstructions(t *testing.T) {
	rules := config.PayloadConfig{Override: []config.PayloadRule{
		{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"instructions": "hero!"}}}}, Params: map[string]any{"instructions": "hero!!"}},
		{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"instructions": "hero"}}}}, Params: map[string]any{"instructions": "hero!", "reasoning.effort": "high"}},
	}}
	bodies := runCodexDuplexPayloadTurns(t, rules, []string{
		`{"instructions":"hero","input":[]}`,
		`{"type":"response.steer","previous_response_id":"r0","input":"continue"}`,
		`{"type":"response.append","previous_response_id":"$previous","input":[]}`,
	}, false, false)
	for _, i := range []int{0, 2} {
		if ojson.GetBytes(bodies[i], "instructions").String() != "hero!" || ojson.GetBytes(bodies[i], "reasoning.effort").String() != "high" {
			t.Fatalf("turn %d: automatic successor lost unconfigured settings: %s", i, bodies[i])
		}
	}
	if ojson.GetBytes(bodies[2], "previous_response_id").String() != "r1" {
		t.Fatalf("append did not continue the automatic successor: %s", bodies[2])
	}
}

func TestCodexDuplexPayloadReplacementAndArrayFilterRunOncePerTurn(t *testing.T) {
	rules := config.PayloadConfig{
		Replace: []config.PayloadReplaceRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Role: "user", Positions: []int{1}}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}}}},
		Filter:  []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"input.0", "instructions"}}},
	}
	bodies := runCodexDuplexPayloadTurns(t, rules, []string{
		`{"instructions":"hero","input":[{"role":"assistant","content":"drop"},{"role":"user","content":"hero"},{"role":"user","content":"hero"}]}`,
		`{"type":"response.append","input":[{"role":"assistant","content":"drop"},{"role":"user","content":"hero"},{"role":"user","content":"hero"}]}`,
		`{"type":"response.append","input":[{"role":"assistant","content":"drop"},{"role":"user","content":"hero"},{"role":"user","content":"hero"}]}`,
	}, false, false)
	for i, body := range bodies {
		if ojson.GetBytes(body, "instructions").Exists() || ojson.GetBytes(body, "input.#").Int() != 2 || ojson.GetBytes(body, "input.0.content").String() != "hero!" || ojson.GetBytes(body, "input.1.content").String() != "hero" {
			t.Fatalf("turn %d: rules repeated or filtered fields restored: %s", i, body)
		}
	}
}

func TestCodexDuplexPayloadInjectedAndFilteredInstructionsStayTurnScoped(t *testing.T) {
	for _, mode := range []string{"inject", "filter"} {
		t.Run(mode, func(t *testing.T) {
			rules := config.PayloadConfig{}
			first := `{"input":[],"event_id":"first"}`
			want := []string{`"injected"`, `""`, `""`}
			if mode == "inject" {
				rules.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"event_id": "first"}}}}, Params: map[string]any{"instructions": "injected"}}}
			} else {
				first = `{"instructions":"hero","input":[],"event_id":"first"}`
				rules.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"event_id": "first"}}}}, Params: []string{"instructions"}}}
				want = []string{"", `"hero"`, `"hero"`}
			}
			bodies := runCodexDuplexPayloadTurns(t, rules, []string{first, `{"type":"response.append","input":[]}`, `{"type":"response.append","input":[]}`}, false, false)
			for i, expected := range want {
				if got := ojson.GetBytes(bodies[i], "instructions").Raw; got != expected {
					t.Errorf("turn %d: instructions=%s, want %s", i, got, expected)
				}
			}
		})
	}
}

func TestCodexDuplexPayloadCompactionInheritsUnconfiguredInstructions(t *testing.T) {
	for _, parentType := range []string{"response.append", "response.create"} {
		for _, compactInstructions := range []string{"", `,"instructions":"new"`} {
			t.Run(parentType+"/"+compactInstructions, func(t *testing.T) {
				rules := config.PayloadConfig{Override: []config.PayloadRule{
					{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"instructions": "hero!"}, {"instructions": "new!"}}}}, Params: map[string]any{"instructions": "accumulated"}},
					{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"instructions": "hero"}}}}, Params: map[string]any{"instructions": "hero!"}},
					{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"instructions": "new"}}}}, Params: map[string]any{"instructions": "new!"}},
				}}
				bodies := runCodexDuplexPayloadTurns(t, rules, []string{
					`{"instructions":"hero","input":[{"role":"user","content":"start"}]}`,
					`{"type":"response.create"` + compactInstructions + `,"input":[{"role":"user","content":"history"},{"type":"compaction_trigger"}]}`,
					`{"type":"` + parentType + `","previous_response_id":"$previous","input":"continue"}`,
					`{"type":"response.append","input":[]}`,
				}, true, false)
				want := "hero!"
				if compactInstructions != "" {
					want = "new!"
				}
				for _, i := range []int{2, 3} {
					if got := ojson.GetBytes(bodies[i], "instructions").String(); got != want {
						t.Errorf("turn %d: instructions=%q want=%q body=%s", i, got, want, bodies[i])
					}
				}
				if ojson.GetBytes(bodies[2], "previous_response_id").Exists() || !bytes.Contains(bodies[2], []byte("COMPACT_CONTEXT")) || bytes.Contains(bodies[2], []byte("cpa-ag-compact-v1:")) {
					t.Fatalf("invalid synthetic-parent reconstruction: %s", bodies[2])
				}
			})
		}
	}
}

func TestCodexDuplexPayloadDefaultInstructionsAcrossTurns(t *testing.T) {
	for _, mode := range []string{"default", "default_raw"} {
		for _, tc := range []struct {
			name   string
			turns  []string
			compat bool
			want   map[int]string
		}{
			{
				name:  "append",
				turns: []string{`{"input":[]}`, `{"type":"response.append","input":[]}`, `{"type":"response.append","input":[]}`},
				want:  map[int]string{0: "default instructions", 1: "default instructions", 2: "default instructions"},
			},
			{
				name:  "explicit_clear",
				turns: []string{`{"input":[]}`, `{"type":"response.append","instructions":"","input":[]}`, `{"type":"response.append","input":[]}`, `{"type":"response.append","instructions":null,"input":[]}`, `{"type":"response.append","input":[]}`},
				want:  map[int]string{0: "default instructions", 1: "", 2: "", 3: "", 4: ""},
			},
			{
				name:  "fresh_create_keeps_payload_default",
				turns: []string{`{"instructions":"initial instructions","input":[]}`, `{"type":"response.create","input":[]}`, `{"type":"response.append","input":[]}`},
				want:  map[int]string{0: "initial instructions", 1: "default instructions", 2: "default instructions"},
			},
			{
				name:  "automatic_successor",
				turns: []string{`{"input":[]}`, `{"type":"response.steer","previous_response_id":"r0","input":"continue"}`, `{"type":"response.append","previous_response_id":"$previous","input":[]}`},
				want:  map[int]string{0: "default instructions", 2: "default instructions"},
			},
			{
				name:   "compaction",
				turns:  []string{`{"input":[{"role":"user","content":"start"}]}`, `{"type":"response.create","input":[{"role":"user","content":"history"},{"type":"compaction_trigger"}]}`, `{"type":"response.append","previous_response_id":"$previous","input":"continue"}`, `{"type":"response.append","input":[]}`},
				compat: true,
				want:   map[int]string{0: "default instructions", 2: "default instructions", 3: "default instructions"},
			},
			{
				name:   "compaction_explicit_clear",
				turns:  []string{`{"instructions":"initial instructions","input":[{"role":"user","content":"start"}]}`, `{"type":"response.create","instructions":"","input":[{"role":"user","content":"history"},{"type":"compaction_trigger"}]}`, `{"type":"response.create","previous_response_id":"$previous","input":"continue"}`, `{"type":"response.append","input":[]}`},
				compat: true,
				want:   map[int]string{0: "initial instructions", 2: "", 3: ""},
			},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				rules := config.PayloadConfig{}
				if mode == "default" {
					rules.Default = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"instructions": "default instructions"}}}
				} else {
					rules.DefaultRaw = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"instructions": `"default instructions"`}}}
				}
				bodies := runCodexDuplexPayloadTurns(t, rules, tc.turns, tc.compat, false)
				for turn, want := range tc.want {
					if got := ojson.GetBytes(bodies[turn], "instructions").String(); got != want {
						t.Errorf("turn %d: instructions=%q, want %q; body=%s", turn, got, want, bodies[turn])
					}
				}
			})
		}
	}
}

func TestCodexDuplexPayloadInstructionFallback(t *testing.T) {
	for _, route := range []string{"standard", "lite", "siwc"} {
		for _, scenario := range []string{"missing", "explicit_empty", "explicit_null", "explicit_whitespace", "clear_with_default", "default", "default_raw", "override_once"} {
			t.Run(route+"/"+scenario, func(t *testing.T) {
				headers := make(http.Header)
				if route == "lite" {
					headers.Set(codexResponsesLiteHeader, "true")
				}
				rules := config.PayloadConfig{}
				middle := ojson.NewDocString(`{"type":"response.create","previous_response_id":"$previous","input":[]}`)
				wantFirst := `"hero"`
				wantMiddle := ""
				wantAppend := wantFirst
				if route == "standard" {
					// The built-in empty instruction is not caller input, but still
					// prevents the legacy fallback to the initial response.
					wantMiddle, wantAppend = `""`, `""`
				}
				switch scenario {
				case "explicit_empty", "explicit_null", "explicit_whitespace", "clear_with_default":
					raw := `""`
					if scenario == "explicit_null" {
						raw = `null`
					} else if scenario == "explicit_whitespace" {
						raw = `"  "`
					}
					_ = middle.SetRaw("instructions", raw)
					wantMiddle = raw
					if route == "siwc" {
						wantMiddle = ""
					} else if route == "standard" && scenario == "explicit_null" {
						wantMiddle = `""`
					}
					wantAppend = wantMiddle
					if scenario == "clear_with_default" {
						rules.Default = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"instructions": "default instructions"}}}
					}
				case "default", "default_raw":
					if scenario == "default" {
						rules.Default = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"instructions": "default instructions"}}}
					} else {
						rules.DefaultRaw = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"instructions": `"default instructions"`}}}
					}
					wantMiddle, wantAppend = `"default instructions"`, `"default instructions"`
				case "override_once":
					rules.Override = []config.PayloadRule{
						{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"instructions": "hero!"}}}}, Params: map[string]any{"instructions": "hero!!"}},
						{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"instructions": "hero"}}}}, Params: map[string]any{"instructions": "hero!"}},
					}
					wantFirst = `"hero!"`
					if route != "standard" {
						wantAppend = wantFirst
					}
				}
				bodies := runCodexDuplexPayloadTurns(t, rules, []string{
					`{"instructions":"hero","input":[]}`,
					middle.String(),
					`{"type":"response.append","input":[]}`,
					`{"type":"response.append","input":[]}`,
				}, false, route == "siwc", headers)
				for turn, want := range []string{wantFirst, wantMiddle, wantAppend, wantAppend} {
					if got := ojson.GetBytes(bodies[turn], "instructions").Raw; got != want {
						t.Errorf("turn %d: instructions=%s, want %s; body=%s", turn, got, want, bodies[turn])
					}
				}
			})
		}
	}
}

func TestCodexDuplexPayloadSIWCInstructionsAcrossTurns(t *testing.T) {
	for _, mode := range []string{"default", "default_raw"} {
		for _, tc := range []struct {
			name  string
			turns []string
			want  map[int]string
		}{
			{
				name:  "initial_empty",
				turns: []string{`{"instructions":"","input":[]}`, `{"type":"response.append","input":[]}`, `{"type":"response.append","input":[]}`},
				want:  map[int]string{0: "", 1: "", 2: ""},
			},
			{
				name:  "initial_null",
				turns: []string{`{"instructions":null,"input":[]}`, `{"type":"response.append","input":[]}`},
				want:  map[int]string{0: "", 1: ""},
			},
			{
				name:  "initial_whitespace",
				turns: []string{`{"instructions":"  ","input":[]}`, `{"type":"response.append","input":[]}`},
				want:  map[int]string{0: "", 1: ""},
			},
			{
				name:  "absent_uses_default",
				turns: []string{`{"input":[]}`, `{"type":"response.append","input":[]}`, `{"type":"response.append","input":[]}`},
				want:  map[int]string{0: `"default instructions"`, 1: `"default instructions"`, 2: `"default instructions"`},
			},
			{
				name:  "append_clears_default",
				turns: []string{`{"input":[]}`, `{"type":"response.append","instructions":"","input":[]}`, `{"type":"response.append","input":[]}`, `{"type":"response.append","instructions":"updated","input":[]}`, `{"type":"response.append","input":[]}`},
				want:  map[int]string{0: `"default instructions"`, 1: "", 2: "", 3: `"updated"`, 4: `"updated"`},
			},
			{
				name:  "automatic_successor_keeps_clear",
				turns: []string{`{"instructions":"","input":[]}`, `{"type":"response.steer","previous_response_id":"r0","input":"continue"}`, `{"type":"response.append","previous_response_id":"$previous","input":[]}`},
				want:  map[int]string{0: "", 2: ""},
			},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				rules := config.PayloadConfig{}
				if mode == "default" {
					rules.Default = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"instructions": "default instructions"}}}
				} else {
					rules.DefaultRaw = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"instructions": `"default instructions"`}}}
				}
				bodies := runCodexDuplexPayloadTurns(t, rules, tc.turns, false, true)
				for turn, want := range tc.want {
					if got := ojson.GetBytes(bodies[turn], "instructions").Raw; got != want {
						t.Errorf("turn %d: instructions=%s, want %s; body=%s", turn, got, want, bodies[turn])
					}
				}
			})
		}
	}
}

func TestCodexWebsocketUnconfiguredInstructionsSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		from       sdktranslator.Format
		body, want string
	}{
		{"native", sdktranslator.FormatCodex, `{"instructions":"hero","input":[]}`, `"hero"`},
		{"native_absent", sdktranslator.FormatCodex, `{"input":[]}`, ""},
		{"native_null", sdktranslator.FormatCodex, `{"instructions":null,"input":[]}`, `null`},
		{"responses_absent", sdktranslator.FormatOpenAIResponse, `{"input":[]}`, ""},
		{"responses_empty", sdktranslator.FormatOpenAIResponse, `{"instructions":"","input":[]}`, `""`},
		{"responses_null", sdktranslator.FormatOpenAIResponse, `{"instructions":null,"input":[]}`, `""`},
		{"translated", sdktranslator.FormatInteractions, `{"system_instruction":"hero","input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]}]}`, `"hero"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := NewCodexWebsocketsExecutor(&config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"instructions": "configured"}}}}})
			req := core.Request{Model: "gpt-6-astra", Payload: []byte(tc.body)}
			headers := make(http.Header)
			if tc.from == sdktranslator.FormatCodex {
				headers.Set(codexResponsesLiteHeader, "true")
			}
			prepared, errPrepare := exec.prepareCodexWebsocketStream(t.Context(), &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": "https://example.invalid"}}, req, core.Options{SourceFormat: tc.from, Headers: headers})
			if errPrepare != nil {
				t.Fatal(errPrepare)
			}
			if ojson.GetBytes(prepared.clientBody, "instructions").String() != "configured" {
				t.Fatalf("payload not applied: %s", prepared.clientBody)
			}
			clear(req.Payload)
			clear(prepared.clientBody)
			if prepared.unconfiguredInstructions != tc.want {
				t.Fatalf("snapshot=%s want=%s", prepared.unconfiguredInstructions, tc.want)
			}
		})
	}
}
