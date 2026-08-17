package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// midSystemLegacyPayload is a caller body pairing a legacy model with a
// mid-conversation role=system turn. The turn ends the array, so the shape is
// rejected by the model rather than by Anthropic's ordering rule.
func midSystemLegacyPayload(model string) []byte {
	return []byte(`{"model":"` + model + `","max_tokens":32,` +
		`"system":[{"type":"text","text":"Top rule"}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"system","content":[{"type":"text","text":"Mid rule"}]}],` +
		`"metadata":{"user_id":"{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"account_uuid\":\"\",\"session_id\":\"11111111-2222-4333-8444-555555555555\"}"}}`)
}

// midSystemUpstream intercepts the transport instead of standing up a test
// server, so the executor keeps the default https://api.anthropic.com base URL.
// The guard only fires on Anthropic's first-party origin, which a httptest
// server address would not satisfy.
type midSystemUpstream struct {
	body    []byte
	called  bool
	headers http.Header
}

func (u *midSystemUpstream) context(t *testing.T, headers http.Header) context.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(nil)
	ginCtx.Request = httptest_NewRequest()
	ginCtx.Request.Header = headers.Clone()
	if ginCtx.Request.Header == nil {
		ginCtx.Request.Header = make(http.Header)
	}
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		payload, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		u.body = payload
		u.called = true
		u.headers = req.Header.Clone()
		contentType := "application/json"
		responseBody := `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		if strings.Contains(req.URL.Path, "count_tokens") {
			responseBody = `{"input_tokens":18}`
		} else if gjson.GetBytes(payload, "stream").Bool() {
			contentType = "text/event-stream"
			// A translated caller aggregates the stream back into one message, so
			// the stub has to complete the block and report a stop reason.
			responseBody = strings.Join([]string{
				`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
				`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
				`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
			}, "\n\n") + "\n\n"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{contentType}},
			Body:       io.NopCloser(strings.NewReader(responseBody)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	return context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(transport))
}

func httptest_NewRequest() *http.Request {
	req, _ := http.NewRequest(http.MethodPost, "http://example.invalid/", nil)
	return req
}

func midSystemAuth() *cliproxyauth.Auth {
	// No base_url, so the executor keeps Anthropic's first-party origin.
	return &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123"}}
}

func midSystemConfig() *config.Config {
	return &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "key-123"}}}
}

func assertMidSystemRejected(t *testing.T, err error, upstream *midSystemUpstream) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want the legacy pairing rejected")
	}
	if upstream.called {
		t.Fatalf("upstream must not be called for a guaranteed rejection; got %s", upstream.body)
	}
	var statusCoder interface{ StatusCode() int }
	if !errors.As(err, &statusCoder) || statusCoder.StatusCode() != http.StatusBadRequest {
		t.Fatalf("error = %v, want a 400 status error", err)
	}
	var scoped interface{ IsRequestScoped() bool }
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
		t.Fatalf("error = %v, want a request-scoped error so no credential is retried", err)
	}
	if !strings.Contains(err.Error(), "role 'system' is not supported on this model") {
		t.Fatalf("error = %v, want Anthropic's wording preserved", err)
	}
}

// Every executor path that can send the pairing to Anthropic must answer it
// locally instead of spending an upstream call on a guaranteed 400.
func TestClaudeExecutor_LegacyMidSystemMessageRejectedOnEveryUpstreamPath(t *testing.T) {
	for _, test := range []struct {
		name  string
		model string
		send  func(t *testing.T, ex *ClaudeExecutor, ctx context.Context, model string) error
	}{
		{name: "execute", model: "claude-haiku-4-5-20251001", send: sendMidSystemExecute},
		{name: "execute stream", model: "claude-haiku-4-5-20251001", send: sendMidSystemStream},
		{name: "count tokens", model: "claude-haiku-4-5-20251001", send: sendMidSystemCountTokens},
		{name: "execute legacy sonnet", model: "claude-sonnet-4-6", send: sendMidSystemExecute},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			ex := NewClaudeExecutor(midSystemConfig())
			err := test.send(t, ex, upstream.context(t, nil), test.model)
			assertMidSystemRejected(t, err, upstream)
		})
	}
}

func sendMidSystemExecute(t *testing.T, ex *ClaudeExecutor, ctx context.Context, model string) error {
	t.Helper()
	_, err := ex.Execute(ctx, midSystemAuth(), cliproxyexecutor.Request{
		Model: model, Payload: midSystemLegacyPayload(model),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	return err
}

func sendMidSystemStream(t *testing.T, ex *ClaudeExecutor, ctx context.Context, model string) error {
	t.Helper()
	result, err := ex.ExecuteStream(ctx, midSystemAuth(), cliproxyexecutor.Request{
		Model: model, Payload: midSystemLegacyPayload(model),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		return err
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			return chunk.Err
		}
	}
	return nil
}

func sendMidSystemCountTokens(t *testing.T, ex *ClaudeExecutor, ctx context.Context, model string) error {
	t.Helper()
	_, err := ex.CountTokens(ctx, midSystemAuth(), cliproxyexecutor.Request{
		Model: model, Payload: midSystemLegacyPayload(model),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	return err
}

// Payload rules run long after translation and can rewrite model and messages,
// so the guard has to read the finished body rather than an intermediate one.
func TestClaudeExecutor_PayloadOverrideCannotSmuggleLegacyMidSystemMessage(t *testing.T) {
	upstream := &midSystemUpstream{}
	cfg := midSystemConfig()
	cfg.Payload.Override = []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}},
		Params: map[string]any{"model": "claude-haiku-4-5-20251001"},
	}}
	ex := NewClaudeExecutor(cfg)

	// The caller addresses a model that accepts the turn; only the payload rule
	// turns it into the rejected pairing.
	_, err := ex.Execute(upstream.context(t, nil), midSystemAuth(), cliproxyexecutor.Request{
		Model: "claude-sonnet-5", Payload: midSystemLegacyPayload("claude-sonnet-5"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	assertMidSystemRejected(t, err, upstream)
}

// A caller may already have the exact role=system turn that cloaking would
// otherwise insert. The message-count proof must keep that turn caller-owned,
// so a later legacy model rewrite is rejected instead of silently consuming it.
func TestClaudeExecutor_PayloadOverrideDoesNotClaimMatchingCallerTurn(t *testing.T) {
	upstream := &midSystemUpstream{}
	cfg := midSystemConfig()
	cfg.Payload.Override = []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}},
		Params: map[string]any{"model": "claude-haiku-4-5-20251001"},
	}}
	ex := NewClaudeExecutor(cfg)
	payload := []byte(`{"model":"claude-sonnet-5","max_tokens":32,` +
		`"system":[{"type":"text","text":"Same rule"}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"system","content":[{"type":"text","text":"Same rule"}]}]}`)

	_, err := ex.Execute(upstream.context(t, nil), midSystemAuth(), cliproxyexecutor.Request{
		Model: "claude-sonnet-5", Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	assertMidSystemRejected(t, err, upstream)
}

// Cloaking relocates a caller's system prompt into a role=system turn for models
// that accept one. A payload rule can then rewrite the model to one that does
// not. Because the caller never wrote that turn, CPA must reconcile its own
// placement through the legacy reminder path instead of returning 400.
func TestClaudeExecutor_PayloadOverrideReconcilesRelocatedSystemPrompt(t *testing.T) {
	for _, test := range []struct {
		name string
		send func(t *testing.T, ex *ClaudeExecutor, ctx context.Context, payload []byte) error
	}{
		{name: "execute", send: func(t *testing.T, ex *ClaudeExecutor, ctx context.Context, payload []byte) error {
			_, err := ex.Execute(ctx, midSystemAuth(), cliproxyexecutor.Request{
				Model: "claude-sonnet-5", Payload: payload,
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			return err
		}},
		{name: "execute stream", send: func(t *testing.T, ex *ClaudeExecutor, ctx context.Context, payload []byte) error {
			result, err := ex.ExecuteStream(ctx, midSystemAuth(), cliproxyexecutor.Request{
				Model: "claude-sonnet-5", Payload: payload,
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if err != nil {
				return err
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					return chunk.Err
				}
			}
			return nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			cfg := midSystemConfig()
			cfg.Payload.Override = []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "*"}},
				Params: map[string]any{"model": "claude-haiku-4-5-20251001"},
			}}
			ex := NewClaudeExecutor(cfg)

			// Only a top-level system prompt: the caller never writes a
			// role=system turn.
			payload := []byte(`{"model":"claude-sonnet-5","max_tokens":32,` +
				`"system":[{"type":"text","text":"Caller top"}],` +
				`"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

			if err := test.send(t, ex, upstream.context(t, nil), payload); err != nil {
				t.Fatalf("request error = %v, want CPA's inserted turn reconciled", err)
			}
			if !upstream.called {
				t.Fatal("expected reconciled request to reach upstream")
			}
			if got := gjson.GetBytes(upstream.body, "model").String(); got != "claude-haiku-4-5-20251001" {
				t.Fatalf("upstream model = %q, want payload override preserved", got)
			}
			if gjson.GetBytes(upstream.body, `messages.#(role=="system")`).Exists() {
				t.Fatalf("reconciled body still carries role=system; body=%s", upstream.body)
			}
			if !strings.Contains(gjson.GetBytes(upstream.body, "messages.0.content").Raw, "<system-reminder>") ||
				!strings.Contains(gjson.GetBytes(upstream.body, "messages.0.content").Raw, "Caller top") {
				t.Fatalf("caller system prompt was not replayed as a legacy reminder; body=%s", upstream.body)
			}
		})
	}
}

// A confirmed native caller owns its wire. It gates the turn on the model
// itself, so CPA forwards the body untouched and lets the upstream answer.
func TestClaudeExecutor_ConfirmedNativeLegacyMidSystemMessageForwarded(t *testing.T) {
	upstream := &midSystemUpstream{}
	ex := NewClaudeExecutor(midSystemConfig())
	headers := claudeNativeHelperHeaders("claude-code-20250219,"+claudeNativeHelperCoreBetas, "gzip", false)

	if _, err := ex.Execute(upstream.context(t, headers), midSystemAuth(), cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: midSystemLegacyPayload("claude-haiku-4-5-20251001"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: headers}); err != nil {
		t.Fatalf("Execute() error = %v, want the native body forwarded", err)
	}
	if !upstream.called {
		t.Fatal("expected the native request to reach the upstream")
	}
	if !gjson.GetBytes(upstream.body, `messages.#(role=="system")`).Exists() {
		t.Fatalf("confirmed native caller lost its role=system turn; body=%s", upstream.body)
	}
}

// The rejection was measured against api.anthropic.com. A third-party gateway
// may map the same model ID onto something that accepts the turn, and answering
// locally would also stop failover to another credential or base URL.
func TestClaudeExecutor_LegacyMidSystemMessageForwardedToThirdPartyGateway(t *testing.T) {
	upstream := &midSystemUpstream{}
	ex := NewClaudeExecutor(&config.Config{ClaudeKey: []config.ClaudeKey{{
		APIKey: "key-123", BaseURL: "https://gateway.example",
	}}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key": "key-123", "base_url": "https://gateway.example",
	}}

	if _, err := ex.Execute(upstream.context(t, nil), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: midSystemLegacyPayload("claude-haiku-4-5-20251001"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err != nil {
		t.Fatalf("Execute() error = %v, want a third-party gateway to decide for itself", err)
	}
	if !upstream.called {
		t.Fatal("expected the request to reach the third-party gateway")
	}
}

// A model outside claudeLegacySystemReminderModels stays optimistic, matching
// how checkSystemInstructions treats unknown and future IDs.
func TestClaudeExecutor_SupportedModelMidSystemMessageForwarded(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5", "claude-sonnet-9"} {
		t.Run(model, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			ex := NewClaudeExecutor(midSystemConfig())
			if _, err := ex.Execute(upstream.context(t, nil), midSystemAuth(), cliproxyexecutor.Request{
				Model: model, Payload: midSystemLegacyPayload(model),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err != nil {
				t.Fatalf("Execute() error = %v, want the request forwarded", err)
			}
			if !upstream.called {
				t.Fatal("expected the request to reach the upstream")
			}
		})
	}
}

// The opt-in rescues the pairing by folding the turn into the system slot, so
// the guard must run after it rather than rejecting the request outright.
func TestClaudeExecutor_LegacyMidSystemMessageOptInStillRebuilds(t *testing.T) {
	upstream := &midSystemUpstream{}
	ex := NewClaudeExecutor(midSystemConfig())
	auth := midSystemAuth()
	auth.Attributes["rebuild_mid_system_message"] = "true"

	if _, err := ex.Execute(upstream.context(t, nil), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: midSystemLegacyPayload("claude-haiku-4-5-20251001"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err != nil {
		t.Fatalf("Execute() error = %v, want the opt-in rebuild to rescue the request", err)
	}
	if !upstream.called {
		t.Fatal("expected the rebuilt request to reach the upstream")
	}
	if gjson.GetBytes(upstream.body, `messages.#(role=="system")`).Exists() {
		t.Fatalf("opt-in rebuild left a role=system turn; body=%s", upstream.body)
	}
}

// The pairing must never originate inside CPA. A non-Claude caller reaches the
// Claude executor through a translator, and every translator hoists system
// content into the top-level system field, so no translated body can carry a
// role=system turn to a legacy model. This pins that guarantee: the guard is for
// callers that speak Claude natively, never for a translated request.
func TestTranslatedRequestNeverPairsLegacyModelWithMidSystemMessage(t *testing.T) {
	const legacyModel = "claude-haiku-4-5-20251001"
	for _, test := range []struct {
		name    string
		format  sdktranslator.Format
		payload string
	}{
		{name: "openai chat with a mid conversation system message", format: sdktranslator.FormatOpenAI,
			payload: `{"model":"` + legacyModel + `","messages":[{"role":"system","content":"Top rule"},{"role":"user","content":"hi"},{"role":"system","content":"Mid rule"},{"role":"assistant","content":"ok"},{"role":"user","content":"go"}]}`},
		{name: "openai chat ending on a system message", format: sdktranslator.FormatOpenAI,
			payload: `{"model":"` + legacyModel + `","messages":[{"role":"user","content":"hi"},{"role":"system","content":"Mid rule"}]}`},
		{name: "gemini with a system instruction", format: sdktranslator.FormatGemini,
			payload: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"Top rule"}]}}`},
		{name: "openai responses with instructions", format: sdktranslator.FormatOpenAIResponse,
			payload: `{"model":"` + legacyModel + `","instructions":"Top rule","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`},
		{name: "interactions with a system instruction", format: sdktranslator.FormatInteractions,
			payload: `{"model":"` + legacyModel + `","system_instruction":"Top rule","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			ex := NewClaudeExecutor(midSystemConfig())

			if _, err := ex.Execute(upstream.context(t, nil), midSystemAuth(), cliproxyexecutor.Request{
				Model: legacyModel, Payload: []byte(test.payload),
			}, cliproxyexecutor.Options{SourceFormat: test.format}); err != nil {
				t.Fatalf("Execute() error = %v, want the translated request forwarded", err)
			}
			if !upstream.called {
				t.Fatal("expected the translated request to reach the upstream")
			}
			// Without these the subject under test could drift away: a body that
			// no longer addresses the legacy model, or that lost the caller's
			// turns, would satisfy the role assertions for the wrong reason.
			if got := gjson.GetBytes(upstream.body, "model").String(); got != legacyModel {
				t.Fatalf("upstream model = %q, want the legacy model %q under test", got, legacyModel)
			}
			if got := len(gjson.GetBytes(upstream.body, "messages").Array()); got == 0 {
				t.Fatalf("upstream messages are empty, so the role assertions prove nothing; body=%s", upstream.body)
			}
			for _, role := range gjson.GetBytes(upstream.body, "messages.#.role").Array() {
				if strings.EqualFold(role.String(), "system") {
					t.Fatalf("translated body carries a system role; body=%s", upstream.body)
				}
			}
		})
	}
}

func TestClaudePayloadHasMidSystemMessage(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "mid conversation turn", want: true,
			payload: `{"messages":[{"role":"user","content":"a"},{"role":"system","content":"s"}]}`},
		{name: "role casing is ignored", want: true,
			payload: `{"messages":[{"role":"SySTeM","content":"s"}]}`},
		{name: "surrounding whitespace is ignored", want: true,
			payload: `{"messages":[{"role":" system ","content":"s"}]}`},
		{name: "only user and assistant turns",
			payload: `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`},
		{name: "top level system field is not a turn",
			payload: `{"system":[{"type":"text","text":"s"}],"messages":[{"role":"user","content":"a"}]}`},
		{name: "messages missing", payload: `{"model":"claude-haiku-4-5"}`},
		{name: "messages is not an array", payload: `{"messages":"system"}`},
		{name: "messages holds a bare string", payload: `{"messages":["system"]}`},
		{name: "system appears only in content", payload: `{"messages":[{"role":"user","content":"role: system"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := claudePayloadHasMidSystemMessage([]byte(test.payload)); got != test.want {
				t.Fatalf("claudePayloadHasMidSystemMessage = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateClaudeMidSystemMessageModel(t *testing.T) {
	const turn = `,"messages":[{"role":"user","content":"a"},{"role":"system","content":"s"}]}`
	for _, test := range []struct {
		name       string
		payload    string
		confirmed  bool
		thirdParty bool
		wantError  bool
	}{
		{name: "legacy model is rejected", wantError: true,
			payload: `{"model":"claude-haiku-4-5-20251001"` + turn},
		{name: "vendor prefixed legacy model is rejected", wantError: true,
			payload: `{"model":"anthropic/claude-sonnet-4-6"` + turn},
		{name: "model casing is ignored", wantError: true,
			payload: `{"model":"Claude-Haiku-4-5-20251001"` + turn},
		{name: "confirmed native keeps the passthrough", confirmed: true,
			payload: `{"model":"claude-haiku-4-5-20251001"` + turn},
		{name: "third party gateway decides for itself", thirdParty: true,
			payload: `{"model":"claude-haiku-4-5-20251001"` + turn},
		{name: "supported model is forwarded",
			payload: `{"model":"claude-sonnet-5"` + turn},
		{name: "unknown model stays optimistic",
			payload: `{"model":"claude-sonnet-9"` + turn},
		{name: "legacy model without the turn is forwarded",
			payload: `{"model":"claude-haiku-4-5-20251001","messages":[{"role":"user","content":"a"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateClaudeMidSystemMessageModel([]byte(test.payload), test.confirmed, !test.thirdParty)
			if test.wantError != (err != nil) {
				t.Fatalf("validateClaudeMidSystemMessageModel error = %v, want error %v", err, test.wantError)
			}
			if err == nil {
				return
			}
			if !strings.Contains(err.Error(), gjson.Get(test.payload, "model").String()) {
				t.Fatalf("error = %v, want the offending model named", err)
			}
		})
	}
}
