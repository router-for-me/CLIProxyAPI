package pluginabi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	payload := json.RawMessage(`{"name":"example"}`)
	env := Envelope{
		OK:     true,
		Result: payload,
	}

	raw, errMarshal := json.Marshal(env)
	if errMarshal != nil {
		t.Fatalf("marshal envelope: %v", errMarshal)
	}

	var decoded Envelope
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal envelope: %v", errUnmarshal)
	}
	if !decoded.OK || string(decoded.Result) != string(payload) {
		t.Fatalf("decoded envelope = %#v, want ok payload", decoded)
	}
}

func TestMethodNamesAreStable(t *testing.T) {
	if SchemaVersion != 6 {
		t.Fatalf("SchemaVersion = %d, want 6", SchemaVersion)
	}
	if SchemaVersionWebSocketResponseObserver != 4 {
		t.Fatalf("SchemaVersionWebSocketResponseObserver = %d, want 4", SchemaVersionWebSocketResponseObserver)
	}
	if SchemaVersionStreamChunkOmitRequestBody != 3 {
		t.Fatalf("SchemaVersionStreamChunkOmitRequestBody = %d, want 3", SchemaVersionStreamChunkOmitRequestBody)
	}
	if SchemaVersionStreamChunkOmitHistory != 5 {
		t.Fatalf("SchemaVersionStreamChunkOmitHistory = %d, want 5", SchemaVersionStreamChunkOmitHistory)
	}
	if SchemaVersionRawManagementResponse != 6 {
		t.Fatalf("SchemaVersionRawManagementResponse = %d, want 6", SchemaVersionRawManagementResponse)
	}
	if MethodPluginRegister != "plugin.register" {
		t.Fatalf("MethodPluginRegister = %q", MethodPluginRegister)
	}
	if MethodPluginQuiesce != "plugin.quiesce" {
		t.Fatalf("MethodPluginQuiesce = %q", MethodPluginQuiesce)
	}
	if MethodRequestInterceptBefore != "request.intercept_before" {
		t.Fatalf("MethodRequestInterceptBefore = %q", MethodRequestInterceptBefore)
	}
	if MethodRequestInterceptAfter != "request.intercept_after" {
		t.Fatalf("MethodRequestInterceptAfter = %q", MethodRequestInterceptAfter)
	}
	if MethodRequestComplete != "request.complete" {
		t.Fatalf("MethodRequestComplete = %q", MethodRequestComplete)
	}
	if MethodResponseInterceptAfter != "response.intercept_after" {
		t.Fatalf("MethodResponseInterceptAfter = %q", MethodResponseInterceptAfter)
	}
	if MethodResponseInterceptStreamChunk != "response.intercept_stream_chunk" {
		t.Fatalf("MethodResponseInterceptStreamChunk = %q", MethodResponseInterceptStreamChunk)
	}
	if MethodWebSocketResponseEvent != "websocket.response_event" {
		t.Fatalf("MethodWebSocketResponseEvent = %q", MethodWebSocketResponseEvent)
	}
	if MethodHostHTTPDo != "host.http.do" {
		t.Fatalf("MethodHostHTTPDo = %q", MethodHostHTTPDo)
	}
	if MethodHostHTTPStreamRead != "host.http.stream_read" {
		t.Fatalf("MethodHostHTTPStreamRead = %q", MethodHostHTTPStreamRead)
	}
	if MethodHostModelExecute != "host.model.execute" {
		t.Fatalf("MethodHostModelExecute = %q", MethodHostModelExecute)
	}
	if MethodHostModelExecuteStream != "host.model.execute_stream" {
		t.Fatalf("MethodHostModelExecuteStream = %q", MethodHostModelExecuteStream)
	}
	if MethodHostModelStreamRead != "host.model.stream_read" {
		t.Fatalf("MethodHostModelStreamRead = %q", MethodHostModelStreamRead)
	}
	if MethodHostModelStreamClose != "host.model.stream_close" {
		t.Fatalf("MethodHostModelStreamClose = %q", MethodHostModelStreamClose)
	}
	if MethodHostAuthList != "host.auth.list" {
		t.Fatalf("MethodHostAuthList = %q", MethodHostAuthList)
	}
	if MethodHostAuthGet != "host.auth.get" {
		t.Fatalf("MethodHostAuthGet = %q", MethodHostAuthGet)
	}
	if MethodHostAuthGetRuntime != "host.auth.get_runtime" {
		t.Fatalf("MethodHostAuthGetRuntime = %q", MethodHostAuthGetRuntime)
	}
	if MethodHostAuthSave != "host.auth.save" {
		t.Fatalf("MethodHostAuthSave = %q", MethodHostAuthSave)
	}
	if MethodHostAffinityLookup != "host.affinity.lookup" {
		t.Fatalf("MethodHostAffinityLookup = %q", MethodHostAffinityLookup)
	}
	if MethodExecutorExecuteStream != "executor.execute_stream" {
		t.Fatalf("MethodExecutorExecuteStream = %q", MethodExecutorExecuteStream)
	}
}

func TestSchedulerPickMethodName(t *testing.T) {
	if MethodSchedulerPick != "scheduler.pick" {
		t.Fatalf("MethodSchedulerPick = %q", MethodSchedulerPick)
	}
	if MethodModelRoute != "model.route" {
		t.Fatalf("MethodModelRoute = %q", MethodModelRoute)
	}
	if MethodQuotaIdentifier != "quota.identifier" {
		t.Fatalf("MethodQuotaIdentifier = %q", MethodQuotaIdentifier)
	}
	if MethodQuotaDescribe != "quota.describe" {
		t.Fatalf("MethodQuotaDescribe = %q", MethodQuotaDescribe)
	}
	if MethodQuotaFetch != "quota.fetch" {
		t.Fatalf("MethodQuotaFetch = %q", MethodQuotaFetch)
	}
	if MethodQuotaReset != "quota.reset" {
		t.Fatalf("MethodQuotaReset = %q", MethodQuotaReset)
	}
}

func TestErrorInterfaceAndConstructor(t *testing.T) {
	var _ error = (*Error)(nil)
	var _ interface{ StatusCode() int } = (*Error)(nil)

	err := NewError("insufficient_quota", "plan limit reached", 403)
	if err == nil {
		t.Fatal("NewError returned nil")
	}
	if err.Error() != "plan limit reached" {
		t.Fatalf("err.Error() = %q, want plan limit reached", err.Error())
	}
	if err.StatusCode() != 403 {
		t.Fatalf("err.StatusCode() = %d, want 403", err.StatusCode())
	}

	envBytes, errEnv := NewErrorEnvelope("rate_limit_exceeded", "too many requests", 429)
	if errEnv != nil {
		t.Fatalf("NewErrorEnvelope() error = %v", errEnv)
	}
	var env Envelope
	if errUnmarshal := json.Unmarshal(envBytes, &env); errUnmarshal != nil {
		t.Fatalf("unmarshal error envelope: %v", errUnmarshal)
	}
	if env.OK {
		t.Fatalf("env.OK = true, want false")
	}
	if env.Error == nil {
		t.Fatal("env.Error = nil, want non-nil")
	}
	if env.Error.Code != "rate_limit_exceeded" || env.Error.HTTPStatus != 429 {
		t.Fatalf("unexpected error payload: %+v", env.Error)
	}

	// Test zero / omitted status
	defaultErr := NewError("server_error", "unexpected error")
	if defaultErr.StatusCode() != 0 {
		t.Fatalf("defaultErr.StatusCode() = %d, want 0", defaultErr.StatusCode())
	}
	defaultEnvBytes, errDefaultEnv := NewErrorEnvelope("server_error", "unexpected error")
	if errDefaultEnv != nil {
		t.Fatalf("NewErrorEnvelope() error = %v", errDefaultEnv)
	}
	if strings.Contains(string(defaultEnvBytes), "http_status") {
		t.Fatalf("default envelope JSON unexpectedly contained http_status: %s", string(defaultEnvBytes))
	}
}
