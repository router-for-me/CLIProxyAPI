package pluginabi

import "encoding/json"

const (
	// ABIVersion tracks the native C ABI shape (native plugin exports).
	ABIVersion uint32 = 1
	// SchemaVersion tracks the RPC JSON contract exchanged at plugin.register.
	// Version 2 adds request lifecycle completion and active request termination.
	// Version 3 omits OriginalRequest/RequestBody on payload stream chunks
	// (ChunkIndex >= 0); those fields remain on StreamChunkHeaderInitIndex only.
	// Plugins that still need per-chunk request bodies should keep schema_version < 3.
	// Version 4 adds upstream WebSocket response event observation.
	// Version 5 omits HistoryChunks on payload stream chunks (ChunkIndex >= 0);
	// those fields remain on StreamChunkHeaderInitIndex only. Plugins that still need
	// per-chunk history chunks should keep schema_version < 5.
	// Version 6 preserves raw JSON bodies for plugin management responses.
	// Plugins that still require HTML entity escaping on JSON response strings
	// should keep schema_version < 6.
	// Version 7 lets request interceptors opt out of receiving the request body
	// per phase via request_interceptor_omit_body_before/after capabilities and
	// lets frontend auth providers opt out via frontend_auth_provider_omit_body /
	// frontend_auth_omit_body; the host then omits the request body instead of
	// reading, cloning, and Base64-encoding the full payload. Plugins that declare
	// the flags on schema_version < 7 (or do not declare them) still receive it.
	// Version 8 lets request interceptors opt into raw request body delivery and
	// localized byte-range patching per phase via request_interceptor_patch_body_before/after
	// (or request_body_patcher_before/after) capabilities and the
	// request.patch_body_before / request.patch_body_after methods; the host passes
	// raw request body bytes directly across the C ABI without JSON/Base64 wrapping
	// and splices validated byte-range edits in a single pass.
	// Version 9 lets ModelRegistrar.RegisterModels and ModelProvider.StaticModels
	// publish model catalogs for multiple providers in one response via Registrations.
	SchemaVersion uint32 = 9
	// SchemaVersionStreamChunkOmitRequestBody is the first schema version that omits
	// request bodies on payload stream-chunk interceptor calls.
	SchemaVersionStreamChunkOmitRequestBody uint32 = 3
	// SchemaVersionWebSocketResponseObserver is the first schema version that supports
	// upstream WebSocket response event observation.
	SchemaVersionWebSocketResponseObserver uint32 = 4
	// SchemaVersionStreamChunkOmitHistory is the first schema version that omits
	// history chunks on payload stream-chunk interceptor calls.
	SchemaVersionStreamChunkOmitHistory uint32 = 5
	// SchemaVersionRawManagementResponse is the first schema version where plugin
	// management JSON responses are preserved without HTML-escaping strings.
	SchemaVersionRawManagementResponse uint32 = 6
	// SchemaVersionRequestInterceptorOmitBody is the first schema version where
	// request interceptors may opt out of receiving the request body per phase.
	SchemaVersionRequestInterceptorOmitBody uint32 = 7
	// SchemaVersionFrontendAuthOmitBody is the first schema version where
	// frontend auth providers may opt out of receiving the request body.
	SchemaVersionFrontendAuthOmitBody uint32 = 7
	// SchemaVersionRequestInterceptorBodyPatch is the first schema version where
	// request interceptors may opt into raw request body delivery and byte-range patching.
	SchemaVersionRequestInterceptorBodyPatch uint32 = 8
	// SchemaVersionMultiProviderModels is the first schema version where model
	// registration responses may publish models for multiple providers via Registrations.
	SchemaVersionMultiProviderModels uint32 = 9
)

const (
	MethodPluginRegister    = "plugin.register"
	MethodPluginQuiesce     = "plugin.quiesce"
	MethodPluginReconfigure = "plugin.reconfigure"
	MethodPluginShutdown    = "plugin.shutdown"

	MethodModelRegister = "model.register"
	MethodModelStatic   = "model.static"
	MethodModelForAuth  = "model.for_auth"

	MethodAuthIdentifier = "auth.identifier"
	MethodAuthParse      = "auth.parse"
	MethodAuthLoginStart = "auth.login.start"
	MethodAuthLoginPoll  = "auth.login.poll"
	MethodAuthRefresh    = "auth.refresh"

	MethodFrontendAuthIdentifier   = "frontend_auth.identifier"
	MethodFrontendAuthAuthenticate = "frontend_auth.authenticate"

	// MethodSchedulerPick asks a scheduler plugin to select an auth candidate.
	MethodSchedulerPick = "scheduler.pick"
	// MethodModelRoute asks a router plugin to select a plugin executor for a matching request.
	MethodModelRoute = "model.route"

	MethodExecutorIdentifier    = "executor.identifier"
	MethodExecutorExecute       = "executor.execute"
	MethodExecutorExecuteStream = "executor.execute_stream"
	MethodExecutorCountTokens   = "executor.count_tokens"
	MethodExecutorHTTPRequest   = "executor.http_request"

	MethodRequestTranslate       = "request.translate"
	MethodRequestNormalize       = "request.normalize"
	MethodRequestInterceptBefore = "request.intercept_before"
	MethodRequestInterceptAfter  = "request.intercept_after"
	MethodRequestPatchBodyBefore = "request.patch_body_before"
	MethodRequestPatchBodyAfter  = "request.patch_body_after"
	MethodRequestComplete        = "request.complete"

	MethodResponseTranslate            = "response.translate"
	MethodResponseNormalizeBefore      = "response.normalize_before"
	MethodResponseNormalizeAfter       = "response.normalize_after"
	MethodResponseInterceptAfter       = "response.intercept_after"
	MethodResponseInterceptStreamChunk = "response.intercept_stream_chunk"

	MethodWebSocketResponseEvent = "websocket.response_event"

	MethodThinkingIdentifier = "thinking.identifier"
	MethodThinkingApply      = "thinking.apply"

	MethodUsageHandle = "usage.handle"

	MethodCommandLineRegister = "command_line.register"
	MethodCommandLineExecute  = "command_line.execute"

	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"

	MethodQuotaIdentifier = "quota.identifier"
	MethodQuotaDescribe   = "quota.describe"
	MethodQuotaFetch      = "quota.fetch"
	MethodQuotaReset      = "quota.reset"

	MethodHostHTTPDo             = "host.http.do"
	MethodHostHTTPDoStream       = "host.http.do_stream"
	MethodHostHTTPOperationOpen  = "host.http.operation_open"
	MethodHostHTTPCancel         = "host.http.cancel"
	MethodHostHTTPStreamRead     = "host.http.stream_read"
	MethodHostHTTPStreamClose    = "host.http.stream_close"
	MethodHostModelExecute       = "host.model.execute"
	MethodHostModelExecuteStream = "host.model.execute_stream"
	MethodHostModelStreamRead    = "host.model.stream_read"
	MethodHostModelStreamClose   = "host.model.stream_close"
	MethodHostStreamEmit         = "host.stream.emit"
	MethodHostStreamClose        = "host.stream.close"
	MethodHostLog                = "host.log"
	MethodHostAuthList           = "host.auth.list"
	MethodHostAuthGet            = "host.auth.get"
	MethodHostAuthGetRuntime     = "host.auth.get_runtime"
	MethodHostAuthSave           = "host.auth.save"
	MethodHostAuthModels         = "host.auth.models"
	MethodHostAffinityLookup     = "host.affinity.lookup"

	MethodHostRoutingResetCooldown = "host.routing.reset_cooldown"
)

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	// HTTPStatus is the HTTP status code (e.g. 401, 403, 429) to surface to the client.
	// When omitted or 0, CPA defaults to HTTP 500 (internal_server_error).
	HTTPStatus int `json:"http_status,omitempty"`
	// RetryAfterSeconds carries upstream retry guidance for cooldown and response headers.
	// It is an optional hint. Schema-version agreement does not negotiate support for
	// this field, and older hosts silently ignore it, so plugins must not depend on it
	// for correctness. The same pair is accepted as error_detail on host.stream.emit
	// and host.stream.close.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
}

// Error implements the error interface for Error.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// StatusCode returns the HTTP status code embedded in the Error, or 0 if unset.
func (e *Error) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

// NewError creates an Error instance with an optional HTTP status code.
func NewError(code, message string, httpStatus ...int) *Error {
	status := 0
	if len(httpStatus) > 0 {
		status = httpStatus[0]
	}
	return &Error{
		Code:       code,
		Message:    message,
		HTTPStatus: status,
	}
}

// NewErrorEnvelope serializes a failed RPC Envelope containing an Error with an optional HTTP status code.
func NewErrorEnvelope(code, message string, httpStatus ...int) ([]byte, error) {
	return json.Marshal(Envelope{
		OK:    false,
		Error: NewError(code, message, httpStatus...),
	})
}
