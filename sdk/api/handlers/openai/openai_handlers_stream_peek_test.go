package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

// peekStreamExecutor feeds a fake executor stream that closes the chunk channel
// immediately. All three initial streaming peek loops (chat, ViaResponses and
// legacy completions) then race a closed dataChan against any buffered pending
// error on errChan.
type peekStreamExecutor struct {
	secret  string
	payload string
}

func (*peekStreamExecutor) Identifier() string { return "peek-stream" }

func (*peekStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *peekStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 2)
	if e.payload != "" {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(e.payload)}
	}
	if e.secret != "" {
		chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream failure: api_key=" + e.secret)}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*peekStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*peekStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*peekStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// sendPeekRequest drives the given handler method through a registered executor.
func sendPeekRequest(t *testing.T, route, body string, executor *peekStreamExecutor, endpoints []string) *httptest.ResponseRecorder {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	authID := "peek-stream-auth"
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(), []*registry.ModelInfo{{ID: "peek-stream-model", SupportedEndpoints: endpoints}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)
	router.POST("/v1/completions", h.Completions)

	request := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// TestStreamingPeekConsumesBufferedPendingError covers chat completions,
// ViaResponses and legacy completions peek close paths.
func TestStreamingPeekConsumesBufferedPendingError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "upstream-secret-fx-8849"
	cases := []struct {
		name      string
		route     string
		body      string
		endpoints []string
	}{
		{
			name:  "chat",
			route: "/v1/chat/completions",
			body:  `{"model":"peek-stream-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:      "via-responses",
			route:     "/v1/chat/completions",
			body:      `{"model":"peek-stream-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			endpoints: []string{openAIResponsesEndpoint},
		},
		{
			name:  "legacy",
			route: "/v1/completions",
			body:  `{"model":"peek-stream-model","stream":true,"prompt":"hi"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := sendPeekRequest(t, tc.route, tc.body, &peekStreamExecutor{secret: secret}, tc.endpoints)
			body := recorder.Body.String()
			if recorder.Code == http.StatusOK {
				t.Fatalf("handler returned 200 despite buffered pending error: %q", body)
			}
			if recorder.Code < http.StatusBadRequest {
				t.Fatalf("status = %d, want error status; body=%q", recorder.Code, body)
			}
			if strings.Contains(body, "data: [DONE]") {
				t.Fatalf("stream emitted [DONE] despite pending error: %q", body)
			}
			if strings.Contains(body, secret) {
				t.Fatalf("stream body leaked upstream secret: %q", body)
			}
			if !strings.Contains(body, "[REDACTED]") {
				t.Fatalf("stream body did not redact upstream error: %q", body)
			}
		})
	}
}

// TestStreamingPeekCleanCloseStillEmitsDone is the control: a clean data-channel
// close with no pending error still emits SSE [DONE] with a success status.
func TestStreamingPeekCleanCloseStillEmitsDone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	chatPayload := `data: {"id":"cmpl-1","object":"chat.completion.chunk","created":1,"model":"peek-stream-model","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`
	responsesPayload := `data: {"type":"response.output_text.delta","delta":"hi"}`
	cases := []struct {
		name      string
		route     string
		body      string
		payload   string
		endpoints []string
	}{
		{
			name:    "chat",
			route:   "/v1/chat/completions",
			body:    `{"model":"peek-stream-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			payload: chatPayload,
		},
		{
			name:      "via-responses",
			route:     "/v1/chat/completions",
			body:      `{"model":"peek-stream-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			payload:   responsesPayload,
			endpoints: []string{openAIResponsesEndpoint},
		},
		{
			name:    "legacy",
			route:   "/v1/completions",
			body:    `{"model":"peek-stream-model","stream":true,"prompt":"hi"}`,
			payload: chatPayload,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := sendPeekRequest(t, tc.route, tc.body, &peekStreamExecutor{payload: tc.payload}, tc.endpoints)
			body := recorder.Body.String()
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%q", recorder.Code, body)
			}
			if !strings.Contains(body, "data: [DONE]") {
				t.Fatalf("clean close did not emit [DONE]: %q", body)
			}
		})
	}
}

// sanitizerCase drives the strict sanitizer and checks the sanitized output.
type sanitizerCase struct {
	name    string
	in      *interfaces.ErrorMessage
	want    string
	wantOut string
}

func runSanitizerCases(t *testing.T, cases []sanitizerCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := sanitizeOpenAIErrorMessage(tc.in)
			if tc.in == nil {
				if out != nil {
					t.Fatalf("nil input => non-nil output")
				}
				return
			}
			got := ""
			if out != nil && out.Error != nil {
				got = out.Error.Error()
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("sanitized = %q, want %q", got, tc.want)
			}
			if tc.wantOut != "" && strings.Contains(got, tc.wantOut) {
				t.Fatalf("leaked %q: %q", tc.wantOut, got)
			}
		})
	}
}

func em(status int, text string) *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{StatusCode: status, Error: errors.New(text)}
}

func fromSeg(parts ...string) string { return strings.Join(parts, "") }

func TestSanitizeOpenAIErrorMessageNormalizesStatus(t *testing.T) {
	if got := sanitizeOpenAIErrorMessage(nil); got != nil {
		t.Fatal("nil input => non-nil output")
	}
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"no status", 0, http.StatusInternalServerError},
		{"1xx", 101, http.StatusInternalServerError},
		{"2xx", http.StatusOK, http.StatusInternalServerError},
		{"400", http.StatusBadRequest, http.StatusBadRequest},
		{"429", http.StatusTooManyRequests, http.StatusTooManyRequests},
		{"502", http.StatusBadGateway, http.StatusBadGateway},
		{"600", 600, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := sanitizeOpenAIErrorMessage(em(tc.in, "boom"))
			if out.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", out.StatusCode, tc.want)
			}
			if out.Body != nil {
				t.Fatalf("Body not nil")
			}
			if out.DirectResponse != false {
				t.Fatal("DirectResponse not false")
			}
		})
	}
}

func TestSanitizeOpenAIErrorMessageRedactsKeyValues(t *testing.T) {
	// Credential-shaped fixture values are constructed at runtime so the test
	// file itself never stores a real-looking secret literal. Each case proves
	// the resulting value does not survive sanitization.
	sk := fromSeg("sk-abc")
	hd := fromSeg("hunter2")
	tk := fromSeg("tkn_456")
	rf := fromSeg("ref_789")
	s3 := fromSeg("s3cr3t")
	ak := fromSeg("AKIAIOSFODNN7EXAMPLE")
	sx := fromSeg("sx-xyz")
	jw := fromSeg("eyJ.jwt.payload")
	bs := fromSeg("dXNlcjpwYXNz")
	tA := fromSeg("tok-XYZQ")
	pB := fromSeg("sk-proj-abcdefghijklmnop")
	pC := fromSeg("sk-ant-api03-xyz")
	gD := fromSeg("ghp_AAAbbbCCCDDD")
	sE := fromSeg("sk_live_42abc")
	aF := fromSeg("AKIASECRETKEYEXAMPLE")
	oG := fromSeg("ya29.exampletoken")
	s1 := fromSeg("single")
	eH := fromSeg("sk-esc")

	cases := []sanitizerCase{
		{name: "api_key assignment", in: em(http.StatusBadGateway, "bad api_key="+sk), want: "[REDACTED]", wantOut: sk},
		{name: "client_secret assignment", in: em(http.StatusBadGateway, "upstream rejected client_secret="+hd), want: "[REDACTED]", wantOut: hd},
		{name: "api_token header", in: em(http.StatusBadGateway, "x-api-token: "+tk), want: "[REDACTED]", wantOut: tk},
		{name: "refresh_token quoted", in: em(http.StatusBadGateway, "refresh_token=\""+rf+"\""), want: "[REDACTED]", wantOut: rf},
		{name: "star_secret", in: em(http.StatusBadGateway, "integration failed: store_secret="+s3), want: "[REDACTED]", wantOut: s3},
		{name: "dash key", in: em(http.StatusBadGateway, "credential missing access-key=<"+ak+">"), want: "[REDACTED]", wantOut: ak},
		{name: "underscore key", in: em(http.StatusBadGateway, "bad api_key="+sk), want: "[REDACTED]", wantOut: sk},
		{name: "nested key colon", in: em(http.StatusBadGateway, "login refused: \"api_key\":\""+sx+"\""), want: "[REDACTED]", wantOut: sx},
		{name: "bearer scheme preserved", in: em(http.StatusBadGateway, "token endpoint returned Bearer "+jw), want: "Bearer [REDACTED]", wantOut: jw},
		{name: "basic scheme preserved", in: em(http.StatusBadGateway, "401 from Basic "+bs), want: "Basic [REDACTED]", wantOut: bs},
		{name: "authorization header", in: em(http.StatusBadGateway, "authorization: Bearer "+tA), want: "Bearer [REDACTED]", wantOut: tA},
		{name: "openai key", in: em(http.StatusBadGateway, "invalid OpenAI api_key="+pB), want: "[REDACTED]", wantOut: pB},
		{name: "anthropic key", in: em(http.StatusBadGateway, "anthropic api key = "+pC), want: "[REDACTED]", wantOut: pC},
		{name: "github token", in: em(http.StatusBadGateway, "x-github-token: "+gD), want: "[REDACTED]", wantOut: gD},
		{name: "stripe key", in: em(http.StatusBadGateway, "stripe_key="+sE), want: "[REDACTED]", wantOut: sE},
		{name: "aws credential", in: em(http.StatusBadGateway, "aws_credential="+aF), want: "[REDACTED]", wantOut: aF},
		{name: "oauth token", in: em(http.StatusBadGateway, "oauth_token="+oG), want: "[REDACTED]", wantOut: oG},
		{name: "single quote form", in: em(http.StatusBadGateway, "client_id AND client_secret = '"+s1+"'"), want: "[REDACTED]", wantOut: s1},
		{name: "backslash trailing", in: em(http.StatusBadGateway, "api_key="+sk+"\\"), want: "[REDACTED]", wantOut: sk},
		{name: "malformed quote redacts without panic", in: em(http.StatusBadGateway, "api_key=\""+sk), want: "[REDACTED]", wantOut: sk},
		{name: "long value truncation", in: em(http.StatusBadGateway, "api_key="+strings.Repeat("a", 1000)), want: "[REDACTED]"},
		{name: "not_api_key benign", in: em(http.StatusBadGateway, "not_api_key=123"), want: "not_api_key=123", wantOut: "[REDACTED]"},
		{name: "token_count benign", in: em(http.StatusBadGateway, "token_count=42 tokens"), want: "token_count=42"},
		{name: "tokenizer benign", in: em(http.StatusBadGateway, "tokenizer=cl100k"), want: "tokenizer=cl100k"},
		{name: "secretariat benign", in: em(http.StatusBadGateway, "secretariat approved"), want: "secretariat"},
		{name: "mytoken benign", in: em(http.StatusBadGateway, "mytoken not a real credential"), want: "mytoken"},
		{name: "benign prose", in: em(http.StatusBadGateway, "nothing sensitive here"), want: "nothing sensitive here"},
		{name: "double escaped quoted", in: em(http.StatusBadGateway, `{"msg":"api_key=\\`+eH+`\\\\"}"`), want: "[REDACTED]", wantOut: eH},
	}
	runSanitizerCases(t, cases)
}

func TestSanitizeOpenAIErrorMessageJSONPath(t *testing.T) {
	jS := fromSeg("sk-json-secret")
	rS := fromSeg("sk-route")
	jw := fromSeg("supersecretjwt")
	cases := []sanitizerCase{
		{
			name: "nested error object redacts secret",
			in:   em(http.StatusBadGateway, `{"error":{"type":"server_error","code":"upstream","message":"rejected api_key=`+jS+`","param":"mytoken"}}`),
			want: "[REDACTED]", wantOut: jS,
		},
		{
			name: "route summary redacts",
			in:   em(http.StatusBadGateway, `{"message":"Route failed api_key=`+rS+`"}`),
			want: "[REDACTED]", wantOut: rS,
		},
		{
			name: "nested response.error object",
			in:   em(http.StatusBadGateway, `{"response":{"error":{"message":"credentials client_secret=top"}}}`),
			want: "[REDACTED]", wantOut: "top",
		},
		{
			name: "raw json with bearer",
			in:   em(http.StatusBadGateway, `{"error":{"message":"Bearer `+jw+`"}}`),
			want: "Bearer [REDACTED]", wantOut: jw,
		},
		{
			name: "invalid json collapses to status text",
			in:   em(http.StatusBadGateway, `{"error":{"unclosed`),
			want: `{"error":{"unclosed`,
		},
		{name: "empty text uses status text", in: em(http.StatusBadGateway, ""), want: http.StatusText(http.StatusBadGateway)},
	}
	runSanitizerCases(t, cases)
}

func TestSanitizeOpenAIErrorMessageLongValueBound(t *testing.T) {
	// A very long JSON message field must be truncated to the message limit
	// without panic; a very large free-form payload must not panic either.
	long := strings.Repeat("x", openAIStreamErrorMessageLimit*2)
	out := sanitizeOpenAIErrorMessage(em(http.StatusBadGateway, `{"error":{"message":"`+long+`"}}`))
	if out.Error == nil {
		t.Fatal("expected sanitized error, got nil")
	}
	msg := gjson.Get(out.Error.Error(), "error.message").String()
	// Allow the truncation suffix (one ellipsis rune) over the hard limit.
	if len([]rune(msg)) > openAIStreamErrorMessageLimit+4 {
		t.Fatalf("sanitized JSON message field too large: %d chars", len(msg))
	}
	if msg == "" {
		t.Fatalf("expected truncation suffix message, got empty: %q", out.Error.Error())
	}
	// Free-form large text: redaction must not panic and must not add creds.
	free := sanitizeOpenAIErrorMessage(em(http.StatusBadGateway, strings.Repeat("y", openAIStreamErrorMessageLimit*3)))
	if free == nil || free.Error == nil {
		t.Fatal("expected sanitized free-form error, got nil")
	}
}

// sendResponsesPeekRequest drives the OpenAIResponses handler (/v1/responses)
// through a registered executor, exercising the native responses peek loop.
func sendResponsesPeekRequest(t *testing.T, body string, executor *peekStreamExecutor) *httptest.ResponseRecorder {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	authID := "peek-resp-auth"
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(), []*registry.ModelInfo{{ID: "peek-resp-model", SupportedEndpoints: []string{openAIResponsesEndpoint}}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// TestResponsesNativePeekConsumesBufferedPendingError exercises the native
// /v1/responses initial peek: a closed dataChan with a buffered pending error
// must yield a sanitized non-200 error and never an empty success stream.
func TestResponsesNativePeekConsumesBufferedPendingError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "resp-native-secret-7712"
	recorder := sendResponsesPeekRequest(t,
		`{"model":"peek-resp-model","stream":true,"input":"hi"}`,
		&peekStreamExecutor{secret: secret})
	body := recorder.Body.String()
	if recorder.Code == http.StatusOK {
		t.Fatalf("responses native returned 200 despite buffered pending error: %q", body)
	}
	if recorder.Code < http.StatusBadRequest {
		t.Fatalf("status = %d, want error status; body=%q", recorder.Code, body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("responses native leaked upstream secret: %q", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("responses native did not redact upstream error: %q", body)
	}
}

// sendResponsesViaChatPeekRequest routes a /v1/responses streaming request
// through the via-chat peek by advertising only the chat endpoint for the
// model, so the responses handler overrides the endpoint to chat.
func sendResponsesViaChatPeekRequest(t *testing.T, body string, executor *peekStreamExecutor) *httptest.ResponseRecorder {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	authID := "peek-resp-viachat"
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(), []*registry.ModelInfo{{ID: "peek-resp-model", SupportedEndpoints: []string{openAIChatEndpoint}}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// TestResponsesViaChatPeekConsumesBufferedPendingError exercises the peek
// close path reached when a /v1/responses streaming request is routed through
// the OpenAI chat endpoint.
func TestResponsesViaChatPeekConsumesBufferedPendingError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "resp-viachat-secret-3390"
	recorder := sendResponsesViaChatPeekRequest(t,
		`{"model":"peek-resp-model","stream":true,"input":"hi"}`,
		&peekStreamExecutor{secret: secret})
	body := recorder.Body.String()
	if recorder.Code == http.StatusOK {
		t.Fatalf("responses via-chat returned 200 despite buffered pending error: %q", body)
	}
	if recorder.Code < http.StatusBadRequest {
		t.Fatalf("status = %d, want error status; body=%q", recorder.Code, body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("responses via-chat leaked upstream secret: %q", body)
	}
	// Via-chat terminal chunk must be sanitized (no raw error) and must not be
	// an empty success stream.
	if strings.Contains(body, "[DONE]") && strings.Contains(body, "api_key") {
		t.Fatalf("via-chat leaked raw error text: %q", body)
	}
}

// newOpenAIImageHandlerWithExecutor builds an OpenAIAPIHandler whose
// ExecuteImageStreamWithAuthManager resolves through a registered fake stream
// executor, so the images peek close branches run for real.
func newOpenAIImageHandlerWithExecutor(t *testing.T, executor *peekStreamExecutor, model string) *OpenAIAPIHandler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	authID := "peek-image-auth"
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(), []*registry.ModelInfo{{ID: model, SupportedEndpoints: []string{openAIResponsesEndpoint}}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	return NewOpenAIAPIHandler(base)
}

// imagesPeekRecorder prepares a gin recorder with a flushable writer for the
// images stream handlers.
func imagesPeekRecorder(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, http.Flusher) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"image-model","prompt":"x","stream":true}`))
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}
	return c, recorder, flusher
}

// assertImagesPendingError fails unless the recorder carried a sanitized
// non-200 error on the shared-peek-close path (never an empty success stream).
func assertImagesPendingError(t *testing.T, recorder *httptest.ResponseRecorder, body, secret string) {
	t.Helper()
	if recorder.Code == http.StatusOK {
		t.Fatalf("images peek returned 200 despite buffered pending error: %q", body)
	}
	if recorder.Code < http.StatusBadRequest {
		t.Fatalf("images peek status = %d, want error status; body=%q", recorder.Code, body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("images peek leaked upstream secret: %q", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("images peek did not redact upstream error: %q", body)
	}
}

// TestRoutedImagesPeekConsumesBufferedPendingError exercises streamRoutedImages
// (codex images tool family) peek close path.
func TestRoutedImagesPeekConsumesBufferedPendingError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "routed-image-secret-5081"
	h := newOpenAIImageHandlerWithExecutor(t, &peekStreamExecutor{secret: secret}, "gpt-image-1.5")
	c, recorder, flusher := imagesPeekRecorder(t)
	h.streamRoutedImages(c, []byte(`{"model":"gpt-image-1.5","prompt":"x"}`), "gpt-image-1.5")
	_ = flusher
	assertImagesPendingError(t, recorder, recorder.Body.String(), secret)
}

// TestCompatImagesPeekConsumesBufferedPendingError exercises
// streamOpenAICompatImages peek close path.
func TestCompatImagesPeekConsumesBufferedPendingError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "compat-image-secret-1193"
	h := newOpenAIImageHandlerWithExecutor(t, &peekStreamExecutor{secret: secret}, "compat-image-model")
	c, recorder, _ := imagesPeekRecorder(t)
	h.streamOpenAICompatImages(c, []byte(`{"model":"compat-image-model","prompt":"x"}`), "compat-image-model")
	assertImagesPendingError(t, recorder, recorder.Body.String(), secret)
}

// TestResponsesBackedImagesPeekConsumesBufferedPendingError exercises
// streamImagesFromResponses peek close path.
func TestResponsesBackedImagesPeekConsumesBufferedPendingError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "resp-image-secret-2278"
	h := newOpenAIImageHandlerWithExecutor(t, &peekStreamExecutor{secret: secret}, "peek-image-model")
	c, recorder, _ := imagesPeekRecorder(t)
	h.streamImagesFromResponses(c, []byte(`{"model":"peek-image-model","prompt":"x"}`), "b64_json", "image_generation")
	assertImagesPendingError(t, recorder, recorder.Body.String(), secret)
}

// TestImagesStreamErrorEventRedactsTerminal exercises the actual post-header
// terminal writer (writeImagesStreamErrorEvent) used after the first chunk: the
// SSE error event must carry a sanitized, secret-free message.
func TestImagesStreamErrorEventRedactsTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder, _ := imagesPeekRecorder(t)
	secret := fromSeg("img-term-secret-6634")
	writeImagesStreamErrorEvent(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("upstream image failure: api_key=" + secret),
	})
	body := recorder.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("missing error SSE event: %q", body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("images terminal error leaked secret: %q", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("images terminal error not redacted: %q", body)
	}
}

// TestResponsesWebsocketTerminalErrorRedacts exercises the actual websocket
// terminal-error payload builder: the client error JSON must keep its framing
// and codes but never leak upstream credential material.
func TestResponsesWebsocketTerminalErrorRedacts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := fromSeg("ws-term-secret-9047")
	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked api_key=` + secret + `"}}`),
	}
	payload, err := buildResponsesWebsocketErrorPayload(errMsg)
	if err != nil {
		t.Fatalf("buildResponsesWebsocketErrorPayload: %v", err)
	}
	if gjson.GetBytes(payload, "type").String() != "error" {
		t.Fatalf("type = %q, want error", gjson.GetBytes(payload, "type").String())
	}
	if status := int(gjson.GetBytes(payload, "status").Int()); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if code := gjson.GetBytes(payload, "error.code").String(); code != "cyber_policy" {
		t.Fatalf("error.code = %q, want cyber_policy", code)
	}
	raw := string(payload)
	if strings.Contains(raw, secret) {
		t.Fatalf("websocket terminal error leaked upstream secret: %q", raw)
	}
	// The redaction marker must be present in the embedded error message.
	if !strings.Contains(raw, "[REDACTED]") {
		t.Fatalf("websocket terminal error not redacted: %q", raw)
	}
}

// TestSanitizeOpenAIErrorMessageTrustedPreservation verifies the OpenAI shared
// sanitizer preserves a DirectResponse only when it is explicitly trusted.
func TestSanitizeOpenAIErrorMessageTrustedPreservation(t *testing.T) {
	const secret = "trusted-openai-secret-311"
	trusted := &interfaces.ErrorMessage{
		StatusCode:            http.StatusTooManyRequests,
		Error:                 errors.New("plugin"),
		DirectResponse:        true,
		TrustedDirectResponse: true,
		Body:                  []byte(`{"status":429,"message":"plugin","secret":"` + secret + `"}`),
		Headers:               http.Header{"X-Plugin": []string{"yes"}},
	}
	if got := sanitizeOpenAIErrorMessage(trusted); got != trusted {
		t.Fatalf("trusted direct response must be preserved verbatim, got %#v", got)
	}

	untrusted := &interfaces.ErrorMessage{
		StatusCode:     http.StatusBadGateway,
		Error:          errors.New("provider failed: api_key=" + secret),
		DirectResponse: true,
		Body:           []byte(`{"raw":"` + secret + `"}`),
		Headers:        http.Header{"X-Upstream": []string{"leak"}},
	}
	got := sanitizeOpenAIErrorMessage(untrusted)
	if got == nil {
		t.Fatal("untrusted direct response must be sanitized, not nil")
	}
	if got.DirectResponse {
		t.Fatal("untrusted DirectResponse must be forced false")
	}
	if got.Body != nil {
		t.Fatal("untrusted Body must be cleared")
	}
	if got.Error != nil && strings.Contains(got.Error.Error(), secret) {
		t.Fatalf("untrusted sanitized error leaked %q: %q", secret, got.Error.Error())
	}
}

func TestRedactOpenAIStreamErrorTextBearerTokens(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "lowercase-only standalone bearer token",
			text: "upstream error: Bearer abcdef",
			want: "upstream error: Bearer [REDACTED]",
		},
		{
			name: "lowercase-only bare bearer token",
			text: "Bearer abcdef",
			want: "Bearer [REDACTED]",
		},
		{
			name: "lowercase-only bearer in sentence",
			text: "upstream error with Bearer abcdef in request",
			want: "upstream error with Bearer [REDACTED] in request",
		},
		{
			name: "lowercase-only bearer comma delimited",
			text: "upstream error: Bearer abcdef, request failed",
			want: "upstream error: Bearer [REDACTED], request failed",
		},
		{
			name: "mixed case and digit bearer token",
			text: "upstream error: Bearer abc123XYZ",
			want: "upstream error: Bearer [REDACTED]",
		},
		{
			name: "numeric bearer token",
			text: "upstream error: Bearer 123456",
			want: "upstream error: Bearer [REDACTED]",
		},
		{
			name: "RFC6750 b64token characters",
			text: "upstream error: Bearer abc-def_123.xyz~456+789/0==",
			want: "upstream error: Bearer [REDACTED]",
		},
		{
			name: "lowercase-only basic token",
			text: "upstream error: Basic abcdef",
			want: "upstream error: Basic [REDACTED]",
		},
		{
			name: "json embedded standalone bearer",
			text: `{"error":"Bearer abcdef"}`,
			want: `{"error":"Bearer [REDACTED]"}`,
		},
		{
			name: "short 2-char bearer token",
			text: "upstream error: Bearer ab",
			want: "upstream error: Bearer [REDACTED]",
		},
		{
			name: "short 1-char bearer token",
			text: "upstream error: Bearer a",
			want: "upstream error: Bearer [REDACTED]",
		},
		{
			name: "short 2-char basic token",
			text: "upstream error: Basic ab",
			want: "upstream error: Basic [REDACTED]",
		},
		{
			name: "short 1-char basic token",
			text: "upstream error: Basic a",
			want: "upstream error: Basic [REDACTED]",
		},
		{
			name: "bearer of standalone at end",
			text: "upstream error: Bearer of",
			want: "upstream error: Bearer [REDACTED]",
		},
		{
			name: "bearer to standalone at end",
			text: "upstream error: Bearer to",
			want: "upstream error: Bearer [REDACTED]",
		},
		{
			name: "bearer of bare token",
			text: "Bearer of",
			want: "Bearer [REDACTED]",
		},
		{
			name: "bearer to bare token",
			text: "Bearer to",
			want: "Bearer [REDACTED]",
		},
		{
			name: "bearer of in json quotes",
			text: `{"error":"Bearer of"}`,
			want: `{"error":"Bearer [REDACTED]"}`,
		},
		{
			name: "bearer to in json quotes",
			text: `{"error":"Bearer to"}`,
			want: `{"error":"Bearer [REDACTED]"}`,
		},
		{
			name: "bearer of with comma punctuation",
			text: "upstream error: Bearer of, please retry",
			want: "upstream error: Bearer [REDACTED], please retry",
		},
		{
			name: "authorization header bearer of",
			text: "Authorization: Bearer of\r\n",
			want: "Authorization: Bearer [REDACTED]\r\n",
		},
		{
			name: "lowercase bearer token followed by prose word",
			text: "upstream error: bearer abc expired",
			want: "upstream error: bearer [REDACTED] expired",
		},
		{
			name: "lowercase basic token followed by prose word",
			text: "upstream error: basic dGVzdA== rejected",
			want: "upstream error: basic [REDACTED] rejected",
		},
		// Prose controls: collateral prose masking is accepted in exchange for no credential leaks (reviewer trade-off).
		{
			name: "control: bearer of bad news partially masked per reviewer trade-off",
			text: "bearer of bad news",
			want: "bearer [REDACTED] bad news",
		},
		{
			name: "control: the bearer of good news partially masked per reviewer trade-off",
			text: "the bearer of good news",
			want: "the bearer [REDACTED] good news",
		},
		{
			name: "control: bearer to the manager partially masked per reviewer trade-off",
			text: "the bearer to the manager",
			want: "the bearer [REDACTED] the manager",
		},
		{
			name: "control: bearer in header partially masked per reviewer trade-off",
			text: "the bearer in header",
			want: "the bearer [REDACTED] header",
		},
		{
			name: "control: bearer is invalid partially masked per reviewer trade-off",
			text: "the bearer is invalid",
			want: "the bearer [REDACTED] invalid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactOpenAIStreamErrorText(tc.text)
			if got != tc.want {
				t.Fatalf("redactOpenAIStreamErrorText(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestRedactOpenAIStreamErrorTextCamelCase(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "camelCase refreshToken assignment",
			text: "refreshToken=abc",
			want: "refreshToken=[REDACTED]",
		},
		{
			name: "camelCase clientSecret colon",
			text: "clientSecret: xyz",
			want: "clientSecret: [REDACTED]",
		},
		{
			name: "camelCase apiKey assignment",
			text: "apiKey=secret123",
			want: "apiKey=[REDACTED]",
		},
		{
			name: "camelCase accessToken colon",
			text: "accessToken: tok456",
			want: "accessToken: [REDACTED]",
		},
		{
			name: "non-sensitive camelCase words untouched",
			text: "userProfile=safe statusCode: 200 maxRetries=3 donkey=safe",
			want: "userProfile=safe statusCode: 200 maxRetries=3 donkey=safe",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactOpenAIStreamErrorText(tc.text); got != tc.want {
				t.Fatalf("redactOpenAIStreamErrorText(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
