package cliproxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestDevinPublishedModelRoutes(t *testing.T) {
	const source = "devin/swe-2-lightning"
	const baseSource = "devin/swe-2"
	const alias = "team/chat"
	const prefixedAlias = "team/" + alias
	const excludedSource = "devin/swe-1-6-slow"
	var excluded []string
	for _, model := range registry.GetDevinModels() {
		id := "devin/" + model.ID
		if id != source && id != baseSource {
			excluded = append(excluded, id)
		}
	}

	wireRequests := make(chan ojson.Result, 16)
	var rateLimited atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, wire, errRead := helps.ReadConnectFrame(req.Body)
		if errRead != nil {
			t.Errorf("read Devin upstream request: %v", errRead)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, body, errDecode := helps.FinalizeDevinPayload(wire, func(body []byte) []byte { return body })
		if errDecode != nil {
			t.Errorf("decode Devin upstream request: %v", errDecode)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		wireRequests <- ojson.ParseBytes(body)
		if rateLimited.Load() {
			w.Header().Set("Retry-After", "3600")
			http.Error(w, "model rate limit", http.StatusTooManyRequests)
			return
		}
		textFrame := protowire.AppendString(protowire.AppendTag(nil, 3, protowire.BytesType), "hello from Devin")
		response := append(helps.WrapConnectEnvelope(textFrame), helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...)
		w.Header().Set("Content-Type", "application/connect+proto")
		_, _ = w.Write(response)
	}))
	t.Cleanup(upstream.Close)

	for _, tc := range []struct {
		name     string
		forced   bool
		listed   []string
		rejected []string
	}{
		{
			name:     "qualified alias overlaps optional prefix",
			listed:   []string{source, "team/" + source, alias, prefixedAlias, baseSource, "team/" + baseSource},
			rejected: []string{"chat", "swe-2-lightning", excludedSource, "team/" + excludedSource, "hidden"},
		},
		{
			name: "forced prefix and per-auth alias override", forced: true,
			listed:   []string{prefixedAlias, "team/" + baseSource},
			rejected: []string{alias, source, "team/" + source, baseSource, "team/hidden"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aliasEntry := config.OAuthModelAlias{Name: source, Alias: alias, Fork: !tc.forced, ForceMapping: true}
			cfg := &config.Config{
				OAuthExcludedModels: map[string][]string{"devin": excluded},
				OAuthModelAlias: map[string][]config.OAuthModelAlias{"devin": {
					aliasEntry,
					{Name: excludedSource, Alias: "hidden", Fork: true},
				}},
			}
			cfg.ForceModelPrefix = tc.forced
			cfg.Payload.Override = []config.PayloadRule{
				{Models: []config.PayloadModelRule{{Name: alias}}, Params: map[string]any{"completion_config.temperature": 0.25}},
				{Models: []config.PayloadModelRule{{Name: prefixedAlias}}, Params: map[string]any{"completion_config.temperature": 0.5}},
			}
			auth := &coreauth.Auth{
				ID: t.Name(), Provider: "devin", Prefix: "team", Status: coreauth.StatusActive,
				Attributes: map[string]string{"auth_kind": "oauth", "api_key": "mock-token", "base_url": upstream.URL},
			}
			if tc.forced {
				cfg.OAuthModelAlias["devin"][0] = config.OAuthModelAlias{Name: baseSource, Alias: alias}
				coreauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{aliasEntry})
			}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
			manager.SetRetryConfig(0, 0, 0)
			auth, err := manager.Register(t.Context(), auth)
			if err != nil {
				t.Fatal(err)
			}
			reg := registry.GetGlobalRegistry()
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			service := &Service{cfg: cfg, coreManager: manager}
			if !service.refreshModelRegistrationForAuth(auth) {
				t.Fatal("failed to publish Devin catalog")
			}
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			chat := openai.NewOpenAIAPIHandler(base)
			responses := openai.NewOpenAIResponsesAPIHandler(base)
			router := gin.New()
			router.GET("/v1/models", chat.OpenAIModels)
			router.POST("/v1/chat/completions", chat.ChatCompletions)
			router.GET("/v1/responses/ws", responses.ResponsesWebsocket)

			list := httptest.NewRecorder()
			router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
			var listed []string
			for _, entry := range ojson.GetBytesNoCopy(list.Body.Bytes(), "data").Array() {
				listed = append(listed, entry.Get("id").String())
			}
			if !slices.Equal(slices.Sorted(slices.Values(listed)), slices.Sorted(slices.Values(tc.listed))) {
				t.Fatalf("listed models = %v, want %v", listed, tc.listed)
			}

			assertWire := func(wantUID string, wantTemperature float64) {
				t.Helper()
				select {
				case wire := <-wireRequests:
					if model := wire.Get("model").String(); model != wantUID {
						t.Fatalf("upstream chat_model_uid = %q, want %q", model, wantUID)
					}
					if temp := wire.Get("completion_config.temperature").Float(); temp != wantTemperature {
						t.Fatalf("upstream temperature = %v, want %v", temp, wantTemperature)
					}
				default:
					t.Fatal("no request reached the mock Devin upstream")
				}
				if len(wireRequests) != 0 {
					t.Fatal("unexpected extra Devin upstream request")
				}
			}
			chatRequest := func(model, effort string, stream bool) *httptest.ResponseRecorder {
				payload := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"reasoning_effort":%q,"stream":%t}`, model, effort, stream)
				recorder := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(recorder, req)
				return recorder
			}
			for _, id := range tc.rejected {
				if got := chatRequest(id+"(high)", "", false); got.Code == http.StatusOK || len(wireRequests) != 0 {
					t.Fatalf("unpublished route %q returned %d or reached upstream", id, got.Code)
				}
			}
			for _, id := range tc.listed {
				wantUID, temperature := "swe-2-high-lightning", 1.0
				if strings.HasSuffix(id, baseSource) {
					wantUID = "swe-2-high"
				}
				switch id {
				case alias:
					temperature = 0.25
				case prefixedAlias:
					temperature = 0.5
				}
				got := chatRequest(id+"(high)", "max", false)
				if got.Code != http.StatusOK {
					t.Fatalf("request to %q returned %d: %s", id, got.Code, got.Body)
				}
				assertWire(wantUID, temperature)
				if !strings.HasSuffix(id, alias) {
					continue
				}
				if ojson.GetBytesNoCopy(got.Body.Bytes(), "model").String() != alias {
					t.Fatalf("response to %q did not use alias %q: %s", id, alias, got.Body)
				}
				for _, suffix := range []string{"(low)", "(8192)", "(auto)", "(-1)"} {
					got := chatRequest(id+suffix, "max", false)
					if got.Code != http.StatusOK || ojson.GetBytesNoCopy(got.Body.Bytes(), "model").String() != alias {
						t.Fatalf("request to %s%s did not return the mapped Devin model: %d %s", id, suffix, got.Code, got.Body)
					}
					assertWire("swe-2-medium-lightning", temperature)
				}
			}
			for _, req := range []struct{ suffix, effort, uid string }{
				{"(medium-fast)", "max", "swe-2-medium-fast"},
				{"(8192)", "max", "swe-2-medium"},
				{"", "medium", "swe-2-medium"},
				{"", "", "swe-2-high"},
			} {
				id := baseSource
				if tc.forced {
					id = "team/" + id
				}
				if got := chatRequest(id+req.suffix, req.effort, false); got.Code != http.StatusOK {
					t.Fatalf("request to %q returned %d: %s", id+req.suffix, got.Code, got.Body)
				}
				assertWire(req.uid, 1)
			}
			got := chatRequest(prefixedAlias+"(high)", "", true)
			if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"model":"team/chat"`) || !strings.Contains(got.Body.String(), "hello from Devin") {
				t.Fatalf("stream did not return the mapped Devin output: %d %s", got.Code, got.Body)
			}
			assertWire("swe-2-high-lightning", 0.5)
			countPayload := []byte(`{"messages":[{"role":"user","content":"Count these Devin input tokens"}]}`)
			count, _, countErr := base.ExecuteCountWithAuthManager(t.Context(), "openai", prefixedAlias+"(high)", countPayload, "")
			if countErr != nil || ojson.GetBytesNoCopy(count, "input_tokens").Int() <= 0 || len(wireRequests) != 0 {
				t.Fatalf("count did not use the published Devin route: %s, error=%v", count, countErr)
			}

			if tc.forced {
				return
			}
			t.Run("downstream websocket", func(t *testing.T) {
				server := httptest.NewServer(router)
				t.Cleanup(server.Close)
				wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
				conn, _, errDial := websocket.DefaultDialer.DialContext(t.Context(), wsURL, nil)
				if errDial != nil {
					t.Fatal(errDial)
				}
				defer func() { _ = conn.Close() }()
				if errDeadline := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
					t.Fatal(errDeadline)
				}
				for _, req := range []struct {
					id          string
					temperature float64
				}{{alias, 0.25}, {prefixedAlias, 0.5}} {
					payload := fmt.Appendf(nil, `{"type":"response.create","model":%q,"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`, req.id+"(high)")
					if errWrite := conn.WriteMessage(websocket.TextMessage, payload); errWrite != nil {
						t.Fatal(errWrite)
					}
					for {
						_, payload, errRead := conn.ReadMessage()
						if errRead != nil {
							t.Fatal(errRead)
						}
						event := ojson.ParseBytes(payload)
						switch event.Get("type").String() {
						case "error", "response.failed":
							t.Fatalf("websocket request for %q failed: %s", req.id, payload)
						case "response.completed":
							if model := event.Get("response.model").String(); model != alias {
								t.Fatalf("websocket response model = %q, want %q", model, alias)
							}
						default:
							continue
						}
						break
					}
					assertWire("swe-2-high-lightning", req.temperature)
				}
			})

			rateLimited.Store(true)
			limited := chatRequest(prefixedAlias+"(low)", "", false)
			rateLimited.Store(false)
			if limited.Code != http.StatusTooManyRequests {
				t.Fatalf("expected a model-scoped 429 from Devin, got %d: %s", limited.Code, limited.Body)
			}
			assertWire("swe-2-medium-lightning", 0.5)
			for _, id := range []string{source, alias, prefixedAlias} {
				if !reg.IsModelSuspendedForClient(auth.ID, id) {
					t.Fatalf("cooldown was not projected onto %q", id)
				}
				if got := chatRequest(id+"(high)", "", false); got.Code == http.StatusOK || len(wireRequests) != 0 {
					t.Fatalf("cooled route %q accepted a different effort: %d", id, got.Code)
				}
			}
			cfg.OAuthModelAlias["devin"][0].Name = baseSource
			cfg.OAuthExcludedModels["devin"] = append(slices.Clone(excluded), source)
			manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
			if !service.refreshModelRegistrationForAuth(auth) {
				t.Fatal("failed to republish Devin alias")
			}
			if got := chatRequest(alias+"(high)", "", false); got.Code != http.StatusOK {
				t.Fatalf("rebound alias returned %d: %s", got.Code, got.Body)
			}
			assertWire("swe-2-high", 0.25)
		})
	}
}
