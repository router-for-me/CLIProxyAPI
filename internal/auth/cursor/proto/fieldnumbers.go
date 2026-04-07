// Package proto provides hand-rolled protobuf encode/decode for Cursor's gRPC API.
// Field numbers are extracted from the TypeScript generated proto/agent_pb.ts in alma-plugins/cursor-auth.
package proto

// AgentClientMessage (msg 118) oneof "message"
const (
	ACM_RunRequest              = 1 // AgentRunRequest
	ACM_ExecClientMessage       = 2 // ExecClientMessage
	ACM_KvClientMessage         = 3 // KvClientMessage
	ACM_ConversationAction      = 4 // ConversationAction
	ACM_ExecClientControlMsg    = 5 // ExecClientControlMessage
	ACM_InteractionResponse     = 6 // InteractionResponse
	ACM_ClientHeartbeat         = 7 // ClientHeartbeat
)

// AgentServerMessage (msg 119) oneof "message"
const (
	ASM_InteractionUpdate         = 1 // InteractionUpdate
	ASM_ExecServerMessage         = 2 // ExecServerMessage
	ASM_ConversationCheckpoint    = 3 // ConversationStateStructure
	ASM_KvServerMessage           = 4 // KvServerMessage
	ASM_ExecServerControlMessage  = 5 // ExecServerControlMessage
	ASM_InteractionQuery          = 7 // InteractionQuery
)

// AgentRunRequest (msg 91)
const (
	ARR_ConversationState = 1 // ConversationStateStructure
	ARR_Action            = 2 // ConversationAction
	ARR_ModelDetails      = 3 // ModelDetails
	ARR_McpTools          = 4 // McpTools
	ARR_ConversationId    = 5 // string (optional)
)

// ConversationStateStructure (msg 83)
const (
	CSS_RootPromptMessagesJson = 1  // repeated bytes
	CSS_TurnsOld               = 2  // repeated bytes (deprecated)
	CSS_Todos                  = 3  // repeated bytes
	CSS_PendingToolCalls       = 4  // repeated string
	CSS_Turns                  = 8  // repeated bytes (CURRENT field for turns)
	CSS_PreviousWorkspaceUris  = 9  // repeated string
	CSS_SelfSummaryCount       = 17 // uint32
	CSS_ReadPaths              = 18 // repeated string
)

// ConversationAction (msg 54) oneof "action"
const (
	CA_UserMessageAction = 1 // UserMessageAction
)

// UserMessageAction (msg 55)
const (
	UMA_UserMessage = 1 // UserMessage
)

// UserMessage (msg 63)
const (
	UM_Text            = 1 // string
	UM_MessageId       = 2 // string
	UM_SelectedContext = 3 // SelectedContext (optional)
)

// SelectedContext
const (
	SC_SelectedImages = 1 // repeated SelectedImage
)

// SelectedImage
const (
	SI_BlobId   = 1 // bytes (oneof dataOrBlobId)
	SI_Uuid     = 2 // string
	SI_Path     = 3 // string
	SI_MimeType = 7 // string
	SI_Data     = 8 // bytes (oneof dataOrBlobId)
)

// ModelDetails (msg 88)
const (
	MD_ModelId        = 1 // string
	MD_ThinkingDetails = 2 // ThinkingDetails (optional)
	MD_DisplayModelId = 3 // string
	MD_DisplayName    = 4 // string
)

// McpTools (msg 307)
const (
	MT_McpTools = 1 // repeated McpToolDefinition
)

// McpToolDefinition (msg 306)
const (
	MTD_Name               = 1 // string
	MTD_Description        = 2 // string
	MTD_InputSchema        = 3 // bytes
	MTD_ProviderIdentifier = 4 // string
	MTD_ToolName           = 5 // string
)

// ConversationTurnStructure (msg 70) oneof "turn"
const (
	CTS_AgentConversationTurn = 1 // AgentConversationTurnStructure
)

// AgentConversationTurnStructure (msg 72)
const (
	ACTS_UserMessage = 1 // bytes (serialized UserMessage)
	ACTS_Steps       = 2 // repeated bytes (serialized ConversationStep)
)

// ConversationStep (msg 53) oneof "message"
const (
	CS_AssistantMessage = 1 // AssistantMessage
)

// AssistantMessage
const (
	AM_Text = 1 // string
)

// --- Server-side message fields ---

// InteractionUpdate oneof "message"
const (
	IU_TextDelta         = 1  // TextDeltaUpdate
	IU_ThinkingDelta     = 4  // ThinkingDeltaUpdate
	IU_ThinkingCompleted = 5  // ThinkingCompletedUpdate
)

// TextDeltaUpdate (msg 92)
const (
	TDU_Text = 1 // string
)

// ThinkingDeltaUpdate (msg 97)
const (
	TKD_Text = 1 // string
)

// KvServerMessage (msg 271)
const (
	KSM_Id          = 1 // uint32
	KSM_GetBlobArgs = 2 // GetBlobArgs
	KSM_SetBlobArgs = 3 // SetBlobArgs
)

// GetBlobArgs (msg 267)
const (
	GBA_BlobId = 1 // bytes
)

// SetBlobArgs (msg 269)
const (
	SBA_BlobId   = 1 // bytes
	SBA_BlobData = 2 // bytes
)

// KvClientMessage (msg 272)
const (
	KCM_Id            = 1 // uint32
	KCM_GetBlobResult = 2 // GetBlobResult
	KCM_SetBlobResult = 3 // SetBlobResult
)

// GetBlobResult (msg 268)
const (
	GBR_BlobData = 1 // bytes (optional)
)

// ExecServerMessage
const (
	ESM_Id      = 1  // uint32
	ESM_ExecId  = 15 // string
	// oneof message:
	ESM_ShellArgs              = 2  // ShellArgs
	ESM_WriteArgs              = 3  // WriteArgs
	ESM_DeleteArgs             = 4  // DeleteArgs
	ESM_GrepArgs               = 5  // GrepArgs
	ESM_ReadArgs               = 7  // ReadArgs (NOTE: 6 is skipped)
	ESM_LsArgs                 = 8  // LsArgs
	ESM_DiagnosticsArgs        = 9  // DiagnosticsArgs
	ESM_RequestContextArgs     = 10 // RequestContextArgs
	ESM_McpArgs                = 11 // McpArgs
	ESM_ShellStreamArgs        = 14 // ShellArgs (stream variant)
	ESM_BackgroundShellSpawn   = 16 // BackgroundShellSpawnArgs
	ESM_FetchArgs              = 20 // FetchArgs
	ESM_WriteShellStdinArgs    = 23 // WriteShellStdinArgs
)

// ExecClientMessage
const (
	ECM_Id     = 1  // uint32
	ECM_ExecId = 15 // string
	// oneof message (mirrors server fields):
	ECM_ShellResult              = 2
	ECM_WriteResult              = 3
	ECM_DeleteResult             = 4
	ECM_GrepResult               = 5
	ECM_ReadResult               = 7
	ECM_LsResult                 = 8
	ECM_DiagnosticsResult        = 9
	ECM_RequestContextResult     = 10
	ECM_McpResult                = 11
	ECM_ShellStream              = 14
	ECM_BackgroundShellSpawnRes  = 16
	ECM_FetchResult              = 20
	ECM_WriteShellStdinResult    = 23
)

// McpArgs
const (
	MCA_Name               = 1 // string
	MCA_Args               = 2 // map<string, bytes>
	MCA_ToolCallId         = 3 // string
	MCA_ProviderIdentifier = 4 // string
	MCA_ToolName           = 5 // string
)

// RequestContextResult oneof "result"
const (
	RCR_Success = 1 // RequestContextSuccess
	RCR_Error   = 2 // RequestContextError
)

// RequestContextSuccess (msg 337)
const (
	RCS_RequestContext = 1 // RequestContext
)

// RequestContext
const (
	RC_Rules = 2 // repeated CursorRule
	RC_Tools = 7 // repeated McpToolDefinition
)

// McpResult oneof "result"
const (
	MCR_Success  = 1 // McpSuccess
	MCR_Error    = 2 // McpError
	MCR_Rejected = 3 // McpRejected
)

// McpSuccess (msg 290)
const (
	MCS_Content = 1 // repeated McpToolResultContentItem
	MCS_IsError = 2 // bool
)

// McpToolResultContentItem oneof "content"
const (
	MTRCI_Text = 1 // McpTextContent
)

// McpTextContent (msg 287)
const (
	MTC_Text = 1 // string
)

// McpError (msg 291)
const (
	MCE_Error = 1 // string
)

// --- Rejection messages ---

// ReadRejected: path=1, reason=2
// ShellRejected: command=1, workingDirectory=2, reason=3, isReadonly=4
// WriteRejected: path=1, reason=2
// DeleteRejected: path=1, reason=2
// LsRejected: path=1, reason=2
// GrepError: error=1
// FetchError: url=1, error=2
// WriteShellStdinError: error=1

// ReadResult oneof: success=1, error=2, rejected=3
// ShellResult oneof: success=1 (+ various), rejected=?
// The TS code uses specific result field numbers from the oneof:
const (
	RR_Rejected = 3 // ReadResult.rejected
	SR_Rejected = 5 // ShellResult.rejected (from TS: ShellResult has success/various/rejected)
	WR_Rejected = 5 // WriteResult.rejected
	DR_Rejected = 3 // DeleteResult.rejected
	LR_Rejected = 3 // LsResult.rejected
	GR_Error    = 2 // GrepResult.error
	FR_Error    = 2 // FetchResult.error
	BSSR_Rejected = 2 // BackgroundShellSpawnResult.rejected (error field)
	WSSR_Error    = 2 // WriteShellStdinResult.error
)

// --- Rejection struct fields ---
const (
	REJ_Path             = 1
	REJ_Reason           = 2
	SREJ_Command         = 1
	SREJ_WorkingDir      = 2
	SREJ_Reason          = 3
	SREJ_IsReadonly      = 4
	GERR_Error           = 1
	FERR_Url             = 1
	FERR_Error           = 2
)

// ReadArgs
const (
	RA_Path = 1 // string
)

// WriteArgs
const (
	WA_Path = 1 // string
)

// DeleteArgs
const (
	DA_Path = 1 // string
)

// LsArgs
const (
	LA_Path = 1 // string
)

// ShellArgs
const (
	SHA_Command          = 1 // string
	SHA_WorkingDirectory = 2 // string
)

// FetchArgs
const (
	FA_Url = 1 // string
)
