package pluginabi

import "encoding/json"

const (
	// ABIVersion tracks the native C ABI shape (native plugin exports).
	ABIVersion uint32 = 1
	// SchemaVersionV1 is the original RPC JSON contract.
	SchemaVersionV1 uint32 = 1
	// SchemaVersionV2 adds structured host model execution errors in successful
	// results while retaining the v1 envelope error contract for v1 plugins.
	SchemaVersionV2 uint32 = 2
	// SchemaVersion is the compatibility default for plugins that have not
	// explicitly opted into a newer RPC JSON contract.
	SchemaVersion uint32 = SchemaVersionV1
	// CurrentSchemaVersion is the latest RPC JSON contract supported by the host.
	CurrentSchemaVersion uint32 = SchemaVersionV2
)

const (
	MethodPluginRegister    = "plugin.register"
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

	MethodResponseTranslate            = "response.translate"
	MethodResponseNormalizeBefore      = "response.normalize_before"
	MethodResponseNormalizeAfter       = "response.normalize_after"
	MethodResponseInterceptAfter       = "response.intercept_after"
	MethodResponseInterceptStreamChunk = "response.intercept_stream_chunk"

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
