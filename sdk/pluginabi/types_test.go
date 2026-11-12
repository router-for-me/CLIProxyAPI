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
	if SchemaVersion != 10 {
		t.Fatalf("SchemaVersion = %d, want 10", SchemaVersion)
	}
	if SchemaVersionMultiAuthProviders != 10 {
		t.Fatalf("SchemaVersionMultiAuthProviders = %d, want 10", SchemaVersionMultiAuthProviders)
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
	if SchemaVersionRequestInterceptorOmitBody != 7 {
		t.Fatalf("SchemaVersionRequestInterceptorOmitBody = %d, want 7", SchemaVersionRequestInterceptorOmitBody)
	}
	if SchemaVersionFrontendAuthOmitBody != 7 {
		t.Fatalf("SchemaVersionFrontendAuthOmitBody = %d, want 7", SchemaVersionFrontendAuthOmitBody)
	}
	if SchemaVersionRequestInterceptorBodyPatch != 8 {
		t.Fatalf("SchemaVersionRequestInterceptorBodyPatch = %d, want 8", SchemaVersionRequestInterceptorBodyPatch)
	}
	if SchemaVersionMultiProviderModels != 9 {
		t.Fatalf("SchemaVersionMultiProviderModels = %d, want 9", SchemaVersionMultiProviderModels)
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
	if MethodRequestPatchBodyBefore != "request.patch_body_before" {
		t.Fatalf("MethodRequestPatchBodyBefore = %q", MethodRequestPatchBodyBefore)
	}
	if MethodRequestPatchBodyAfter != "request.patch_body_after" {
		t.Fatalf("MethodRequestPatchBodyAfter = %q", MethodRequestPatchBodyAfter)
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
	if MethodHostHTTPDoStream != "host.http.do_stream" {
		t.Fatalf("MethodHostHTTPDoStream = %q", MethodHostHTTPDoStream)
	}
	if MethodHostHTTPOperationOpen != "host.http.operation_open" {
		t.Fatalf("MethodHostHTTPOperationOpen = %q", MethodHostHTTPOperationOpen)
	}
	if MethodHostHTTPCancel != "host.http.cancel" {
		t.Fatalf("MethodHostHTTPCancel = %q", MethodHostHTTPCancel)
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
	if MethodHostAuthModels != "host.auth.models" {
		t.Fatalf("MethodHostAuthModels = %q", MethodHostAuthModels)
	}
	if MethodHostAffinityLookup != "host.affinity.lookup" {
		t.Fatalf("MethodHostAffinityLookup = %q", MethodHostAffinityLookup)
	}
	if MethodHostRoutingResetCooldown != "host.routing.reset_cooldown" {
		t.Fatalf("MethodHostRoutingResetCooldown = %q", MethodHostRoutingResetCooldown)
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

func TestErrorCredentialScopedAndCodes(t *testing.T) {
	var _ interface{ IsCredentialScoped() bool } = (*Error)(nil)

	var nilErr *Error
	if nilErr.IsCredentialScoped() {
		t.Fatal("nil Error reported IsCredentialScoped() = true")
	}

	modelScoped := NewError("rate_limited", "per-model rate limit", 429)
	if modelScoped.IsCredentialScoped() {
		t.Fatal("model-scoped Error reported IsCredentialScoped() = true")
	}

	flagScoped := &Error{
		Code:              "rate_limited",
		Message:           "account limited, resets at 2026-01-01T00:00:00Z",
		HTTPStatus:        429,
		RetryAfterSeconds: 120,
		CredentialScoped:  true,
	}
	if !flagScoped.IsCredentialScoped() {
		t.Fatal("Error with CredentialScoped=true reported IsCredentialScoped() = false")
	}

	raw, errMarshal := json.Marshal(Envelope{OK: false, Error: flagScoped})
	if errMarshal != nil {
		t.Fatalf("marshal credential-scoped envelope: %v", errMarshal)
	}
	if !strings.Contains(string(raw), `"credential_scoped":true`) {
		t.Fatalf("marshaled envelope missing credential_scoped: %s", string(raw))
	}
	var decoded Envelope
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal credential-scoped envelope: %v", errUnmarshal)
	}
	if decoded.Error == nil || !decoded.Error.CredentialScoped || !decoded.Error.IsCredentialScoped() {
		t.Fatalf("decoded error = %+v, want CredentialScoped=true", decoded.Error)
	}

	codeScoped := NewError(ErrorCodeCredentialQuota, "daily quota spent", 429)
	if !codeScoped.IsCredentialScoped() {
		t.Fatal("Error with ErrorCodeCredentialQuota reported IsCredentialScoped() = false")
	}
	if codeScoped.IsRequestScoped() || codeScoped.IsTransientRateLimit() {
		t.Fatal("credential-scoped Error reported request-scoped or transient-rate-limit")
	}

	reqScoped := NewError(ErrorCodeRequestScoped, "invalid prompt", 400)
	if !reqScoped.IsRequestScoped() {
		t.Fatal("Error with ErrorCodeRequestScoped reported IsRequestScoped() = false")
	}

	transientRL := NewError(ErrorCodeTransientRateLimit, "turn rate limit", 429)
	if !transientRL.IsTransientRateLimit() {
		t.Fatal("Error with ErrorCodeTransientRateLimit reported IsTransientRateLimit() = false")
	}

	truncated := NewError(ErrorCodeStreamTruncated, "stream ended early", 502)
	if !truncated.IsStreamTruncated() {
		t.Fatal("Error with ErrorCodeStreamTruncated reported IsStreamTruncated() = false")
	}
	truncatedCredScoped := &Error{
		Code:             ErrorCodeStreamTruncated,
		Message:          "stream ended early",
		HTTPStatus:       429,
		CredentialScoped: true,
	}
	if !truncatedCredScoped.IsCredentialScoped() || truncatedCredScoped.IsStreamTruncated() {
		t.Fatal("credential-scoped Error with stream_truncated code must prioritize IsCredentialScoped()")
	}

	if ErrorCodeCredentialQuota != "credential_quota" ||
		ErrorCodeTransientRateLimit != "transient_rate_limit" ||
		ErrorCodeRequestScoped != "request_scoped" ||
		ErrorCodeForceCooldown != "force_cooldown" ||
		ErrorCodeStreamTruncated != "stream_truncated" ||
		ErrorCodeConnectionLifecycle != "connection_lifecycle" ||
		ErrorCodeTransientTransport != "transient_transport" ||
		ErrorCodeModelNotFound != "model_not_found" ||
		ErrorCodeNotImplemented != "not_implemented" {
		t.Fatal("unexpected routing/cooldown error code constant values")
	}
}
