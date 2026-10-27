package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

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
