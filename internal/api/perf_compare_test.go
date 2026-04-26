package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	perfRunEnv            = "CPA_RUN_PERF"
	perfDurationMsEnv     = "CPA_PERF_DURATION_MS"
	perfConcurrencyEnv    = "CPA_PERF_CONCURRENCY"
	perfWSConcurrencyEnv  = "CPA_PERF_WS_CONCURRENCY"
	perfPayloadUnitsEnv   = "CPA_PERF_PAYLOAD_UNITS"
	perfPromptSizeEnv     = "CPA_PERF_PROMPT_SIZE"
	perfTurnsEnv          = "CPA_PERF_TURNS"
	perfScenariosEnv      = "CPA_PERF_SCENARIOS"
	perfVariantsEnv       = "CPA_PERF_VARIANTS"
	perfProvider          = "codex"
	perfModel             = "gpt-5-codex"
	perfModelSecondary    = "gpt-5-codex-mini"
	defaultPerfPromptSize = "medium"

	defaultPerfDuration      = 2 * time.Second
	defaultPerfConcurrency   = 16
	defaultPerfWSConcurrency = 4
	defaultPerfPayloadUnits  = 24
	defaultPerfTurns         = 1
	defaultPerfVariants      = 64

	perfPayloadUnitsSmall = 8
	perfPayloadUnitsLarge = 96

	perfScenarioChatNonstream   = "chat_nonstream"
	perfScenarioChatStream      = "chat_stream_sse"
	perfScenarioResponses       = "responses_nonstream"
	perfScenarioResponsesStream = "responses_stream_sse"
	perfScenarioCompact         = "responses_compact"
	perfScenarioWSSingleTurn    = "websocket_single_turn"
	perfScenarioWSMultiTurn     = "websocket_multi_turn"

	perfCompletedEventType         = "response.completed"
	perfDoneEventType              = "response.done"
	perfErrorEventType             = "error"
	perfWebsocketResponseCreate    = "response.create"
	perfWebsocketPreviousResponse  = "resp_ws_perf_1"
	perfWebsocketReadTimeout       = 15 * time.Second
	perfAssistantPromptUnitDivisor = 3
)

type perfOptions struct {
	duration      time.Duration
	concurrency   int
	wsConcurrency int
	promptSize    string
	payloadUnits  int
	turns         int
	variants      int
	scenarioSet   map[string]struct{}
}

type perfRequestVariant struct {
	apiHeaders      http.Header
	wsHeaders       http.Header
	chatBody        []byte
	chatBodyStream  []byte
	responsesBody   []byte
	responsesStream []byte
	compactBody     []byte
	wsTurnBodies    [][]byte
	wsClient        perfDownstreamWebsocketClient
}

type perfFixture struct {
	baseURL  string
	wsURL    string
	client   *http.Client
	variants []perfRequestVariant
	cleanup  func()
}

type perfDownstreamWebsocketClient struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

type perfScenario struct {
	id          string
	name        string
	concurrency int
	run         func(*perfFixture, uint64) (int, error)
}

type perfScenarioResult struct {
	Name             string  `json:"name"`
	Concurrency      int     `json:"concurrency"`
	DurationMs       float64 `json:"duration_ms"`
	Requests         int64   `json:"requests"`
	Errors           int64   `json:"errors"`
	ResponseBytes    int64   `json:"response_bytes"`
	RPS              float64 `json:"rps"`
	AvgMs            float64 `json:"avg_ms"`
	P50Ms            float64 `json:"p50_ms"`
	P95Ms            float64 `json:"p95_ms"`
	P99Ms            float64 `json:"p99_ms"`
	MaxMs            float64 `json:"max_ms"`
	AllocBytesPerReq float64 `json:"alloc_bytes_per_req"`
	MallocsPerReq    float64 `json:"mallocs_per_req"`
	FirstError       string  `json:"first_error,omitempty"`
}

type perfSuiteResult struct {
	GoVersion      string               `json:"go_version"`
	Provider       string               `json:"provider"`
	Model          string               `json:"model"`
	DurationMs     float64              `json:"duration_ms"`
	PromptSize     string               `json:"prompt_size"`
	PayloadUnits   int                  `json:"payload_units"`
	Turns          int                  `json:"turns"`
	Variants       int                  `json:"variants"`
	ScenarioFilter []string             `json:"scenario_filter,omitempty"`
	ScenarioCount  int                  `json:"scenario_count"`
	Scenarios      []perfScenarioResult `json:"scenarios"`
	GeneratedAt    time.Time            `json:"generated_at"`
}

func TestSyntheticPerformanceSuite(t *testing.T) {
	if os.Getenv(perfRunEnv) != "1" {
		t.Skip("set CPA_RUN_PERF=1 to run synthetic performance suite")
	}

	opts := loadPerfOptions()

	prevOutput := log.StandardLogger().Out
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prevOutput) })
	gin.SetMode(gin.ReleaseMode)

	fixture := newPerfFixture(t, opts)
	defer fixture.cleanup()

	scenarios := buildPerfScenarios(opts)
	if len(scenarios) == 0 {
		t.Fatalf("no performance scenarios selected for %q", strings.TrimSpace(os.Getenv(perfScenariosEnv)))
	}

	results := make([]perfScenarioResult, 0, len(scenarios))
	for _, scenario := range scenarios {
		results = append(results, runPerfScenario(t, fixture, opts.duration, scenario))
	}

	suite := perfSuiteResult{
		GoVersion:      runtime.Version(),
		Provider:       perfProvider,
		Model:          perfModel,
		DurationMs:     float64(opts.duration.Milliseconds()),
		PromptSize:     opts.promptSize,
		PayloadUnits:   opts.payloadUnits,
		Turns:          opts.turns,
		Variants:       opts.variants,
		ScenarioFilter: perfScenarioFilterList(opts.scenarioSet),
		ScenarioCount:  len(results),
		Scenarios:      results,
		GeneratedAt:    time.Now().UTC(),
	}

	encoded, err := json.Marshal(suite)
	if err != nil {
		t.Fatalf("marshal perf suite result: %v", err)
	}
	fmt.Printf("PERF_RESULT %s\n", encoded)
}

func loadPerfOptions() perfOptions {
	promptSize := envPromptSize(perfPromptSizeEnv, defaultPerfPromptSize)
	payloadUnits := envInt(perfPayloadUnitsEnv, promptSizePayloadUnits(promptSize))
	if strings.TrimSpace(os.Getenv(perfPayloadUnitsEnv)) != "" {
		promptSize = "custom"
	}

	return perfOptions{
		duration:      envDurationMs(perfDurationMsEnv, defaultPerfDuration),
		concurrency:   envInt(perfConcurrencyEnv, defaultPerfConcurrency),
		wsConcurrency: envInt(perfWSConcurrencyEnv, defaultPerfWSConcurrency),
		promptSize:    promptSize,
		payloadUnits:  payloadUnits,
		turns:         envInt(perfTurnsEnv, defaultPerfTurns),
		variants:      envInt(perfVariantsEnv, defaultPerfVariants),
		scenarioSet:   parsePerfScenarioFilter(os.Getenv(perfScenariosEnv)),
	}
}

func promptSizePayloadUnits(size string) int {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "small":
		return perfPayloadUnitsSmall
	case "large":
		return perfPayloadUnitsLarge
	default:
		return defaultPerfPayloadUnits
	}
}

func envPromptSize(key string, fallback string) string {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "small", "medium", "large":
		return raw
	case "":
		return fallback
	default:
		return fallback
	}
}

func parsePerfScenarioFilter(raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	set := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	}) {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" || token == "all" {
			continue
		}
		set[token] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func perfScenarioFilterList(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func buildPerfScenarios(opts perfOptions) []perfScenario {
	scenarios := []perfScenario{
		{
			id:          perfScenarioChatNonstream,
			name:        "codex_chat_completions_nonstream",
			concurrency: maxInt(opts.concurrency, 1),
			run: func(f *perfFixture, seq uint64) (int, error) {
				variant := f.variant(seq)
				return doHTTPRequest(f.client, http.MethodPost, f.baseURL+"/v1/chat/completions", variant.chatBody, variant.apiHeaders)
			},
		},
		{
			id:          perfScenarioChatStream,
			name:        "codex_chat_completions_stream_sse",
			concurrency: maxInt(opts.concurrency/2, 1),
			run: func(f *perfFixture, seq uint64) (int, error) {
				variant := f.variant(seq)
				return doHTTPRequest(f.client, http.MethodPost, f.baseURL+"/v1/chat/completions", variant.chatBodyStream, variant.apiHeaders)
			},
		},
		{
			id:          perfScenarioResponses,
			name:        "codex_responses_nonstream",
			concurrency: maxInt(opts.concurrency, 1),
			run: func(f *perfFixture, seq uint64) (int, error) {
				variant := f.variant(seq)
				return doHTTPRequest(f.client, http.MethodPost, f.baseURL+"/v1/responses", variant.responsesBody, variant.apiHeaders)
			},
		},
		{
			id:          perfScenarioResponsesStream,
			name:        "codex_responses_stream_sse",
			concurrency: maxInt(opts.concurrency/2, 1),
			run: func(f *perfFixture, seq uint64) (int, error) {
				variant := f.variant(seq)
				return doHTTPRequest(f.client, http.MethodPost, f.baseURL+"/v1/responses", variant.responsesStream, variant.apiHeaders)
			},
		},
		{
			id:          perfScenarioCompact,
			name:        "codex_responses_compact",
			concurrency: maxInt(opts.concurrency, 1),
			run: func(f *perfFixture, seq uint64) (int, error) {
				variant := f.variant(seq)
				return doHTTPRequest(f.client, http.MethodPost, f.baseURL+"/v1/responses/compact", variant.compactBody, variant.apiHeaders)
			},
		},
		{
			id:          perfScenarioWSSingleTurn,
			name:        "codex_responses_websocket_single_turn",
			concurrency: maxInt(opts.wsConcurrency, 1),
			run: func(f *perfFixture, seq uint64) (int, error) {
				variant := f.variant(seq)
				return variant.wsClient.do(f.wsURL+"/v1/responses", variant.wsTurnBodies[:1], variant.wsHeaders)
			},
		},
	}

	if opts.turns > 1 {
		scenarios = append(scenarios, perfScenario{
			id:          perfScenarioWSMultiTurn,
			name:        "codex_responses_websocket_multi_turn",
			concurrency: maxInt(opts.wsConcurrency, 1),
			run: func(f *perfFixture, seq uint64) (int, error) {
				variant := f.variant(seq)
				return variant.wsClient.do(f.wsURL+"/v1/responses", variant.wsTurnBodies, variant.wsHeaders)
			},
		})
	}

	if len(opts.scenarioSet) == 0 {
		return scenarios
	}

	filtered := make([]perfScenario, 0, len(scenarios))
	for _, scenario := range scenarios {
		if perfScenarioSelected(scenario, opts.scenarioSet) {
			filtered = append(filtered, scenario)
		}
	}
	return filtered
}

func perfScenarioSelected(scenario perfScenario, set map[string]struct{}) bool {
	if len(set) == 0 {
		return true
	}

	candidates := []string{
		strings.ToLower(strings.TrimSpace(scenario.id)),
		strings.ToLower(strings.TrimSpace(scenario.name)),
	}
	for _, candidate := range candidates {
		if _, ok := set[candidate]; ok {
			return true
		}
	}
	return false
}

func (f *perfFixture) variant(seq uint64) *perfRequestVariant {
	if f == nil || len(f.variants) == 0 {
		return &perfRequestVariant{}
	}
	index := int(seq % uint64(len(f.variants)))
	return &f.variants[index]
}

func newPerfFixture(t *testing.T, opts perfOptions) *perfFixture {
	t.Helper()

	authDir := t.TempDir()
	upstream := newPerfCodexUpstream(t)
	cfg := &config.Config{
		Host:           "127.0.0.1",
		AuthDir:        authDir,
		Debug:          false,
		CommercialMode: true,
	}

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))

	auth := &coreauth.Auth{
		ID:        "perf-codex-auth-001",
		FileName:  "perf-codex-auth-001.json",
		Provider:  perfProvider,
		Label:     "Perf Codex Auth",
		Status:    coreauth.StatusActive,
		CreatedAt: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
		Attributes: map[string]string{
			"api_key":    "sk-perf",
			"base_url":   upstream.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{
			"email": "perf-codex@example.com",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register codex auth: %v", err)
	}

	models := []*registry.ModelInfo{
		{ID: perfModel, Object: "model", OwnedBy: perfProvider},
		{ID: perfModelSecondary, Object: "model", OwnedBy: perfProvider},
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, models)

	server := NewServer(cfg, manager, nil, filepath.Join(authDir, "config.yaml"))
	httpServer := httptest.NewServer(server.engine)

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        512,
			MaxIdleConnsPerHost: 512,
			MaxConnsPerHost:     512,
			DisableCompression:  true,
		},
	}

	variants := make([]perfRequestVariant, 0, maxInt(opts.variants, 1))
	for i := 0; i < maxInt(opts.variants, 1); i++ {
		sessionID := fmt.Sprintf("perf-session-%03d", i)
		variants = append(variants, perfRequestVariant{
			apiHeaders:      buildCodexAPIHeaders(sessionID),
			wsHeaders:       buildCodexWebsocketHeaders(sessionID),
			chatBody:        buildChatRequestBody(opts.payloadUnits, opts.turns, false, i),
			chatBodyStream:  buildChatRequestBody(opts.payloadUnits, opts.turns, true, i),
			responsesBody:   buildResponsesRequestBody(opts.payloadUnits, opts.turns, false, i),
			responsesStream: buildResponsesRequestBody(opts.payloadUnits, opts.turns, true, i),
			compactBody:     buildResponsesCompactBody(opts.payloadUnits, opts.turns, i),
			wsTurnBodies:    buildResponsesWebsocketTurnBodies(opts.payloadUnits, opts.turns, i),
		})
	}

	return &perfFixture{
		baseURL:  httpServer.URL,
		wsURL:    "ws" + strings.TrimPrefix(httpServer.URL, "http"),
		client:   client,
		variants: variants,
		cleanup: func() {
			for i := range variants {
				variants[i].wsClient.close()
			}
			httpServer.Close()
			upstream.Close()
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		},
	}
}

type perfCodexUpstream struct {
	URL    string
	server *httptest.Server
}

func newPerfCodexUpstream(t *testing.T) *perfCodexUpstream {
	t.Helper()

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(_ *http.Request) bool {
			return true
		},
	}

	ssePayload := buildCodexSSEPayload()
	compactPayload := buildCodexCompactPayload()
	websocketPayload := buildCodexWebsocketPayload()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/responses" && websocket.IsWebSocketUpgrade(r):
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()

			for {
				msgType, _, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
					continue
				}
				if err := conn.WriteMessage(websocket.TextMessage, websocketPayload); err != nil {
					return
				}
			}
		case r.URL.Path == "/responses" && r.Method == http.MethodPost:
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(ssePayload)
		case r.URL.Path == "/responses/compact" && r.Method == http.MethodPost:
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(compactPayload)
		default:
			http.NotFound(w, r)
		}
	}))

	return &perfCodexUpstream{
		URL:    server.URL,
		server: server,
	}
}

func (u *perfCodexUpstream) Close() {
	if u != nil && u.server != nil {
		u.server.Close()
	}
}

func buildCodexSSEPayload() []byte {
	text := strings.Repeat("codex-output-", 16)
	itemDone, _ := json.Marshal(map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{{
				"type": "output_text",
				"text": text,
			}},
		},
		"output_index": 0,
	})
	completed, _ := json.Marshal(map[string]any{
		"type": perfCompletedEventType,
		"response": map[string]any{
			"id":         "resp_perf_1",
			"object":     "response",
			"created_at": 1775555723,
			"status":     "completed",
			"model":      perfModel,
			"output":     []any{},
			"usage": map[string]any{
				"input_tokens":  128,
				"output_tokens": 64,
				"total_tokens":  192,
			},
		},
	})
	return []byte("data: " + string(itemDone) + "\n" + "data: " + string(completed) + "\n\n")
}

func buildCodexCompactPayload() []byte {
	payload, _ := json.Marshal(map[string]any{
		"id":     "cmp_perf_1",
		"object": "response.compaction",
		"status": "completed",
		"usage": map[string]any{
			"input_tokens":  128,
			"output_tokens": 32,
			"total_tokens":  160,
		},
	})
	return payload
}

func buildCodexWebsocketPayload() []byte {
	payload, _ := json.Marshal(map[string]any{
		"type": perfCompletedEventType,
		"response": map[string]any{
			"id":     perfWebsocketPreviousResponse,
			"object": "response",
			"status": "completed",
			"output": []map[string]any{{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{{
					"type": "output_text",
					"text": strings.Repeat("codex-ws-", 12),
				}},
			}},
			"usage": map[string]any{
				"input_tokens":  64,
				"output_tokens": 24,
				"total_tokens":  88,
			},
		},
	})
	return payload
}

func buildChatRequestBody(units int, turns int, stream bool, variant int) []byte {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	turns = maxInt(turns, 1)
	messages := make([]message, 0, 1+turns*2)
	messages = append(messages, message{
		Role:    "system",
		Content: "You are Codex. Reply concisely.",
	})
	for turn := 0; turn < turns; turn++ {
		messages = append(messages, message{
			Role:    "user",
			Content: perfPromptText("chat-user", variant, turn, units),
		})
		if turn+1 < turns {
			messages = append(messages, message{
				Role:    "assistant",
				Content: perfPromptText("chat-assistant", variant, turn, assistantPromptUnits(units)),
			})
		}
	}

	payload := map[string]any{
		"model":    perfModel,
		"user":     fmt.Sprintf("perf-user-%03d", variant),
		"messages": messages,
		"stream":   stream,
	}
	body, _ := json.Marshal(payload)
	return body
}

func buildResponsesRequestBody(units int, turns int, stream bool, variant int) []byte {
	payload := map[string]any{
		"model":        perfModel,
		"instructions": "You are Codex. Reply concisely.",
		"input":        buildResponsesConversation(units, turns, variant),
		"stream":       stream,
		"store":        true,
		"metadata": map[string]any{
			"conversation_id": fmt.Sprintf("perf-resp-%03d", variant),
		},
	}
	body, _ := json.Marshal(payload)
	return body
}

func buildResponsesCompactBody(units int, turns int, variant int) []byte {
	payload := map[string]any{
		"model":        perfModel,
		"instructions": "Compact this response for Codex.",
		"input":        buildResponsesConversation(units, turns, variant),
		"stream":       false,
		"store":        true,
		"metadata": map[string]any{
			"conversation_id": fmt.Sprintf("perf-compact-%03d", variant),
		},
	}
	body, _ := json.Marshal(payload)
	return body
}

func buildResponsesConversation(units int, turns int, variant int) []map[string]any {
	turns = maxInt(turns, 1)
	input := make([]map[string]any, 0, turns*2)
	for turn := 0; turn < turns; turn++ {
		input = append(input, responsesInputMessage("user", "input_text", perfPromptText("responses-user", variant, turn, units)))
		if turn+1 < turns {
			input = append(input, responsesInputMessage("assistant", "output_text", perfPromptText("responses-assistant", variant, turn, assistantPromptUnits(units))))
		}
	}
	return input
}

func responsesInputMessage(role string, partType string, text string) map[string]any {
	return map[string]any{
		"type": "message",
		"role": role,
		"content": []map[string]any{{
			"type": partType,
			"text": text,
		}},
	}
}

func buildResponsesWebsocketTurnBodies(units int, turns int, variant int) [][]byte {
	turns = maxInt(turns, 1)
	conversationID := fmt.Sprintf("perf-ws-%03d", variant)
	turnBodies := make([][]byte, 0, turns)

	initial := map[string]any{
		"type":         perfWebsocketResponseCreate,
		"model":        perfModel,
		"instructions": "Use the codex websocket path.",
		"input": []map[string]any{
			responsesInputMessage("user", "input_text", perfPromptText("ws-user", variant, 0, units)),
		},
		"stream": true,
		"store":  true,
		"metadata": map[string]any{
			"conversation_id": conversationID,
		},
	}
	body, _ := json.Marshal(initial)
	turnBodies = append(turnBodies, body)

	for turn := 1; turn < turns; turn++ {
		followUp := map[string]any{
			"type":                 perfWebsocketResponseCreate,
			"previous_response_id": perfWebsocketPreviousResponse,
			"input": []map[string]any{
				responsesInputMessage("user", "input_text", perfPromptText("ws-user", variant, turn, units)),
			},
			"stream": true,
			"store":  true,
			"metadata": map[string]any{
				"conversation_id": conversationID,
			},
		}
		body, _ = json.Marshal(followUp)
		turnBodies = append(turnBodies, body)
	}

	return turnBodies
}

func assistantPromptUnits(units int) int {
	return maxInt(units/perfAssistantPromptUnitDivisor, 1)
}

func perfPromptText(prefix string, variant int, turn int, units int) string {
	units = maxInt(units, 1)
	segment := fmt.Sprintf("%s-%03d-%02d-%s ", prefix, variant, turn, perfModel)
	return strings.Repeat(segment, units)
}

func buildCodexAPIHeaders(sessionID string) http.Header {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", "codex_cli_rs/0.118.0 (Mac OS 14.2.0; x86_64) vscode/1.111.0")
	headers.Set("X-Codex-Installation-Id", "perf-installation-1")
	headers.Set("X-Codex-Window-Id", sessionID+":window")
	headers.Set("X-Codex-Parent-Thread-Id", sessionID+":thread")
	headers.Set("X-OpenAI-Subagent", "perf-subagent")
	headers.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	headers.Set("Tracestate", "perf=1")
	headers.Set("Session_id", sessionID)
	return headers
}

func buildCodexWebsocketHeaders(sessionID string) http.Header {
	headers := cloneHeaders(buildCodexAPIHeaders(sessionID))
	headers.Del("Content-Type")
	headers.Set("X-Codex-Turn-State", "perf-turn-state-"+sessionID)
	return headers
}

func cloneHeaders(headers http.Header) http.Header {
	cloned := http.Header{}
	for key, values := range headers {
		copied := make([]string, len(values))
		copy(copied, values)
		cloned[key] = copied
	}
	return cloned
}

func runPerfScenario(t *testing.T, fixture *perfFixture, duration time.Duration, scenario perfScenario) perfScenarioResult {
	t.Helper()

	result := perfScenarioResult{
		Name:        scenario.name,
		Concurrency: maxInt(scenario.concurrency, 1),
		DurationMs:  float64(duration.Milliseconds()),
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	startWall := time.Now()
	deadline := startWall.Add(duration)
	var requestCount atomic.Int64
	var errorCount atomic.Int64
	var responseBytes atomic.Int64
	var seqCounter atomic.Uint64
	var firstError sync.Once
	var latencyMu sync.Mutex
	latencies := make([]int64, 0, result.Concurrency*128)

	var wg sync.WaitGroup
	wg.Add(result.Concurrency)
	for i := 0; i < result.Concurrency; i++ {
		go func() {
			defer wg.Done()

			localLatencies := make([]int64, 0, 256)
			for {
				if time.Now().After(deadline) {
					break
				}
				seq := seqCounter.Add(1) - 1
				startReq := time.Now()
				n, err := scenario.run(fixture, seq)
				latency := time.Since(startReq).Nanoseconds()
				localLatencies = append(localLatencies, latency)
				requestCount.Add(1)
				if err != nil {
					errorCount.Add(1)
					firstError.Do(func() {
						result.FirstError = err.Error()
					})
					continue
				}
				responseBytes.Add(int64(n))
			}

			latencyMu.Lock()
			latencies = append(latencies, localLatencies...)
			latencyMu.Unlock()
		}()
	}
	wg.Wait()

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	totalDuration := time.Since(startWall)
	result.Requests = requestCount.Load()
	result.Errors = errorCount.Load()
	result.ResponseBytes = responseBytes.Load()
	if totalDuration > 0 {
		result.RPS = float64(result.Requests) / totalDuration.Seconds()
	}
	if len(latencies) > 0 {
		var sum int64
		for _, value := range latencies {
			sum += value
		}
		result.AvgMs = nsToMs(float64(sum) / float64(len(latencies)))
		result.P50Ms = percentileMs(latencies, 0.50)
		result.P95Ms = percentileMs(latencies, 0.95)
		result.P99Ms = percentileMs(latencies, 0.99)
		result.MaxMs = nsToMs(float64(latencies[len(latencies)-1]))
	}
	if result.Requests > 0 {
		result.AllocBytesPerReq = float64(after.TotalAlloc-before.TotalAlloc) / float64(result.Requests)
		result.MallocsPerReq = float64(after.Mallocs-before.Mallocs) / float64(result.Requests)
	}
	return result
}

func doHTTPRequest(client *http.Client, method string, url string, body []byte, headers http.Header) (int, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return 0, err
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return len(payload), fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return len(payload), nil
}

func (c *perfDownstreamWebsocketClient) do(url string, payloads [][]byte, headers http.Header) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, err := c.ensureConn(url, headers)
	if err != nil {
		return 0, err
	}

	total := 0
	for _, payload := range payloads {
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			c.closeConnLocked()
			return total, err
		}
		n, err := readWebsocketResponseUntilDone(conn)
		total += n
		if err != nil {
			c.closeConnLocked()
			return total, err
		}
	}
	return total, nil
}

func (c *perfDownstreamWebsocketClient) ensureConn(url string, headers http.Header) (*websocket.Conn, error) {
	if c == nil {
		return nil, fmt.Errorf("websocket client is nil")
	}
	if c.conn != nil {
		return c.conn, nil
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
	}
	conn, resp, err := dialer.Dial(url, headers)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	c.conn = conn
	return conn, nil
}

func (c *perfDownstreamWebsocketClient) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeConnLocked()
}

func (c *perfDownstreamWebsocketClient) closeConnLocked() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close()
	c.conn = nil
}

func readWebsocketResponseUntilDone(conn *websocket.Conn) (int, error) {
	if conn == nil {
		return 0, fmt.Errorf("websocket conn is nil")
	}

	total := 0
	for {
		_ = conn.SetReadDeadline(time.Now().Add(perfWebsocketReadTimeout))
		msgType, response, err := conn.ReadMessage()
		if err != nil {
			return total, err
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}
		response = bytes.TrimSpace(response)
		if len(response) == 0 {
			continue
		}
		total += len(response)

		eventType := strings.TrimSpace(gjson.GetBytes(response, "type").String())
		switch eventType {
		case perfCompletedEventType, perfDoneEventType:
			return total, nil
		case perfErrorEventType:
			return total, fmt.Errorf("websocket error event: %s", string(response))
		}
	}
}

func percentileMs(values []int64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if fraction <= 0 {
		return nsToMs(float64(values[0]))
	}
	if fraction >= 1 {
		return nsToMs(float64(values[len(values)-1]))
	}
	index := int(float64(len(values)-1) * fraction)
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return nsToMs(float64(values[index]))
}

func nsToMs(value float64) float64 {
	return value / float64(time.Millisecond)
}

func envDurationMs(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return time.Duration(parsed) * time.Millisecond
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
