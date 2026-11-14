package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type staticEnvelopePluginClient struct {
	raw []byte
}

func TestPluginErrorCarriesRetryAfterIntoCooldownAndHTTPHeader(t *testing.T) {
	_, errDecode := decodeEnvelopeResult[rpcEmptyResponse](pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:              "rate_limited",
			Message:           "try later",
			HTTPStatus:        http.StatusTooManyRequests,
			RetryAfterSeconds: 7,
		},
	})
	if errDecode == nil {
		t.Fatal("decodeEnvelopeResult returned nil error")
	}
	retryAfter, ok := errDecode.(interface{ RetryAfter() *time.Duration })
	if !ok || retryAfter.RetryAfter() == nil || *retryAfter.RetryAfter() != 7*time.Second {
		t.Fatalf("RetryAfter() = %#v, want 7s", retryAfter)
	}
	if got := coreauth.SafeResponseHeaders(errDecode).Get("Retry-After"); got != "7" {
		t.Fatalf("SafeResponseHeaders Retry-After = %q, want 7", got)
	}

	const (
		provider = "plugin-provider-retry-after"
		model    = "test-model-retry-after"
		authID   = "auth-retry-after-1"
	)
	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-retry-after"})
	host := newHostWithRecords(executorRecord)
	exec := &fakeExecutor{
		identifier: provider,
		execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{}, errDecode
		},
	}
	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = provider

	authMgr := coreauth.NewManager(nil, nil, nil)
	authMgr.RegisterExecutor(adapter)
	auth := &coreauth.Auth{
		ID:         authID,
		Provider:   provider,
		Status:     coreauth.StatusActive,
		FileName:   "auth-retry-after-1.json",
		Attributes: map[string]string{"type": "api_key"},
	}
	if _, errReg := authMgr.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	before := time.Now()
	_, errExec := authMgr.Execute(context.Background(), []string{provider}, coreexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"test-model-retry-after"}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExec == nil {
		t.Fatal("authMgr.Execute returned nil error")
	}
	if got := coreauth.SafeResponseHeaders(errExec).Get("Retry-After"); got != "7" {
		t.Fatalf("SafeResponseHeaders(errExec) Retry-After = %q, want 7", got)
	}
	updated, okAuth := authMgr.GetByID(authID)
	if !okAuth || updated == nil {
		t.Fatalf("GetByID(%q) missing", authID)
	}
	state := updated.ModelStates[model]
	if state == nil || !state.NextRetryAfter.After(before.Add(5*time.Second)) {
		t.Fatalf("model NextRetryAfter = %v, want at least %v (7s cooldown)", state, before.Add(7*time.Second))
	}
}

func TestPluginErrorRetryAfterDoesNotOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	_, errDecode := decodeEnvelopeResult[rpcEmptyResponse](pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:              "rate_limited",
			Message:           "try later",
			HTTPStatus:        http.StatusTooManyRequests,
			RetryAfterSeconds: maxInt,
		},
	})
	if errDecode == nil {
		t.Fatal("decodeEnvelopeResult returned nil error")
	}
	retryAfter, ok := errDecode.(interface{ RetryAfter() *time.Duration })
	maxDurationSeconds := int64(1<<63-1) / int64(time.Second)
	wantSeconds := int64(maxInt)
	if wantSeconds > maxDurationSeconds {
		wantSeconds = maxDurationSeconds
	}
	wantRetryAfter := time.Duration(wantSeconds) * time.Second
	if !ok || retryAfter.RetryAfter() == nil || *retryAfter.RetryAfter() != wantRetryAfter {
		t.Fatalf("RetryAfter() = %#v, want %s", retryAfter, wantRetryAfter)
	}
	wantHeader := strconv.FormatInt(wantSeconds, 10)
	if got := coreauth.SafeResponseHeaders(errDecode).Get("Retry-After"); got != wantHeader {
		t.Fatalf("SafeResponseHeaders Retry-After = %q, want %q", got, wantHeader)
	}
}

func (c staticEnvelopePluginClient) Call(context.Context, string, []byte) ([]byte, error) {
	return c.raw, nil
}

func (c staticEnvelopePluginClient) Shutdown() {}

func TestDecodeEnvelopeResultPreservesPluginHTTPStatus(t *testing.T) {
	_, errDecode := decodeEnvelopeResult[rpcEmptyResponse](pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:       "plugin_error",
			Message:    "license required",
			HTTPStatus: http.StatusForbidden,
		},
	})
	if errDecode == nil {
		t.Fatal("decodeEnvelopeResult returned nil error")
	}
	if got := errDecode.Error(); got != "license required" {
		t.Fatalf("error = %q, want license required", got)
	}
	statusProvider, ok := errDecode.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose StatusCode", errDecode)
	}
	if got := statusProvider.StatusCode(); got != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", got, http.StatusForbidden)
	}
}

func TestCallPluginReturnsPluginErrorWithoutMethodWrapper(t *testing.T) {
	raw, errMarshal := json.Marshal(pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:       "plugin_error",
			Message:    "license required",
			HTTPStatus: http.StatusForbidden,
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal envelope: %v", errMarshal)
	}
	_, errCall := callPlugin[rpcEmptyResponse](context.Background(), staticEnvelopePluginClient{raw: raw}, pluginabi.MethodExecutorExecuteStream, rpcEmptyResponse{})
	if errCall == nil {
		t.Fatal("callPlugin returned nil error")
	}
	if got := errCall.Error(); got != "license required" {
		t.Fatalf("error = %q, want license required", got)
	}
	statusProvider, ok := errCall.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose StatusCode", errCall)
	}
	if got := statusProvider.StatusCode(); got != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", got, http.StatusForbidden)
	}
}

func TestIsPluginErrorEnvelopeAcceptsNonzeroReturnEnvelope(t *testing.T) {
	raw := marshalRPCError("plugin_error", "upstream failed")
	if !isPluginErrorEnvelope(raw) {
		t.Fatalf("isPluginErrorEnvelope(%s) = false, want true", raw)
	}
	if isPluginErrorEnvelope([]byte(`not json`)) {
		t.Fatal("isPluginErrorEnvelope accepted invalid JSON")
	}
}

func TestCallPluginPreservesStatusFromNewErrorEnvelope(t *testing.T) {
	raw, errMarshal := pluginabi.NewErrorEnvelope("insufficient_quota", "plan limit reached", http.StatusForbidden)
	if errMarshal != nil {
		t.Fatalf("NewErrorEnvelope() error = %v", errMarshal)
	}
	_, errCall := callPlugin[rpcEmptyResponse](context.Background(), staticEnvelopePluginClient{raw: raw}, pluginabi.MethodExecutorExecute, rpcEmptyResponse{})
	if errCall == nil {
		t.Fatal("callPlugin returned nil error")
	}
	if got := errCall.Error(); got != "plan limit reached" {
		t.Fatalf("error = %q, want plan limit reached", got)
	}
	statusProvider, ok := errCall.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose StatusCode", errCall)
	}
	if got := statusProvider.StatusCode(); got != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", got, http.StatusForbidden)
	}
}

func TestMarshalRPCErrorPreservesHTTPStatus(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		raw := marshalRPCError("host_call_failed", "synthetic", status)
		var env pluginabi.Envelope
		if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
			t.Fatalf("unmarshal envelope: %v", errUnmarshal)
		}
		if env.OK {
			t.Fatal("expected envelope OK=false")
		}
		if env.Error == nil {
			t.Fatal("expected non-nil Error in envelope")
		}
		if env.Error.HTTPStatus != status {
			t.Fatalf("HTTPStatus = %d, want %d", env.Error.HTTPStatus, status)
		}
		_, errDecode := decodeEnvelopeResult[rpcEmptyResponse](env)
		if errDecode == nil {
			t.Fatal("expected decode error")
		}
		statusProvider, ok := errDecode.(interface{ StatusCode() int })
		if !ok {
			t.Fatalf("decoded error does not expose StatusCode: %T", errDecode)
		}
		if got := statusProvider.StatusCode(); got != status {
			t.Fatalf("StatusCode = %d, want %d", got, status)
		}
	}
}

func TestCallPluginSchedulerPickCompatibility(t *testing.T) {
	tests := []struct {
		name         string
		resultJSON   string
		wantAuthID   string
		wantDelegate string
		wantHandled  bool
		wantReject   bool
		wantCode     string
		wantReason   string
		wantExclude  []string
	}{
		{
			name:         "legacy pascal case auth id",
			resultJSON:   `{"AuthID":"auth-1","DelegateBuiltin":"","Handled":true}`,
			wantAuthID:   "auth-1",
			wantDelegate: "",
			wantHandled:  true,
			wantReject:   false,
		},
		{
			name:         "legacy pascal case delegate",
			resultJSON:   `{"AuthID":"","DelegateBuiltin":"round-robin","Handled":true}`,
			wantAuthID:   "",
			wantDelegate: "round-robin",
			wantHandled:  true,
			wantReject:   false,
		},
		{
			name:         "snake case auth id",
			resultJSON:   `{"auth_id":"auth-2","delegate_builtin":"","handled":true}`,
			wantAuthID:   "auth-2",
			wantDelegate: "",
			wantHandled:  true,
			wantReject:   false,
		},
		{
			name:         "snake case delegate",
			resultJSON:   `{"auth_id":"","delegate_builtin":"fill-first","handled":true}`,
			wantAuthID:   "",
			wantDelegate: "fill-first",
			wantHandled:  true,
			wantReject:   false,
		},
		{
			name:        "snake case terminal rejection",
			resultJSON:  `{"handled":true,"reject":true,"reject_code":"quota_exceeded","reject_reason":"quota exhausted"}`,
			wantHandled: true,
			wantReject:  true,
			wantCode:    "quota_exceeded",
			wantReason:  "quota exhausted",
		},
		{
			name:        "pascal case exclude candidates unhandled",
			resultJSON:  `{"Handled":false,"ExcludeCandidates":["ag-1","ag-2"]}`,
			wantHandled: false,
			wantExclude: []string{"ag-1", "ag-2"},
		},
		{
			name:         "snake case exclude candidates with delegate",
			resultJSON:   `{"handled":true,"delegate_builtin":"round-robin","exclude_candidates":["ag-1","ag-3"]}`,
			wantHandled:  true,
			wantDelegate: "round-robin",
			wantExclude:  []string{"ag-1", "ag-3"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := pluginabi.Envelope{
				OK:     true,
				Result: json.RawMessage(tc.resultJSON),
			}
			raw, errMarshal := json.Marshal(env)
			if errMarshal != nil {
				t.Fatalf("marshal envelope: %v", errMarshal)
			}

			client := staticEnvelopePluginClient{raw: raw}
			resp, errCall := callPlugin[pluginapi.SchedulerPickResponse](context.Background(), client, pluginabi.MethodSchedulerPick, pluginapi.SchedulerPickRequest{})
			if errCall != nil {
				t.Fatalf("callPlugin() error = %v", errCall)
			}
			if resp.AuthID != tc.wantAuthID {
				t.Fatalf("AuthID = %q, want %q", resp.AuthID, tc.wantAuthID)
			}
			if resp.DelegateBuiltin != tc.wantDelegate {
				t.Fatalf("DelegateBuiltin = %q, want %q", resp.DelegateBuiltin, tc.wantDelegate)
			}
			if resp.Handled != tc.wantHandled {
				t.Fatalf("Handled = %v, want %v", resp.Handled, tc.wantHandled)
			}
			if resp.Reject != tc.wantReject {
				t.Fatalf("Reject = %v, want %v", resp.Reject, tc.wantReject)
			}
			if resp.RejectCode != tc.wantCode {
				t.Fatalf("RejectCode = %q, want %q", resp.RejectCode, tc.wantCode)
			}
			if resp.RejectReason != tc.wantReason {
				t.Fatalf("RejectReason = %q, want %q", resp.RejectReason, tc.wantReason)
			}
			if !slices.Equal(resp.ExcludeCandidates, tc.wantExclude) {
				t.Fatalf("ExcludeCandidates = %v, want %v", resp.ExcludeCandidates, tc.wantExclude)
			}
		})
	}
}

func TestStreamCallbacksCarryStructuredPluginErrors(t *testing.T) {
	type statusError interface{ StatusCode() int }
	type retryAfterError interface{ RetryAfter() *time.Duration }

	detail := `{"code":"rate_limited","message":"slow down","http_status":429,"retry_after_seconds":7}`
	for _, tc := range []struct {
		name string
		send func(host *Host, streamID string) error
	}{
		{"emit", func(host *Host, streamID string) error {
			_, err := host.callHostStreamEmit(context.Background(), []byte(`{"stream_id":"`+streamID+`","error_detail":`+detail+`}`))
			return err
		}},
		{"close", func(host *Host, streamID string) error {
			_, err := host.callHostStreamClose([]byte(`{"stream_id":"` + streamID + `","error_detail":` + detail + `}`))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := New()
			streamID, chunks, cleanup := host.streams.open(context.Background())
			t.Cleanup(cleanup)
			if err := tc.send(host, streamID); err != nil {
				t.Fatalf("stream %s callback error = %v", tc.name, err)
			}
			chunk := <-chunks
			if chunk.Err == nil {
				t.Fatal("stream error was dropped")
			}
			var withStatus statusError
			if !errors.As(chunk.Err, &withStatus) || withStatus.StatusCode() != 429 {
				t.Fatalf("stream error status = %v, want 429 (%v)", withStatus, chunk.Err)
			}
			var withRetry retryAfterError
			if !errors.As(chunk.Err, &withRetry) || withRetry.RetryAfter() == nil || *withRetry.RetryAfter() != 7*time.Second {
				t.Fatalf("stream error retry-after = %v, want 7s", withRetry)
			}
			if got := coreauth.SafeResponseHeaders(chunk.Err).Get("Retry-After"); got != "7" {
				t.Fatalf("Retry-After header = %q, want 7", got)
			}
		})
	}
}

func TestStreamCallbacksKeepPlainErrorStrings(t *testing.T) {
	host := New()
	streamID, chunks, cleanup := host.streams.open(context.Background())
	t.Cleanup(cleanup)
	if _, err := host.callHostStreamClose([]byte(`{"stream_id":"` + streamID + `","error":"boom"}`)); err != nil {
		t.Fatalf("stream close callback error = %v", err)
	}
	chunk := <-chunks
	if chunk.Err == nil || chunk.Err.Error() != "boom" {
		t.Fatalf("plain stream error = %v, want boom", chunk.Err)
	}
}

func TestPluginCountTokensNotImplementedDoesNotTriggerCooldown(t *testing.T) {
	coreauth.SetQuotaCooldownDisabled(false)

	_, errDecode := decodeEnvelopeResult[pluginapi.ExecutorResponse](pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:       "not_implemented",
			Message:    "count-tokens/http-request are not implemented yet",
			HTTPStatus: http.StatusNotImplemented,
		},
	})
	if errDecode == nil {
		t.Fatal("decodeEnvelopeResult returned nil error")
	}

	const (
		provider = "kiro-plugin-count-tokens-501"
		model    = "claude-opus-5.5"
		authID   = "kiro-auth-count-tokens-501"
	)
	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "kiro-plugin-count-tokens-501"})
	host := newHostWithRecords(executorRecord)
	exec := &fakeExecutor{
		identifier: provider,
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{}, errDecode
		},
		execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"ok"}]}`)}, nil
		},
	}
	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatClaude},
		[]sdktranslator.Format{sdktranslator.FormatClaude},
	)
	adapter.provider = provider

	authMgr := coreauth.NewManager(nil, nil, nil)
	authMgr.RegisterExecutor(adapter)
	auth := &coreauth.Auth{
		ID:         authID,
		Provider:   provider,
		Status:     coreauth.StatusActive,
		FileName:   "kiro-auth-count-tokens-501.json",
		Attributes: map[string]string{"type": "oauth"},
	}
	if _, errReg := authMgr.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	req := coreexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"claude-opus-5.5","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	}

	for attempt := 1; attempt <= 2; attempt++ {
		_, errCount := authMgr.ExecuteCount(context.Background(), []string{provider}, req, opts)
		if errCount == nil {
			t.Fatalf("attempt %d: ExecuteCount returned nil error, want 501", attempt)
		}
		var sc interface{ StatusCode() int }
		if !errors.As(errCount, &sc) || sc.StatusCode() != http.StatusNotImplemented {
			t.Fatalf("attempt %d: ExecuteCount status = %v, want 501 (%v)", attempt, sc, errCount)
		}
	}

	updated, okAuth := authMgr.GetByID(authID)
	if !okAuth || updated == nil {
		t.Fatalf("GetByID(%q) missing", authID)
	}
	if updated.Unavailable {
		t.Fatalf("auth marked unavailable after count_tokens 501: %+v", updated)
	}
	if state := updated.ModelStates[model]; state != nil {
		t.Fatalf("model state after count_tokens 501 = %+v, want nil", state)
	}

	resp, errExec := authMgr.Execute(context.Background(), []string{provider}, req, opts)
	if errExec != nil {
		t.Fatalf("Execute after count_tokens 501 failed: %v", errExec)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute returned empty payload")
	}
}

type resultCapturingHook struct {
	results []coreauth.Result
}

func (h *resultCapturingHook) OnAuthRegistered(context.Context, *coreauth.Auth) {}
func (h *resultCapturingHook) OnAuthUpdated(context.Context, *coreauth.Auth)    {}
func (h *resultCapturingHook) OnResult(_ context.Context, result coreauth.Result) {
	h.results = append(h.results, result)
}

func TestPluginExecutor429CredentialScopedCoolsEntireCredentialAndSiblingModels(t *testing.T) {
	coreauth.SetQuotaCooldownDisabled(false)

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	})

	for _, tc := range []struct {
		name         string
		envelopeJSON string
		wantCode     string
	}{
		{
			name:         "credential_scoped flag",
			envelopeJSON: `{"ok":false,"error":{"code":"rate_limited","message":"account limited, resets at 2026-01-01T00:00:00Z","http_status":429,"retry_after_seconds":120,"credential_scoped":true}}`,
			wantCode:     "rate_limited",
		},
		{
			name:         "credential_quota error code",
			envelopeJSON: `{"ok":false,"error":{"code":"credential_quota","message":"account limited, resets at 2026-01-01T00:00:00Z","http_status":429,"retry_after_seconds":120}}`,
			wantCode:     "credential_quota",
		},
		{
			name:         "credential_scoped overrides transient_rate_limit code",
			envelopeJSON: `{"ok":false,"error":{"code":"transient_rate_limit","message":"account limited, resets at 2026-01-01T00:00:00Z","http_status":429,"retry_after_seconds":120,"credential_scoped":true}}`,
			wantCode:     "credential_quota",
		},
		{
			name:         "credential_scoped overrides request_scoped code",
			envelopeJSON: `{"ok":false,"error":{"code":"request_scoped","message":"account limited, resets at 2026-01-01T00:00:00Z","http_status":429,"retry_after_seconds":120,"credential_scoped":true}}`,
			wantCode:     "credential_quota",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subscriber, unsubscribe := redisqueue.SubscribeErrors()
			defer unsubscribe()

			_, errDecode := decodeRPCEnvelope[pluginapi.ExecutorResponse]([]byte(tc.envelopeJSON))
			if errDecode == nil {
				t.Fatal("decodeRPCEnvelope returned nil error")
			}
			var scoped interface{ IsCredentialScoped() bool }
			if !errors.As(errDecode, &scoped) || !scoped.IsCredentialScoped() {
				t.Fatalf("decoded plugin error %T does not report IsCredentialScoped() = true", errDecode)
			}

			var (
				provider   = "plugin-provider-cred-scope-" + tc.name
				modelA     = "plugin-model-a-" + tc.name
				modelB     = "plugin-model-b-" + tc.name
				modelC     = "plugin-model-c-" + tc.name
				authID     = "auth-cred-scope-" + tc.name
				execCalled int
			)
			executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-cred-scope-" + tc.name})
			host := newHostWithRecords(executorRecord)
			exec := &fakeExecutor{
				identifier: provider,
				execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					execCalled++
					return pluginapi.ExecutorResponse{}, errDecode
				},
			}
			adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
				[]sdktranslator.Format{sdktranslator.FormatOpenAI},
				[]sdktranslator.Format{sdktranslator.FormatOpenAI},
			)
			adapter.provider = provider

			hook := &resultCapturingHook{}
			authMgr := coreauth.NewManager(nil, nil, hook)
			authMgr.SetRetryConfig(0, 0, 0)
			authMgr.RegisterExecutor(adapter)
			auth := &coreauth.Auth{
				ID:         authID,
				Provider:   provider,
				Status:     coreauth.StatusActive,
				FileName:   authID + ".json",
				Attributes: map[string]string{"type": "oauth"},
				ModelStates: map[string]*coreauth.ModelState{
					modelB: {Status: coreauth.StatusActive},
				},
			}
			if _, errReg := authMgr.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("Register auth: %v", errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
				{ID: modelA},
				{ID: modelB},
				{ID: modelC},
			})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			})

			opts := coreexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAI,
				ResponseFormat: sdktranslator.FormatOpenAI,
			}

			before := time.Now()
			_, errExecA := authMgr.Execute(context.Background(), []string{provider}, coreexecutor.Request{
				Model:   modelA,
				Payload: []byte(`{"model":"` + modelA + `"}`),
			}, opts)
			if errExecA == nil {
				t.Fatal("Execute(modelA) returned nil error, want 429")
			}
			if execCalled != 1 {
				t.Fatalf("execCalled after modelA = %d, want 1", execCalled)
			}
			if len(hook.results) != 1 || !hook.results[0].CredentialScope {
				t.Fatalf("recorded result = %+v, want CredentialScope=true", hook.results)
			}

			var eventPayload []byte
			select {
			case eventPayload = <-subscriber:
			case <-time.After(time.Second):
				t.Fatal("timeout waiting for published error event")
			}
			statusCode := int(ojson.GetBytes(eventPayload, "status_code").Int())
			eventCode := ojson.GetBytes(eventPayload, "code").String()
			if statusCode != http.StatusTooManyRequests || eventCode != tc.wantCode {
				t.Fatalf("error event status=%d code=%q, want 429 %q", statusCode, eventCode, tc.wantCode)
			}
			unavailable := ojson.GetBytes(eventPayload, "auth_status.unavailable").Bool()
			quotaExceeded := ojson.GetBytes(eventPayload, "auth_status.quota.exceeded").Bool()
			quotaReason := ojson.GetBytes(eventPayload, "auth_status.quota.reason").String()
			if !unavailable || !quotaExceeded || quotaReason != "credential_quota" {
				t.Fatalf("error event auth_status = %s, want unavailable with quota.reason=credential_quota", string(eventPayload))
			}

			updated, okAuth := authMgr.GetByID(authID)
			if !okAuth || updated == nil {
				t.Fatalf("GetByID(%q) missing", authID)
			}
			if !updated.Unavailable || !updated.Quota.Exceeded || updated.Quota.Reason != "credential_quota" {
				t.Fatalf("updated auth quota = %+v (unavailable=%v), want unavailable with reason=credential_quota", updated.Quota, updated.Unavailable)
			}
			if !updated.Quota.NextRecoverAt.After(before.Add(110 * time.Second)) {
				t.Fatalf("updated auth NextRecoverAt = %v, want > %v", updated.Quota.NextRecoverAt, before.Add(110*time.Second))
			}
			stateB := updated.ModelStates[modelB]
			if stateB == nil || !stateB.Unavailable || !stateB.Quota.Exceeded || stateB.Quota.Reason != "credential_quota" {
				t.Fatalf("sibling modelB state = %+v, want unavailable with reason=credential_quota", stateB)
			}

			// Sending requests for sibling modelB (pre-existing ModelState) and modelC (unvisited model)
			// must NOT select the cooled credential again.
			for _, siblingModel := range []string{modelB, modelC} {
				_, errSibling := authMgr.Execute(context.Background(), []string{provider}, coreexecutor.Request{
					Model:   siblingModel,
					Payload: []byte(`{"model":"` + siblingModel + `"}`),
				}, opts)
				if errSibling == nil {
					t.Fatalf("Execute(%s) returned nil error, want cooldown rejection", siblingModel)
				}
				if execCalled != 1 {
					t.Fatalf("execCalled after %s = %d, want 1 (credential must not be selected again for sibling model)", siblingModel, execCalled)
				}
			}
		})
	}
}

func TestStreamCallbacksPreserveCredentialScopedPluginErrors(t *testing.T) {
	detail := `{"code":"rate_limited","message":"account limited","http_status":429,"retry_after_seconds":60,"credential_scoped":true}`
	for _, tc := range []struct {
		name string
		send func(host *Host, streamID string) error
	}{
		{"emit", func(host *Host, streamID string) error {
			_, err := host.callHostStreamEmit(context.Background(), []byte(`{"stream_id":"`+streamID+`","error_detail":`+detail+`}`))
			return err
		}},
		{"close", func(host *Host, streamID string) error {
			_, err := host.callHostStreamClose([]byte(`{"stream_id":"` + streamID + `","error_detail":` + detail + `}`))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := New()
			streamID, chunks, cleanup := host.streams.open(context.Background())
			t.Cleanup(cleanup)
			if err := tc.send(host, streamID); err != nil {
				t.Fatalf("stream %s callback error = %v", tc.name, err)
			}
			chunk := <-chunks
			if chunk.Err == nil {
				t.Fatal("stream error was dropped")
			}
			var scoped interface{ IsCredentialScoped() bool }
			if !errors.As(chunk.Err, &scoped) || !scoped.IsCredentialScoped() {
				t.Fatalf("stream error %T does not report IsCredentialScoped() = true", chunk.Err)
			}
		})
	}
}

func TestPluginExecutorStream429CredentialScopedCoolsEntireCredential(t *testing.T) {
	coreauth.SetQuotaCooldownDisabled(false)

	for _, tc := range []struct {
		name   string
		detail string
	}{
		{
			name:   "rate_limited with credential_scoped",
			detail: `{"code":"rate_limited","message":"account limited","http_status":429,"retry_after_seconds":90,"credential_scoped":true}`,
		},
		{
			name:   "transient_rate_limit overridden by credential_scoped",
			detail: `{"code":"transient_rate_limit","message":"account limited","http_status":429,"retry_after_seconds":90,"credential_scoped":true}`,
		},
		{
			name:   "request_scoped overridden by credential_scoped",
			detail: `{"code":"request_scoped","message":"account limited","http_status":429,"retry_after_seconds":90,"credential_scoped":true}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				provider = "plugin-provider-stream-cred-scope-" + tc.name
				modelA   = "plugin-stream-model-a-" + tc.name
				modelB   = "plugin-stream-model-b-" + tc.name
				authID   = "auth-stream-cred-scope-" + tc.name
			)
			executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-stream-cred-scope-" + tc.name})
			host := newHostWithRecords(executorRecord)

			var streamCalls int
			exec := &fakeExecutor{
				identifier: provider,
				executeStream: func(ctx context.Context, _ pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
					streamCalls++
					streamID, chunks, cleanup := host.streams.open(ctx)
					if _, errClose := host.callHostStreamClose([]byte(`{"stream_id":"` + streamID + `","error_detail":` + tc.detail + `}`)); errClose != nil {
						cleanup()
						return pluginapi.ExecutorStreamResponse{}, errClose
					}
					return pluginapi.ExecutorStreamResponse{
						Chunks: cleanupWhenStreamDone(ctx, chunks, cleanup),
					}, nil
				},
			}
			adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
				[]sdktranslator.Format{sdktranslator.FormatOpenAI},
				[]sdktranslator.Format{sdktranslator.FormatOpenAI},
			)
			adapter.provider = provider

			authMgr := coreauth.NewManager(nil, nil, nil)
			authMgr.SetRetryConfig(0, 0, 0)
			authMgr.RegisterExecutor(adapter)
			auth := &coreauth.Auth{
				ID:         authID,
				Provider:   provider,
				Status:     coreauth.StatusActive,
				FileName:   authID + ".json",
				Attributes: map[string]string{"type": "oauth"},
			}
			if _, errReg := authMgr.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("Register auth: %v", errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
				{ID: modelA},
				{ID: modelB},
			})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			})

			opts := coreexecutor.Options{
				Stream:         true,
				SourceFormat:   sdktranslator.FormatOpenAI,
				ResponseFormat: sdktranslator.FormatOpenAI,
			}

			resStreamA, errStreamA := authMgr.ExecuteStream(context.Background(), []string{provider}, coreexecutor.Request{
				Model:   modelA,
				Payload: []byte(`{"model":"` + modelA + `","stream":true}`),
			}, opts)
			if errStreamA == nil {
				if resStreamA == nil || resStreamA.Chunks == nil {
					t.Fatal("ExecuteStream(modelA) returned nil error and nil chunks")
				}
				chunk := <-resStreamA.Chunks
				errStreamA = chunk.Err
			}
			if errStreamA == nil {
				t.Fatal("ExecuteStream(modelA) returned nil stream error, want 429")
			}
			if streamCalls != 1 {
				t.Fatalf("streamCalls after modelA = %d, want 1", streamCalls)
			}

			updated, okAuth := authMgr.GetByID(authID)
			if !okAuth || updated == nil {
				t.Fatalf("GetByID(%q) missing", authID)
			}
			if !updated.Unavailable || !updated.Quota.Exceeded || updated.Quota.Reason != "credential_quota" {
				t.Fatalf("updated auth quota = %+v (unavailable=%v), want unavailable with reason=credential_quota", updated.Quota, updated.Unavailable)
			}

			_, errStreamB := authMgr.ExecuteStream(context.Background(), []string{provider}, coreexecutor.Request{
				Model:   modelB,
				Payload: []byte(`{"model":"` + modelB + `","stream":true}`),
			}, opts)
			if errStreamB == nil {
				t.Fatal("ExecuteStream(modelB) returned nil error, want cooldown rejection")
			}
			if streamCalls != 1 {
				t.Fatalf("streamCalls after modelB = %d, want 1 (credential must not be selected again for sibling model)", streamCalls)
			}
		})
	}
}
