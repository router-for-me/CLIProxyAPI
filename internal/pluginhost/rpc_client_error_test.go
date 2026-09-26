package pluginhost

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type staticEnvelopePluginClient struct {
	raw []byte
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
