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
	SchemaVersion uint32 = 5
	// SchemaVersionStreamChunkOmitRequestBody is the first schema version that omits
	// request bodies on payload stream-chunk interceptor calls.
	SchemaVersionStreamChunkOmitRequestBody uint32 = 3
	// SchemaVersionWebSocketResponseObserver is the first schema version that supports
	// upstream WebSocket response event observation.
	SchemaVersionWebSocketResponseObserver uint32 = 4
	// SchemaVersionStreamChunkOmitHistory is the first schema version that omits
	// history chunks on payload stream-chunk interceptor calls.
	SchemaVersionStreamChunkOmitHistory uint32 = 5
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

	MethodHostHTTPDo             = "host.http.do"
	MethodHostHTTPDoStream       = "host.http.do_stream"
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
)

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}
