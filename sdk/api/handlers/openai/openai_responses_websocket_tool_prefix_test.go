package openai

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketPrewarmToolPrefixBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// The diagnostic emits only counts, synthetic IDs, hashes and booleans.
	// Existing handler log statements can include payloads; silence them only
	// within this nonparallel test and restore the test process logger afterward.
	previousOutput := log.StandardLogger().Out
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(previousOutput) })

	for _, tc := range []struct {
		name  string
		input string
	}{
		{
			name:  "ordinary_delta",
			input: `[{"type":"message","id":"probe-user","role":"user","content":"diagnostic only"}]`,
		},
		{
			name:  "compacted_delta",
			input: `[{"type":"compaction","encrypted_content":"synthetic-checkpoint-not-for-provider"},{"type":"message","id":"probe-user","role":"user","content":"diagnostic only"}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &websocketProviderCaptureExecutor{provider: "codex"}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			auth := &coreauth.Auth{
				ID:         "tool-prefix-fixture-" + tc.name,
				Provider:   "codex",
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"websockets": "false"},
			}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal("fixture auth registration failed")
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "fixture-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router := gin.New()
			router.GET("/v1/responses/ws", h.ResponsesWebsocket)
			server := httptest.NewServer(router)
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
			if err != nil {
				t.Fatal("fixture websocket dial failed")
			}
			defer func() {
				if err := conn.Close(); err != nil {
					t.Error("fixture websocket close failed")
				}
			}()
			send := func(payload string) {
				t.Helper()
				if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
					t.Fatal("fixture websocket write failed")
				}
			}
			read := func() []byte {
				t.Helper()
				_, payload, err := conn.ReadMessage()
				if err != nil {
					t.Fatal("fixture websocket read failed")
				}
				return payload
			}

			warmup := `{"type":"response.create","model":"fixture-model","generate":false,"input":[{"type":"additional_tools","id":"warm-tools","role":"developer","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"probe","description":"synthetic","parameters":{"type":"object","properties":{}}}]}]},{"type":"message","id":"warm-base","role":"developer","content":"synthetic base instructions"}]}`
			send(warmup)
			created := read()
			parent := gjson.GetBytes(created, "response.id").String()
			if gjson.GetBytes(created, "type").String() != "response.created" || !strings.HasPrefix(parent, "resp_prewarm_") {
				t.Fatal("fixture did not receive a synthetic prewarm parent")
			}
			completed := read()
			if gjson.GetBytes(completed, "type").String() != wsEventTypeCompleted || gjson.GetBytes(completed, "response.id").String() != parent {
				t.Fatal("fixture prewarm completion did not match its parent")
			}
			if executor.streamCalls != 0 || len(executor.payloads) != 0 {
				t.Fatal("synthetic prewarm unexpectedly reached the fake executor")
			}
			t.Logf("boundary=prewarm case=%s parent=%s executor_calls=0", tc.name, parent)

			send(fmt.Sprintf(`{"type":"response.create","model":"fixture-model","previous_response_id":%q,"input":%s}`, parent, tc.input))
			if gjson.GetBytes(read(), "type").String() != wsEventTypeCompleted {
				t.Fatal("fixture generating response did not complete")
			}
			if executor.streamCalls != 1 || len(executor.payloads) != 1 {
				t.Fatalf("fake executor counts: calls=%d payloads=%d, want one each", executor.streamCalls, len(executor.payloads))
			}
			forwarded := executor.payloads[0]
			input := gjson.GetBytes(forwarded, "input").Array()
			prefix := gjson.Get(warmup, "input").Array()
			wantSchema := sha256.Sum256([]byte(prefix[0].Get("tools").Raw))
			var gotSchema [sha256.Size]byte
			toolPrefixes, basePrefixes, compactions := 0, 0, 0
			baseMatches := false
			var suffix []gjson.Result
			for _, item := range input {
				if item.Get("type").String() == "compaction" {
					compactions++
				}
				switch {
				case item.Get("type").String() == "additional_tools":
					toolPrefixes++
					gotSchema = sha256.Sum256([]byte(item.Get("tools").Raw))
				case item.Get("id").String() == "warm-base":
					basePrefixes++
					baseMatches = item.Raw == prefix[1].Raw
				default:
					suffix = append(suffix, item)
				}
			}
			wantSuffix := gjson.Parse(tc.input).Array()
			suffixMatches := len(suffix) == len(wantSuffix)
			if suffixMatches {
				for i := range suffix {
					if suffix[i].Raw != wantSuffix[i].Raw {
						suffixMatches = false
					}
				}
			}
			prefixFirst := len(input) >= 2 && input[0].Raw == prefix[0].Raw && input[1].Raw == prefix[1].Raw
			t.Logf("boundary=fake_executor case=%s parent=%s tool_prefixes=%d base_prefixes=%d compaction_items=%d schema_sha256=%x schema_equal=%t base_equal=%t prefix_first=%t suffix_equal=%t", tc.name, parent, toolPrefixes, basePrefixes, compactions, gotSchema, gotSchema == wantSchema, baseMatches, prefixFirst, suffixMatches)
			if toolPrefixes != 1 || basePrefixes != 1 || gotSchema != wantSchema || !baseMatches || !prefixFirst {
				t.Error("acknowledged tool/base prefix lost, changed, duplicated or reordered at fake executor ingress")
			}
			if !suffixMatches {
				t.Error("generating input suffix changed at fake executor ingress")
			}
			if gjson.GetBytes(forwarded, "previous_response_id").Exists() || gjson.GetBytes(forwarded, "generate").Exists() {
				t.Error("synthetic continuation state leaked into fake upstream request")
			}
		})
	}
}
