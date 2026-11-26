package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type issue7751NetTimeoutError struct{}

func (issue7751NetTimeoutError) Error() string   { return "dial tcp 10.0.0.1:443: i/o timeout" }
func (issue7751NetTimeoutError) Timeout() bool   { return true }
func (issue7751NetTimeoutError) Temporary() bool { return true }

type issue7751StaticRTProvider struct {
	rt http.RoundTripper
}

func (p issue7751StaticRTProvider) RoundTripperFor(*cliproxyauth.Auth) http.RoundTripper {
	return p.rt
}

func TestMissingAntigravityProjectIDError_NilCauseIsCredentialScopedNon400(t *testing.T) {
	err := missingAntigravityProjectIDError(nil)
	if err.StatusCode() == http.StatusBadRequest {
		t.Fatalf("StatusCode() = %d, must not classify missing project_id as HTTP 400 client fault", err.StatusCode())
	}
	if err.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("StatusCode() = %d, want %d", err.StatusCode(), http.StatusServiceUnavailable)
	}
	if !err.IsCredentialScoped() {
		t.Fatal("IsCredentialScoped() = false, want true for missing project_id")
	}
	if gotCode := err.ErrorCode(); gotCode != "" {
		t.Fatalf("ErrorCode() = %q, want empty (must not be transient_transport or credential_quota)", gotCode)
	}
	resultErr := cliproxyauth.ResultErrorFromError(err)
	if resultErr == nil {
		t.Fatal("ResultErrorFromError() = nil")
	}
	if resultErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("ResultError.HTTPStatus = %d, want %d", resultErr.HTTPStatus, http.StatusServiceUnavailable)
	}
	if resultErr.Code == cliproxyauth.ErrorCodeTransientTransport || resultErr.Code == cliproxyauth.ErrorCodeCredentialQuota || resultErr.Code == cliproxyauth.ErrorCodeRequestScoped {
		t.Fatalf("ResultError.Code = %q, must not be transient_transport, credential_quota, or request_scoped", resultErr.Code)
	}
}

func TestMissingAntigravityProjectIDError_TransportErrorPreservesUnwrapAndTransientClassification(t *testing.T) {
	cause := fmt.Errorf("execute request: %w", issue7751NetTimeoutError{})
	err := missingAntigravityProjectIDError(cause)
	if err.StatusCode() == http.StatusBadRequest {
		t.Fatalf("StatusCode() = %d, transport timeout must not map to HTTP 400", err.StatusCode())
	}
	if err.StatusCode() != 0 {
		t.Fatalf("StatusCode() = %d, want 0 for status-less transport failure", err.StatusCode())
	}
	if err.IsCredentialScoped() {
		t.Fatal("IsCredentialScoped() = true, transport timeout must not be credential-scoped")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || netErr == nil || !netErr.Timeout() {
		t.Fatalf("errors.As(err, &net.Error) failed to unwrap timeout cause: %v", err)
	}
	if gotCode := err.ErrorCode(); gotCode != cliproxyauth.ErrorCodeTransientTransport {
		t.Fatalf("ErrorCode() = %q, want %q", gotCode, cliproxyauth.ErrorCodeTransientTransport)
	}
	resultErr := cliproxyauth.ResultErrorFromError(err)
	if resultErr == nil || resultErr.Code != cliproxyauth.ErrorCodeTransientTransport {
		t.Fatalf("ResultErrorFromError() = %+v, want Code=%q", resultErr, cliproxyauth.ErrorCodeTransientTransport)
	}
}

func TestMissingAntigravityProjectIDError_PreservesCauseTOSViolationAndMetadata(t *testing.T) {
	cause := statusErr{
		code:             http.StatusForbidden,
		msg:              "upstream TOS_VIOLATION",
		credentialScoped: true,
		tosViolation:     true,
		tosAppealURL:     "https://forms.gle/test-appeal-7751",
		tosAppealURLText: "Submit Appeal",
	}
	err := missingAntigravityProjectIDError(cause)
	if err.StatusCode() != http.StatusForbidden {
		t.Fatalf("StatusCode() = %d, want %d", err.StatusCode(), http.StatusForbidden)
	}
	if !err.IsTerminalTOSViolation() {
		t.Fatal("IsTerminalTOSViolation() = false, want true")
	}
	if !err.IsCredentialScoped() {
		t.Fatal("IsCredentialScoped() = false, want true")
	}
	if gotCode := err.ErrorCode(); gotCode != "tos_violation" {
		t.Fatalf("ErrorCode() = %q, want %q", gotCode, "tos_violation")
	}
	meta := err.TOSAppealMetadata()
	if meta["tos_appeal_url"] != "https://forms.gle/test-appeal-7751" || meta["tos_appeal_url_text"] != "Submit Appeal" {
		t.Fatalf("TOSAppealMetadata() = %+v, want appeal URL and text preserved", meta)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(err, cause) = false, want Unwrap() to preserve cause")
	}
}

func TestAntigravityPrepareRequestAuth_Discovery429PreservesRetryDelayAndUnwrap_Issue7751(t *testing.T) {
	const rateLimitBody = `{"error":{"code":429,"message":"Too Many Requests","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"37s"}]}}`
	exec := NewAntigravityExecutor(&config.Config{})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(rateLimitBody)),
		}, nil
	}))

	_, errPrepare := exec.PrepareRequestAuth(ctx, &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "rate-limited-token",
			"expired":      time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		},
	})
	if errPrepare == nil {
		t.Fatal("PrepareRequestAuth() = nil, want 429 error")
	}
	var httpErr *antigravity.HTTPStatusError
	if !errors.As(errPrepare, &httpErr) || httpErr == nil || httpErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("errors.As(errPrepare, &HTTPStatusError) failed: %T (%v)", errPrepare, errPrepare)
	}
	sErr, ok := errPrepare.(statusErr)
	if !ok {
		t.Fatalf("errPrepare type = %T, want statusErr", errPrepare)
	}
	if !sErr.IsCredentialScoped() {
		t.Fatal("IsCredentialScoped() = false, want true for discovery 429")
	}
	if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 37*time.Second {
		t.Fatalf("RetryAfter() = %v, want 37s parsed from Google RPC RetryInfo", sErr.RetryAfter())
	}
}

func TestAntigravityPrepareRequestAuth_EmptyProjectIDFromDiscoveryCoolsAndRotates_Issue7751(t *testing.T) {
	cliproxyauth.SetQuotaCooldownDisabled(false)
	cliproxyauth.SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() {
		cliproxyauth.SetQuotaCooldownDisabled(false)
		cliproxyauth.SetTransientErrorCooldownSeconds(0)
	})

	const model = "gemini-3.1-pro"
	var (
		loadCodeAssistCalls atomic.Int32
		onboardUserCalls    atomic.Int32
		generateCalls       atomic.Int32
	)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
			loadCodeAssistCalls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":{}}`)),
			}, nil
		case strings.HasSuffix(req.URL.Path, ":onboardUser"):
			onboardUserCalls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"done":true,"response":{"cloudaicompanionProject":{}}}`)),
			}, nil
		case strings.Contains(req.URL.Path, "generateContent"):
			generateCalls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`)),
			}, nil
		default:
			t.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL.String())
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("{}")),
			}, nil
		}
	})

	exec := NewAntigravityExecutor(&config.Config{})
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
	manager.SetRoundTripperProvider(issue7751StaticRTProvider{rt: transport})
	manager.RegisterExecutor(exec)

	ctx := cliproxyauth.WithSkipPersist(context.Background())
	suffix := uuid.NewString()
	brokenID := "ag-1-broken-" + suffix
	healthyID := "ag-2-healthy-" + suffix

	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	brokenAuth := &cliproxyauth.Auth{
		ID:       brokenID,
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "broken-token",
			"expired":      future,
		},
	}
	healthyAuth := &cliproxyauth.Auth{
		ID:       healthyID,
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "healthy-token",
			"project_id":   "healthy-project-7751",
			"expired":      future,
		},
	}
	for _, a := range []*cliproxyauth.Auth{brokenAuth, healthyAuth} {
		if _, errReg := manager.Register(ctx, a); errReg != nil {
			t.Fatalf("Register(%s): %v", a.ID, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(brokenID)
		registry.GetGlobalRegistry().UnregisterClient(healthyID)
	})

	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}

	if _, errExec := manager.Execute(ctx, []string{"antigravity"}, req, opts); errExec != nil {
		t.Fatalf("first Execute() error = %v, want rotation to healthy credential", errExec)
	}
	if got := generateCalls.Load(); got != 1 {
		t.Fatalf("generateCalls after first Execute = %d, want 1", got)
	}

	brokenSnap, ok := manager.GetByID(brokenID)
	if !ok || brokenSnap == nil {
		t.Fatal("broken auth missing from manager")
	}
	if cliproxyauth.IsAuthAvailableForModel(brokenSnap, model, time.Now()) {
		t.Fatal("broken auth remained available after empty project_id; want credential cooled down")
	}
	if brokenSnap.LastError == nil {
		t.Fatal("brokenSnap.LastError = nil, want recorded 503 failure")
	}
	if brokenSnap.LastError.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("brokenSnap.LastError.HTTPStatus = %d, want %d", brokenSnap.LastError.HTTPStatus, http.StatusServiceUnavailable)
	}
	if brokenSnap.LastError.Code == cliproxyauth.ErrorCodeCredentialQuota || brokenSnap.LastError.Code == cliproxyauth.ErrorCodeTransientTransport {
		t.Fatalf("brokenSnap.LastError.Code = %q, must not be misclassified as credential_quota or transient_transport", brokenSnap.LastError.Code)
	}

	// Second request must skip the cooled broken credential instead of hammering discovery again.
	if _, errExec := manager.Execute(ctx, []string{"antigravity"}, req, opts); errExec != nil {
		t.Fatalf("second Execute() error = %v, want healthy credential served directly", errExec)
	}
	if got := loadCodeAssistCalls.Load(); got != 1 {
		t.Fatalf("loadCodeAssistCalls = %d, want 1 (broken credential must stay cooled)", got)
	}
	if got := onboardUserCalls.Load(); got != 1 {
		t.Fatalf("onboardUserCalls = %d, want 1 (broken credential must stay cooled)", got)
	}
}

func TestAntigravityPrepareRequestAuth_NetTimeoutPreservesTransientTransportAndRotatesWithoutCooling_Issue7751(t *testing.T) {
	cliproxyauth.SetQuotaCooldownDisabled(false)
	cliproxyauth.SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() {
		cliproxyauth.SetQuotaCooldownDisabled(false)
		cliproxyauth.SetTransientErrorCooldownSeconds(0)
	})

	const model = "gemini-3.1-pro"
	timeoutTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
			return nil, issue7751NetTimeoutError{}
		case strings.Contains(req.URL.Path, "generateContent"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`)),
			}, nil
		default:
			t.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL.String())
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("{}")),
			}, nil
		}
	})

	exec := NewAntigravityExecutor(&config.Config{})
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	// Direct PrepareRequestAuth contract check.
	directCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", timeoutTransport)
	_, errPrepare := exec.PrepareRequestAuth(directCtx, &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "timeout-token",
			"expired":      future,
		},
	})
	if errPrepare == nil {
		t.Fatal("PrepareRequestAuth() = nil, want transport timeout error")
	}
	var netErr net.Error
	if !errors.As(errPrepare, &netErr) || netErr == nil || !netErr.Timeout() {
		t.Fatalf("PrepareRequestAuth() lost net.Error timeout via Unwrap: %T (%v)", errPrepare, errPrepare)
	}
	if sc, ok := errPrepare.(interface{ StatusCode() int }); ok && sc.StatusCode() != 0 {
		t.Fatalf("PrepareRequestAuth() StatusCode() = %d, want 0 for transient transport timeout", sc.StatusCode())
	}
	resultErr := cliproxyauth.ResultErrorFromError(errPrepare)
	if resultErr == nil || resultErr.Code != cliproxyauth.ErrorCodeTransientTransport {
		t.Fatalf("ResultErrorFromError(errPrepare) = %+v, want Code=%q", resultErr, cliproxyauth.ErrorCodeTransientTransport)
	}

	// End-to-end Manager rotation check: rotates to healthy credential without cooling the timed-out credential.
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
	manager.SetRoundTripperProvider(issue7751StaticRTProvider{rt: timeoutTransport})
	manager.RegisterExecutor(exec)

	ctx := cliproxyauth.WithSkipPersist(context.Background())
	suffix := uuid.NewString()
	timeoutID := "ag-1-timeout-" + suffix
	healthyID := "ag-2-healthy-" + suffix

	timeoutAuth := &cliproxyauth.Auth{
		ID:       timeoutID,
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "timeout-token",
			"expired":      future,
		},
	}
	healthyAuth := &cliproxyauth.Auth{
		ID:       healthyID,
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "healthy-token",
			"project_id":   "healthy-project-7751",
			"expired":      future,
		},
	}
	for _, a := range []*cliproxyauth.Auth{timeoutAuth, healthyAuth} {
		if _, errReg := manager.Register(ctx, a); errReg != nil {
			t.Fatalf("Register(%s): %v", a.ID, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(timeoutID)
		registry.GetGlobalRegistry().UnregisterClient(healthyID)
	})

	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}
	if _, errExec := manager.Execute(ctx, []string{"antigravity"}, req, opts); errExec != nil {
		t.Fatalf("Execute() error = %v, want rotation to healthy credential", errExec)
	}

	timeoutSnap, ok := manager.GetByID(timeoutID)
	if !ok || timeoutSnap == nil {
		t.Fatal("timeout auth missing from manager")
	}
	if !cliproxyauth.IsAuthAvailableForModel(timeoutSnap, model, time.Now()) {
		t.Fatalf("timeout auth was cooled down on transient transport error; nextRetry=%v lastErr=%+v", timeoutSnap.NextRetryAfter, timeoutSnap.LastError)
	}
}

func TestAntigravityPrepareRequestAuth_DiscoveryTOSViolationDisablesCredentialAndPersistsAppeal_Issue7751(t *testing.T) {
	// Include a message >200 bytes before details so onboardUser's 200-char preview truncation
	// would drop TOS_VIOLATION and appeal_url unless the raw response body is preserved.
	const tosBodyLong = `{"error":{"code":403,"message":"This account has been suspended for violating the Terms of Service across multiple Google Cloud Code Assist endpoints; please review the enforcement notice carefully and submit an appeal through the provided form below.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudaicompanion.googleapis.com","metadata":{"appeal_url":"https://forms.gle/hGzM9MEUv2azZsrb9","appeal_url_link_text":"Submit Appeal"}}]}}`
	const tosBodyLRO = `{"done":true,"error":{"code":7,"message":"Violation of Terms of Service","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudaicompanion.googleapis.com","metadata":{"appeal_url":"https://forms.gle/hGzM9MEUv2azZsrb9","appeal_url_link_text":"Submit Appeal"}}]}}`

	cases := []struct {
		name      string
		transport http.RoundTripper
	}{
		{
			name: "loadCodeAssist",
			transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
					return &http.Response{
						StatusCode: http.StatusForbidden,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(tosBodyLong)),
					}, nil
				case strings.Contains(req.URL.Path, "generateContent"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`)),
					}, nil
				default:
					return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
				}
			}),
		},
		{
			name: "onboardUser",
			transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`)),
					}, nil
				case strings.HasSuffix(req.URL.Path, ":onboardUser"):
					return &http.Response{
						StatusCode: http.StatusForbidden,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(tosBodyLong)),
					}, nil
				case strings.Contains(req.URL.Path, "generateContent"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`)),
					}, nil
				default:
					return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
				}
			}),
		},
		{
			name: "onboardUser_lro_done_error",
			transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`)),
					}, nil
				case strings.HasSuffix(req.URL.Path, ":onboardUser"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(tosBodyLRO)),
					}, nil
				case strings.Contains(req.URL.Path, "generateContent"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`)),
					}, nil
				default:
					return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
				}
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			exec := NewAntigravityExecutor(&config.Config{})
			future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
			directCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", tc.transport)

			_, errPrepare := exec.PrepareRequestAuth(directCtx, &cliproxyauth.Auth{
				ID: "tos-prepare-direct",
				Metadata: map[string]any{
					"access_token": "tos-token",
					"expired":      future,
				},
			})
			if errPrepare == nil {
				t.Fatal("PrepareRequestAuth() = nil, want 403 TOS_VIOLATION error")
			}
			var httpErr *antigravity.HTTPStatusError
			if !errors.As(errPrepare, &httpErr) || httpErr == nil || httpErr.StatusCode() != http.StatusForbidden {
				t.Fatalf("errors.As(errPrepare, &HTTPStatusError) failed: %T (%v)", errPrepare, errPrepare)
			}
			sErr, ok := errPrepare.(statusErr)
			if !ok {
				t.Fatalf("errPrepare type = %T, want statusErr", errPrepare)
			}
			if !sErr.IsTerminalTOSViolation() {
				t.Fatalf("IsTerminalTOSViolation() = false, want true (err=%v)", errPrepare)
			}
			if !sErr.IsCredentialScoped() {
				t.Fatal("IsCredentialScoped() = false, want true")
			}
			if sErr.ErrorCode() != "tos_violation" {
				t.Fatalf("ErrorCode() = %q, want tos_violation", sErr.ErrorCode())
			}
			meta := sErr.TOSAppealMetadata()
			if meta["tos_appeal_url"] != "https://forms.gle/hGzM9MEUv2azZsrb9" || meta["tos_appeal_url_text"] != "Submit Appeal" {
				t.Fatalf("TOSAppealMetadata() = %+v, want appeal URL and text", meta)
			}

			// Verify Manager deactivates the TOS-violating credential and persists appeal metadata.
			const model = "gemini-3.1-pro"
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
			manager.SetRoundTripperProvider(issue7751StaticRTProvider{rt: tc.transport})
			manager.RegisterExecutor(exec)

			ctx := cliproxyauth.WithSkipPersist(context.Background())
			suffix := uuid.NewString()
			tosID := "ag-1-tos-" + suffix
			healthyID := "ag-2-healthy-" + suffix

			tosAuth := &cliproxyauth.Auth{
				ID:       tosID,
				Provider: "antigravity",
				Status:   cliproxyauth.StatusActive,
				Metadata: map[string]any{
					"type":         "antigravity",
					"access_token": "tos-token",
					"expired":      future,
				},
			}
			healthyAuth := &cliproxyauth.Auth{
				ID:       healthyID,
				Provider: "antigravity",
				Status:   cliproxyauth.StatusActive,
				Metadata: map[string]any{
					"type":         "antigravity",
					"access_token": "healthy-token",
					"project_id":   "healthy-project-7751",
					"expired":      future,
				},
			}
			for _, a := range []*cliproxyauth.Auth{tosAuth, healthyAuth} {
				if _, errReg := manager.Register(ctx, a); errReg != nil {
					t.Fatalf("Register(%s): %v", a.ID, errReg)
				}
				registry.GetGlobalRegistry().RegisterClient(a.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
			}
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(tosID)
				registry.GetGlobalRegistry().UnregisterClient(healthyID)
			})

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}
			if _, errExec := manager.Execute(ctx, []string{"antigravity"}, req, opts); errExec != nil {
				t.Fatalf("Execute() error = %v, want rotation to healthy credential after deactivating TOS credential", errExec)
			}

			tosSnap, ok := manager.GetByID(tosID)
			if !ok || tosSnap == nil {
				t.Fatal("tos auth missing from manager")
			}
			if tosSnap.Status != cliproxyauth.StatusDisabled || !tosSnap.Disabled {
				t.Fatalf("tosSnap status=%q disabled=%v, want StatusDisabled and Disabled=true", tosSnap.Status, tosSnap.Disabled)
			}
			if tosSnap.StatusMessage != "tos_violation" {
				t.Fatalf("tosSnap.StatusMessage = %q, want tos_violation", tosSnap.StatusMessage)
			}
			if gotURL, _ := tosSnap.Metadata["tos_appeal_url"].(string); gotURL != "https://forms.gle/hGzM9MEUv2azZsrb9" {
				t.Fatalf("tosSnap.Metadata[tos_appeal_url] = %q, want appeal URL persisted", gotURL)
			}
			if gotText, _ := tosSnap.Metadata["tos_appeal_url_text"].(string); gotText != "Submit Appeal" {
				t.Fatalf("tosSnap.Metadata[tos_appeal_url_text] = %q, want Submit Appeal persisted", gotText)
			}
		})
	}
}

func TestAntigravityPrepareRequestAuth_ExpiredTokenSingleDiscoveryPreservesTOSAnd429_Issue7751(t *testing.T) {
	t.Run("expired token first discovery 403 TOS_VIOLATION is not dropped by duplicate discovery", func(t *testing.T) {
		const tosBody = `{"error":{"code":403,"message":"Violation of Terms of Service","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudcode-pa.googleapis.com","metadata":{"appeal_url":"https://forms.gle/expiredTokenAppeal","appeal_url_link_text":"Appeal Now"}}]}}`
		var loadCalls atomic.Int32
		var tokenCalls atomic.Int32
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				tokenCalls.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"refreshed-access-tos","expires_in":3600}`)),
				}, nil
			case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				call := loadCalls.Add(1)
				if call == 1 {
					return &http.Response{
						StatusCode: http.StatusForbidden,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(tosBody)),
					}, nil
				}
				return nil, issue7751NetTimeoutError{}
			default:
				t.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL.String())
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("{}")),
				}, nil
			}
		})

		exec := NewAntigravityExecutor(&config.Config{})
		expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		auth := &cliproxyauth.Auth{
			ID:       "ag-expired-tos-" + uuid.NewString(),
			Provider: "antigravity",
			Metadata: map[string]any{
				"access_token":  "stale-token",
				"refresh_token": t.Name() + "-refresh-" + uuid.NewString(),
				"expired":       expired,
			},
		}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		_, errPrepare := exec.PrepareRequestAuth(ctx, auth)
		if errPrepare == nil {
			t.Fatal("PrepareRequestAuth() = nil, want 403 TOS_VIOLATION error")
		}
		if got := tokenCalls.Load(); got != 1 {
			t.Fatalf("oauth token refresh calls = %d, want 1", got)
		}
		if got := loadCalls.Load(); got != 1 {
			t.Fatalf("loadCodeAssist calls = %d, want 1 (single discovery)", got)
		}
		var sErr statusErr
		if !errors.As(errPrepare, &sErr) {
			t.Fatalf("expected statusErr, got %T (%v)", errPrepare, errPrepare)
		}
		if sErr.StatusCode() != http.StatusForbidden || !sErr.IsTerminalTOSViolation() {
			t.Fatalf("status=%d IsTerminalTOSViolation=%v, want 403 and true", sErr.StatusCode(), sErr.IsTerminalTOSViolation())
		}
		meta := sErr.TOSAppealMetadata()
		if meta["tos_appeal_url"] != "https://forms.gle/expiredTokenAppeal" || meta["tos_appeal_url_text"] != "Appeal Now" {
			t.Fatalf("TOSAppealMetadata() = %+v, want appeal URL and text", meta)
		}
	})

	t.Run("expired token first discovery 429 Retry-After is not dropped by duplicate discovery", func(t *testing.T) {
		var loadCalls atomic.Int32
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"refreshed-access-429","expires_in":3600}`)),
				}, nil
			case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				call := loadCalls.Add(1)
				if call == 1 {
					return &http.Response{
						StatusCode: http.StatusTooManyRequests,
						Header:     http.Header{"Retry-After": {"45"}},
						Body:       io.NopCloser(strings.NewReader(`{"error":{"code":429,"message":"rate limited"}}`)),
					}, nil
				}
				return nil, issue7751NetTimeoutError{}
			default:
				t.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL.String())
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("{}")),
				}, nil
			}
		})

		exec := NewAntigravityExecutor(&config.Config{})
		expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		auth := &cliproxyauth.Auth{
			ID:       "ag-expired-429-" + uuid.NewString(),
			Provider: "antigravity",
			Metadata: map[string]any{
				"access_token":  "stale-token",
				"refresh_token": t.Name() + "-refresh-" + uuid.NewString(),
				"expired":       expired,
			},
		}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		_, errPrepare := exec.PrepareRequestAuth(ctx, auth)
		if errPrepare == nil {
			t.Fatal("PrepareRequestAuth() = nil, want 429 error")
		}
		if got := loadCalls.Load(); got != 1 {
			t.Fatalf("loadCodeAssist calls = %d, want 1 (single discovery)", got)
		}
		var sErr statusErr
		if !errors.As(errPrepare, &sErr) {
			t.Fatalf("expected statusErr, got %T (%v)", errPrepare, errPrepare)
		}
		if sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("StatusCode() = %d, want 429", sErr.StatusCode())
		}
		if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 45*time.Second {
			t.Fatalf("RetryAfter() = %v, want 45s", sErr.RetryAfter())
		}
		if !sErr.IsCredentialScoped() {
			t.Fatal("IsCredentialScoped() = false, want true for upstream 429 during project discovery")
		}
	})
}

func TestAntigravityPrepareRequestAuth_IncompleteOnboardingPollingIsTransientNotCredentialFault_Issue7751(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`)),
				}, nil
			case strings.HasSuffix(req.URL.Path, ":onboardUser"):
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"done":false}`)),
				}, nil
			default:
				t.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL.String())
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("{}")),
				}, nil
			}
		})

		exec := NewAntigravityExecutor(&config.Config{})
		future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		_, errPrepare := exec.PrepareRequestAuth(ctx, &cliproxyauth.Auth{
			Metadata: map[string]any{
				"access_token": "onboarding-in-progress-token",
				"expired":      future,
			},
		})
		if errPrepare == nil {
			t.Fatal("PrepareRequestAuth() = nil, want incomplete onboarding error")
		}
		var sErr statusErr
		if !errors.As(errPrepare, &sErr) {
			t.Fatalf("expected statusErr, got %T (%v)", errPrepare, errPrepare)
		}
		if sErr.StatusCode() == http.StatusBadRequest {
			t.Fatalf("StatusCode() = 400, must not classify incomplete onboarding as client fault")
		}
		if sErr.IsCredentialScoped() {
			t.Fatalf("IsCredentialScoped() = true, want false so credential is not cooled as missing project_id when onboarding did not complete")
		}
		if sErr.ErrorCode() != cliproxyauth.ErrorCodeTransientTransport {
			t.Fatalf("ErrorCode() = %q, want %q", sErr.ErrorCode(), cliproxyauth.ErrorCodeTransientTransport)
		}
		resultErr := cliproxyauth.ResultErrorFromError(errPrepare)
		if resultErr == nil || resultErr.Code != cliproxyauth.ErrorCodeTransientTransport {
			t.Fatalf("ResultErrorFromError(errPrepare) = %+v, want Code=%q", resultErr, cliproxyauth.ErrorCodeTransientTransport)
		}
	})
}

func TestAntigravityPrepareRequestAuth_TransientDiscoveryFailuresTriggerRequestRetryRound_Issue7751(t *testing.T) {
	const model = "gemini-3.1-pro"

	cases := []struct {
		name            string
		wantErrSubstr   string
		firstRoundReply func(req *http.Request) (*http.Response, error)
	}{
		{
			name:          "incomplete onboardUser polling retries in next request-retry round",
			wantErrSubstr: "did not complete",
			firstRoundReply: func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`)),
					}, nil
				case strings.HasSuffix(req.URL.Path, ":onboardUser"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"done":false}`)),
					}, nil
				default:
					return nil, fmt.Errorf("unexpected URL in round 1: %s", req.URL.String())
				}
			},
		},
		{
			name:          "internal discovery acquisition deadline retries in next request-retry round",
			wantErrSubstr: "context deadline exceeded",
			firstRoundReply: func(req *http.Request) (*http.Response, error) {
				<-req.Context().Done()
				return nil, req.Context().Err()
			},
		},
		{
			name:          "completed onboardUser LRO error preserves reason and retries in next request-retry round",
			wantErrSubstr: "UNAVAILABLE",
			firstRoundReply: func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`)),
					}, nil
				case strings.HasSuffix(req.URL.Path, ":onboardUser"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"done":true,"error":{"code":14,"message":"UNAVAILABLE"}}`)),
					}, nil
				default:
					return nil, fmt.Errorf("unexpected URL in round 1: %s", req.URL.String())
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exec := NewAntigravityExecutor(&config.Config{})
				future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

				// Direct PrepareRequestAuth check on the failing round-1 reply.
				directCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(tc.firstRoundReply))
				_, errDirect := exec.PrepareRequestAuth(directCtx, &cliproxyauth.Auth{
					Metadata: map[string]any{
						"access_token": "retry-round-token",
						"expired":      future,
					},
				})
				if errDirect == nil {
					t.Fatal("direct PrepareRequestAuth() = nil, want error")
				}
				if !strings.Contains(errDirect.Error(), tc.wantErrSubstr) {
					t.Fatalf("direct PrepareRequestAuth() err = %q, want substring %q", errDirect.Error(), tc.wantErrSubstr)
				}
				var sErr statusErr
				if !errors.As(errDirect, &sErr) {
					t.Fatalf("expected statusErr, got %T (%v)", errDirect, errDirect)
				}
				if sErr.IsCredentialScoped() {
					t.Fatalf("IsCredentialScoped() = true, want false")
				}
				if sErr.ErrorCode() != cliproxyauth.ErrorCodeTransientTransport {
					t.Fatalf("ErrorCode() = %q, want %q", sErr.ErrorCode(), cliproxyauth.ErrorCodeTransientTransport)
				}

				// End-to-end Manager test with request-retry = 1 and a single credential:
				// round 1 fails, does not cool the credential, and round 2 succeeds.
				var loadCalls atomic.Int32
				var generateCalls atomic.Int32
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					switch {
					case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
						if loadCalls.Add(1) == 1 {
							return tc.firstRoundReply(req)
						}
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": {"application/json"}},
							Body:       io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"recovered-project-7751"}`)),
						}, nil
					case strings.HasSuffix(req.URL.Path, ":onboardUser"):
						return tc.firstRoundReply(req)
					case strings.Contains(req.URL.Path, "generateContent"):
						generateCalls.Add(1)
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": {"application/json"}},
							Body:       io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`)),
						}, nil
					default:
						t.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL.String())
						return &http.Response{
							StatusCode: http.StatusNotFound,
							Header:     make(http.Header),
							Body:       io.NopCloser(strings.NewReader("{}")),
						}, nil
					}
				})

				manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(1, 0, 0)
				manager.SetRoundTripperProvider(issue7751StaticRTProvider{rt: transport})
				manager.RegisterExecutor(exec)

				ctx := cliproxyauth.WithSkipPersist(context.Background())
				authID := "ag-retry-" + uuid.NewString()
				a := &cliproxyauth.Auth{
					ID:       authID,
					Provider: "antigravity",
					Status:   cliproxyauth.StatusActive,
					Metadata: map[string]any{
						"type":         "antigravity",
						"access_token": "retry-round-token",
						"expired":      future,
					},
				}
				if _, errReg := manager.Register(ctx, a); errReg != nil {
					t.Fatalf("Register(%s): %v", a.ID, errReg)
				}
				registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(authID)
				})

				req := cliproxyexecutor.Request{
					Model:   model,
					Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
				}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}
				if _, errExec := manager.Execute(ctx, []string{"antigravity"}, req, opts); errExec != nil {
					t.Fatalf("Execute() error = %v, want second request-retry round to succeed", errExec)
				}
				if got := loadCalls.Load(); got != 2 {
					t.Fatalf("loadCodeAssist calls = %d, want 2 (initial round + retry round)", got)
				}
				if got := generateCalls.Load(); got != 1 {
					t.Fatalf("generateContent calls = %d, want 1", got)
				}
			})
		})
	}
}
