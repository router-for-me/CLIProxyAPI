package handlers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type directResponseTestError struct {
	status int
	body   []byte
	direct bool
}

func (e directResponseTestError) Error() string        { return string(e.body) }
func (e directResponseTestError) StatusCode() int      { return e.status }
func (e directResponseTestError) DirectResponse() bool { return e.direct }
func (e directResponseTestError) ResponseBody() []byte { return e.body }

type responseBodyOnlyTestError struct {
	status int
	body   []byte
}

func (e responseBodyOnlyTestError) Error() string        { return string(e.body) }
func (e responseBodyOnlyTestError) StatusCode() int      { return e.status }
func (e responseBodyOnlyTestError) ResponseBody() []byte { return e.body }

func TestWriteErrorResponse_AddonHeadersDisabledByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New("rate limit"),
		Addon: http.Header{
			"Retry-After":  {"30"},
			"X-Request-Id": {"req-1"},
		},
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After should be empty when passthrough is disabled, got %q", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "" {
		t.Fatalf("X-Request-Id should be empty when passthrough is disabled, got %q", got)
	}
}

func TestWriteErrorResponseDirectResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer.Header().Set("X-Cpa-Trace-Id", "local-trace")
	c.Writer.Header().Set("Access-Control-Allow-Origin", "https://trusted.example")

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode:     http.StatusForbidden,
		DirectResponse: true,
		Body:           []byte(`{"error":"blocked"}`),
		Headers: http.Header{
			"Content-Type":                {"application/problem+json"},
			"X-Plugin-Policy":             {"blocked"},
			"X-Cpa-Trace-Id":              {"plugin-trace"},
			"Access-Control-Allow-Origin": {"https://untrusted.example"},
		},
	})

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if got := recorder.Body.String(); got != `{"error":"blocked"}` {
		t.Fatalf("body = %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("X-Plugin-Policy"); got != "blocked" {
		t.Fatalf("X-Plugin-Policy = %q", got)
	}
	if got := recorder.Header().Get("X-Cpa-Trace-Id"); got != "local-trace" {
		t.Fatalf("X-Cpa-Trace-Id = %q, want local value", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://trusted.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want trusted origin", got)
	}
}

func TestExecutionErrorMessagePreservesMarkedResponseBodyExactly(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        []byte
		contentType string
	}{
		{name: "json", status: http.StatusBadRequest, body: []byte(`{"error":"invalid_request"}`), contentType: "application/json"},
		{name: "text", status: http.StatusBadGateway, body: []byte("provider unavailable"), contentType: "text/plain; charset=utf-8"},
		{name: "multiline", status: http.StatusTooManyRequests, body: []byte("first line\r\nsecond line\n"), contentType: "text/plain; charset=utf-8"},
		{name: "empty", status: http.StatusUnauthorized, body: []byte{}, contentType: "application/json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

			errUpstream := directResponseTestError{status: tt.status, body: tt.body, direct: true}
			msg := executionErrorMessage(errUpstream)
			if msg == nil || !msg.DirectResponse {
				t.Fatalf("executionErrorMessage() direct response = %#v, want true", msg)
			}
			NewBaseAPIHandlers(nil, nil).WriteErrorResponse(c, msg)
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.status)
			}
			if got := recorder.Body.Bytes(); !bytes.Equal(got, tt.body) {
				t.Fatalf("body = %q, want exact body %q", got, tt.body)
			}
			if got := recorder.Header().Get("Content-Type"); got != tt.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, tt.contentType)
			}
		})
	}
}

func TestExecutionErrorMessageDoesNotTrustResponseBodyWithoutMarker(t *testing.T) {
	errUnmarked := responseBodyOnlyTestError{
		status: http.StatusBadGateway,
		body:   []byte("unmarked upstream body"),
	}
	msg := executionErrorMessage(errUnmarked)
	if msg == nil {
		t.Fatal("executionErrorMessage() returned nil")
	}
	if msg.DirectResponse {
		t.Fatal("executionErrorMessage() trusted an unmarked response body")
	}
}

func TestInternalConcurrencyBusyWritesRetryAfterWithoutPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      coreauth.NewHomeConcurrencyBusyError("busy", 750*time.Millisecond),
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}

func TestWriteErrorResponseHomeBusyNormalAndStreamHeaders(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "stream"}[stream], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if stream {
				c.Request.Header.Set("Accept", "text/event-stream")
			}

			handler := NewBaseAPIHandlers(nil, nil)
			handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests,
				Error:      coreauth.NewHomeConcurrencyBusyError("busy", 750*time.Millisecond),
			})
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
			}
			if got := recorder.Header().Get("Retry-After"); got != "1" {
				t.Fatalf("Retry-After = %q, want 1", got)
			}
		})
	}
}

func TestWriteErrorResponse_AddonHeadersEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Writer.Header().Set("X-Request-Id", "old-value")
	c.Writer.Header().Set("x-cpa-trace-id", "local-trace")
	c.Writer.Header().Set("Access-Control-Expose-Headers", "x-cpa-trace-id")

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New("rate limit"),
		Addon: http.Header{
			"Retry-After":                   {"30"},
			"X-Request-Id":                  {"new-1", "new-2"},
			"x-cpa-trace-id":                {"upstream-trace"},
			"Access-Control-Expose-Headers": {"upstream-header"},
		},
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After = %q, want %q", got, "30")
	}
	if got := recorder.Header().Values("X-Request-Id"); !reflect.DeepEqual(got, []string{"new-1", "new-2"}) {
		t.Fatalf("X-Request-Id = %#v, want %#v", got, []string{"new-1", "new-2"})
	}
	if got := recorder.Header().Get("x-cpa-trace-id"); got != "local-trace" {
		t.Fatalf("x-cpa-trace-id = %q, want local trace", got)
	}
	if got := recorder.Header().Get("Access-Control-Expose-Headers"); got != "x-cpa-trace-id" {
		t.Fatalf("Access-Control-Expose-Headers = %q, want CPA value", got)
	}
}

func TestEnrichAuthSelectionError_DefaultsTo503WithContext(t *testing.T) {
	in := &coreauth.Error{Code: "auth_not_found", Message: "no auth available"}
	out := enrichAuthSelectionError(in, []string{"claude"}, "claude-sonnet-4-6")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if got.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", got.StatusCode(), http.StatusServiceUnavailable)
	}
	if !strings.Contains(got.Message, "providers=claude") {
		t.Fatalf("message missing provider context: %q", got.Message)
	}
	if !strings.Contains(got.Message, "model=claude-sonnet-4-6") {
		t.Fatalf("message missing model context: %q", got.Message)
	}
	if !strings.Contains(got.Message, "/v0/management/auth-files") {
		t.Fatalf("message missing management hint: %q", got.Message)
	}
}

func TestEnrichAuthSelectionError_PreservesExplicitStatus(t *testing.T) {
	in := &coreauth.Error{Code: "auth_unavailable", Message: "no auth available", HTTPStatus: http.StatusTooManyRequests}
	out := enrichAuthSelectionError(in, []string{"gemini"}, "gemini-2.5-pro")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if got.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", got.StatusCode(), http.StatusTooManyRequests)
	}
}

func TestEnrichAuthSelectionError_IgnoresOtherErrors(t *testing.T) {
	in := errors.New("boom")
	out := enrichAuthSelectionError(in, []string{"claude"}, "claude-sonnet-4-6")
	if out != in {
		t.Fatalf("expected original error to be returned unchanged")
	}
}

func TestEnrichAuthSelectionError_IncludesUpstreamErrorFromCause(t *testing.T) {
	in := coreauth.WithCause(&coreauth.Error{
		Code:    "auth_unavailable",
		Message: "no auth available",
	}, errors.New(`{"type":"error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later.","sequence_number":0}`))
	out := enrichAuthSelectionError(in, []string{"codex"}, "gpt-5.6-sol")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if got.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", got.StatusCode(), http.StatusServiceUnavailable)
	}
	if !strings.Contains(got.Message, "providers=codex") {
		t.Fatalf("message missing provider context: %q", got.Message)
	}
	if !strings.Contains(got.Message, "model=gpt-5.6-sol") {
		t.Fatalf("message missing model context: %q", got.Message)
	}
	if !strings.Contains(got.Message, "Our servers are currently overloaded. Please try again later.") && !strings.Contains(got.Message, "server_is_overloaded") {
		t.Fatalf("message missing upstream error details: %q", got.Message)
	}
}

func TestEnrichAuthSelectionError_DoesNotModifyModelCooldownError(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&overloadStreamExecutor{})
	next := time.Now().Add(time.Minute)
	auth := &coreauth.Auth{
		ID:          "auth-cool",
		Provider:    "codex",
		Unavailable: true,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			NextRecoverAt: next,
		},
		LastError: &coreauth.Error{
			Code:    "auth_unavailable",
			Message: "no auth available",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.6-sol"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	_, errPick := manager.Execute(context.Background(), []string{"codex"}, coreexecutor.Request{Model: "gpt-5.6-sol"}, coreexecutor.Options{})
	if errPick == nil {
		t.Fatal("expected pick error for cooling auth")
	}

	out := enrichAuthSelectionError(errPick, []string{"codex"}, "gpt-5.6-sol")
	if out != errPick {
		t.Fatalf("expected modelCooldownError to remain untouched by enrichAuthSelectionError, got %T: %v", out, out)
	}
	if clienterror.HTTPStatusFromError(out) != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", clienterror.HTTPStatusFromError(out))
	}
	if isAuthSelectionUnavailable(errPick) {
		t.Fatal("isAuthSelectionUnavailable(modelCooldownError) = true, want false")
	}
}

func TestExtractUpstreamErrorSummary_SanitizationAndTruncation(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantMask     string
		wantExact    string
		forbiddenRaw string
	}{
		{
			name:         "authorization header with bearer and topsecret redacted",
			input:        "authorization: Bearer abcdef+TOPSECRET==",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "authorization header with custom scheme redacted",
			input:        "authorization: ApiKey SUPERSECRET",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "authorization header with comma separated parameters redacted",
			input:        "Authorization: ApiKey first,SECONDSECRET",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "SECONDSECRET",
		},
		{
			name:         "authorization header with digest parameters redacted",
			input:        `Authorization: Digest username="Mufasa", realm="myrealm", nonce="NONCE", uri="/dir/index.html", response="SIG"`,
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "NONCE",
		},
		{
			name:         "double quoted multi word password redacted",
			input:        `password="correct horse battery staple"`,
			wantMask:     `[REDACTED]`,
			forbiddenRaw: "correct horse battery staple",
		},
		{
			name:         "double quoted comma containing api key redacted",
			input:        `api_key="secret,value"`,
			wantMask:     `[REDACTED]`,
			forbiddenRaw: "secret,value",
		},
		{
			name:         "bearer token redacted",
			input:        "upstream returned error: Bearer abcdef+TOPSECRET==",
			wantMask:     "Bearer [REDACTED]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "sk key redacted",
			input:        "invalid key sk-live-secret-key-123456",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "live-secret-key",
		},
		{
			name:         "user path redacted",
			input:        "open /Users/alice/configs/auth.json: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "kv secret redacted",
			input:        "failed with api_key=secret-value-123 and token: my-secret-token",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "secret-value-123",
		},
		{
			name:         "unstructured invalid api key redacted",
			input:        "invalid API key SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "unstructured invalid access token redacted",
			input:        "invalid access token SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "unstructured socks5 proxy auth redacted",
			input:        "proxyconnect tcp: socks5://alice:PASSSECRET@proxy.internal:1080",
			wantMask:     "[REDACTED_AUTH]",
			forbiddenRaw: "PASSSECRET",
		},
		{
			name:         "unstructured signature url query redacted",
			input:        "request failed: https://example.com?sig=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:     "json code and message extracted",
			input:    `{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`,
			wantMask: "Our servers are currently overloaded. Please try again later.",
		},
		{
			name:     "code prefix with json handled",
			input:    `auth_unavailable: {"error":{"code":"server_is_overloaded","message":"Overloaded"}}`,
			wantMask: "Overloaded",
		},
		{
			name:         "json message containing invalid token secret redacted",
			input:        `{"code":"oops","message":"invalid token SUPERSECRET"}`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "single quoted key kv redacted",
			input:        `'api_key'=SUPERSECRET`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "authorization equals bearer redacted",
			input:        `authorization=Bearer SUPERSECRET`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "json message containing invalid token secret with escaped quotes redacted",
			input:        `{"code":"oops","message":"invalid token \"SUPERSECRET\""}`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "json message containing password with escaped quotes redacted",
			input:        `{"code":"oops","message":"password=\"abc\\\"SUPERSECRET\""}`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "natural language api key redacted",
			input:        "upstream rejected API key: SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "natural language password is redacted",
			input:        "password is SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "mnt unix path redacted",
			input:        "open /mnt/secrets/alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "windows path with spaces redacted",
			input:        `open C:\Users\Alice Smith\secret.txt: permission denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "cookie header redacted",
			input:        "Cookie: sessionid=COOKIESECRET",
			wantMask:     "Cookie: [REDACTED]",
			forbiddenRaw: "COOKIESECRET",
		},
		{
			name:         "set-cookie header redacted",
			input:        "Set-Cookie: session=SETCOOKIESECRET",
			wantMask:     "Cookie: [REDACTED]",
			forbiddenRaw: "SETCOOKIESECRET",
		},
		{
			name:         "private key kv redacted",
			input:        "private_key=PRIVATEKEYSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "PRIVATEKEYSECRET",
		},
		{
			name:         "uri userinfo redacted",
			input:        "https://user:PASSSECRET@example.com/api",
			wantMask:     "https://[REDACTED_AUTH]@",
			forbiddenRaw: "PASSSECRET",
		},
		{
			name:         "workspace unix path redacted",
			input:        "/workspace/tenants/alice/oauth-cache",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "opt unix path redacted",
			input:        `open /opt/cli-proxy/auth/alice.json: failed`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "windows path redacted",
			input:        `open C:\Users\alice\secret.txt: failed`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "incorrect api key provided redacted",
			input:        "Incorrect API key provided: SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "plural credentials redacted",
			input:        "credentials: SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "aws secret access key redacted",
			input:        "AWS_SECRET_ACCESS_KEY=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "protocol relative uri userinfo redacted",
			input:        "//alice:PASSSECRET@example.com/api",
			wantMask:     "[REDACTED_AUTH]",
			forbiddenRaw: "PASSSECRET",
		},
		{
			name:         "run secrets unix path redacted",
			input:        "open /run/secrets/alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "custom tenant unix path redacted",
			input:        "open /custom/tenant/alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "windows unc path redacted",
			input:        `open \\server\share\alice\secret.txt: permission denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "service key kv redacted",
			input:        "SERVICE_KEY=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "openai key kv redacted",
			input:        "OPENAI_KEY=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "x-key query param redacted",
			input:        "https://example.com?x-key=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "unix path with spaces redacted",
			input:        "open /custom/tenant/Alice Smith/secret.txt: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "Smith",
		},
		{
			name:         "unix path with unicode redacted",
			input:        "open /custom/租户/alice: denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "single segment unix path redacted",
			input:        "open /alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "stat single segment unicode path redacted",
			input:        "stat /客户: no such file or directory",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "客户",
		},
		{
			name:         "quoted path with colon and secret redacted",
			input:        `open "/tmp/customer:TOPSECRET/creds": denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "unquoted multi-word password redacted",
			input:        "password = correct horse battery staple",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "battery staple",
		},
		{
			name:         "unquoted multi-word credentials redacted",
			input:        "credentials: alice secret",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "alice secret",
		},
		{
			name:         "quoted path containing password assignment redacted",
			input:        `open "/Users/alice/password=foo/bar": denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "unquoted path with colon and secret redacted",
			input:        "open /tmp/customer:TOPSECRET/creds: denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "unquoted path with colon in leaf component redacted",
			input:        "open /tmp/customer:TOPSECRET: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "multiple unquoted unix paths redacted",
			input:        "rename /Users/alice/source /Users/bob/private-data: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "rename [REDACTED_PATH] [REDACTED_PATH]: denied",
			forbiddenRaw: "bob",
		},
		{
			name:         "non-terminal space path in multiple unix paths redacted",
			input:        "rename /Users/Alice Smith/source /Users/bob/private-data: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "rename [REDACTED_PATH] [REDACTED_PATH]: denied",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "leaf filename with space redacted",
			input:        "open /tmp/Alice Smith.txt: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "multiple paths with leaf filename spaces redacted",
			input:        "rename /tmp/Alice Smith /tmp/Bob Jones: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "rename [REDACTED_PATH] [REDACTED_PATH]: denied",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "unquoted path with colon space in leaf component redacted",
			input:        "open /tmp/customer: TOPSECRET: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "parenthesized unquoted path redacted and parens preserved",
			input:        "open (/tmp/customer): denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open ([REDACTED_PATH]): denied",
			forbiddenRaw: "customer",
		},
		{
			name:         "braced unquoted path redacted and braces preserved",
			input:        "open {/tmp/customer}: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open {[REDACTED_PATH]}: denied",
			forbiddenRaw: "customer",
		},
		{
			name:         "nested error text preserved after path",
			input:        "open /tmp/config: permission denied: retry later",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: permission denied: retry later",
			forbiddenRaw: "config",
		},
		{
			name:         "nested known error text preserved after path",
			input:        "open /tmp/config: permission denied: access denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: permission denied: access denied",
			forbiddenRaw: "config",
		},
		{
			name:         "uppercase connector between paths preserved",
			input:        "copy /tmp/a   TO\t/tmp/b: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "copy [REDACTED_PATH]   TO\t[REDACTED_PATH]: denied",
			forbiddenRaw: "",
		},
		{
			name:         "long connector string bounded to 256 runes",
			input:        strings.Repeat("a", 300) + " to /tmp/x: denied",
			wantMask:     "...",
			forbiddenRaw: "x",
		},
		{
			name:     "long utf-8 text truncated to 256 runes",
			input:    strings.Repeat("你好世界🌟", 80),
			wantMask: "...",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := coreauth.ExtractUpstreamErrorSummary(tc.input)
			if !strings.Contains(got, tc.wantMask) {
				t.Fatalf("ExtractUpstreamErrorSummary(%q) = %q, want containing %q", tc.input, got, tc.wantMask)
			}
			if tc.wantExact != "" && got != tc.wantExact {
				t.Fatalf("ExtractUpstreamErrorSummary(%q) = %q, want exact %q", tc.input, got, tc.wantExact)
			}
			if tc.forbiddenRaw != "" && strings.Contains(got, tc.forbiddenRaw) {
				t.Fatalf("ExtractUpstreamErrorSummary(%q) leaked sensitive value %q in output: %q", tc.input, tc.forbiddenRaw, got)
			}
			if len([]rune(got)) > 256 {
				t.Fatalf("ExtractUpstreamErrorSummary length = %d runes, want <= 256", len([]rune(got)))
			}
		})
	}
}

func TestExecutionErrorMessageMapsContextStatuses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "canceled", err: context.Canceled, want: clienterror.StatusClientClosedRequest},
		{name: "deadline", err: context.DeadlineExceeded, want: http.StatusGatewayTimeout},
		{
			name: "url error wraps canceled",
			err:  &url.Error{Op: "Post", URL: "https://example.com", Err: context.Canceled},
			want: clienterror.StatusClientClosedRequest,
		},
		{name: "plain error defaults to 500", err: errors.New("boom"), want: http.StatusInternalServerError},
		{
			name: "explicit status wins",
			err:  &coreauth.Error{Code: "rate_limited", Message: "slow down", HTTPStatus: http.StatusTooManyRequests},
			want: http.StatusTooManyRequests,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := executionErrorMessage(tc.err)
			if msg == nil {
				t.Fatalf("executionErrorMessage() returned nil")
			}
			if msg.StatusCode != tc.want {
				t.Fatalf("StatusCode = %d, want %d", msg.StatusCode, tc.want)
			}
			if msg.Error != tc.err {
				t.Fatalf("Error = %v, want original %v", msg.Error, tc.err)
			}
		})
	}
}

func TestStatusFromErrorMapsContextStatuses(t *testing.T) {
	if got := statusFromError(context.Canceled); got != clienterror.StatusClientClosedRequest {
		t.Fatalf("statusFromError(canceled) = %d, want %d", got, clienterror.StatusClientClosedRequest)
	}
	if got := statusFromError(context.DeadlineExceeded); got != http.StatusGatewayTimeout {
		t.Fatalf("statusFromError(deadline) = %d, want %d", got, http.StatusGatewayTimeout)
	}
	if got := statusFromError(&url.Error{Op: "Post", URL: "https://example.com", Err: context.Canceled}); got != clienterror.StatusClientClosedRequest {
		t.Fatalf("statusFromError(url canceled) = %d, want %d", got, clienterror.StatusClientClosedRequest)
	}
	if got := statusFromError(errors.New("boom")); got != 0 {
		t.Fatalf("statusFromError(plain) = %d, want 0", got)
	}
}

func TestWriteErrorResponse_ContextCanceledUses499(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, executionErrorMessage(context.Canceled))

	if recorder.Code != clienterror.StatusClientClosedRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, clienterror.StatusClientClosedRequest)
	}
}
