// Package proto provides protobuf encoding for Cursor's gRPC API,
// using dynamicpb with the embedded FileDescriptorProto from agent.proto.
// This mirrors the cursor-auth TS plugin's use of @bufbuild/protobuf create()+toBinary().
package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// --- Public types ---

// RunRequestParams holds all data needed to build an AgentRunRequest.
type RunRequestParams struct {
	ModelId        string
	SystemPrompt   string
	UserText       string
	MessageId      string
	ConversationId string
	Images         []ImageData
	Turns          []TurnData
	McpTools       []McpToolDef
	BlobStore      map[string][]byte // hex(sha256) -> data, populated during encoding
	RawCheckpoint  []byte            // if non-nil, use as conversation_state directly (from server checkpoint)
}

type ImageData struct {
	MimeType string
	Data     []byte
}

type TurnData struct {
	UserText      string
	AssistantText string
}

type McpToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// --- Helper: create a dynamic message and set fields ---

func newMsg(name string) *dynamicpb.Message {
	return dynamicpb.NewMessage(Msg(name))
}

func field(msg *dynamicpb.Message, name string) protoreflect.FieldDescriptor {
	return msg.Descriptor().Fields().ByName(protoreflect.Name(name))
}

func setStr(msg *dynamicpb.Message, name, val string) {
	if val != "" {
		msg.Set(field(msg, name), protoreflect.ValueOfString(val))
	}
}

func setBytes(msg *dynamicpb.Message, name string, val []byte) {
	if len(val) > 0 {
		msg.Set(field(msg, name), protoreflect.ValueOfBytes(val))
	}
}

func setUint32(msg *dynamicpb.Message, name string, val uint32) {
	msg.Set(field(msg, name), protoreflect.ValueOfUint32(val))
}

func setBool(msg *dynamicpb.Message, name string, val bool) {
	msg.Set(field(msg, name), protoreflect.ValueOfBool(val))
}

func setMsg(msg *dynamicpb.Message, name string, sub *dynamicpb.Message) {
	msg.Set(field(msg, name), protoreflect.ValueOfMessage(sub.ProtoReflect()))
}

func marshal(msg *dynamicpb.Message) []byte {
	b, err := proto.Marshal(msg)
	if err != nil {
		panic("cursor proto marshal: " + err.Error())
	}
	return b
}

// --- Encode functions mirroring cursor-fetch.ts ---

// EncodeHeartbeat returns an encoded AgentClientMessage with clientHeartbeat.
// Mirrors: create(AgentClientMessageSchema, { message: { case: 'clientHeartbeat', value: create(ClientHeartbeatSchema, {}) } })
func EncodeHeartbeat() []byte {
	hb := newMsg("ClientHeartbeat")
	acm := newMsg("AgentClientMessage")
	setMsg(acm, "client_heartbeat", hb)
	return marshal(acm)
}

// EncodeRunRequest builds a full AgentClientMessage wrapping an AgentRunRequest.
// Mirrors buildCursorRequest() in cursor-fetch.ts.
// If p.RawCheckpoint is set, it is used directly as the conversation_state bytes
// (from a previous conversation_checkpoint_update), skipping manual turn construction.
func EncodeRunRequest(p *RunRequestParams) []byte {
	if p.RawCheckpoint != nil {
		return encodeRunRequestWithCheckpoint(p)
	}

	if p.BlobStore == nil {
		p.BlobStore = make(map[string][]byte)
	}

	// --- Conversation turns ---
	// Each turn is serialized as bytes (ConversationTurnStructure → bytes)
	var turnBytes [][]byte
	for _, turn := range p.Turns {
		// UserMessage for this turn
		um := newMsg("UserMessage")
		setStr(um, "text", turn.UserText)
		setStr(um, "message_id", generateId())
		umBytes := marshal(um)

		// Steps (assistant response)
		var stepBytes [][]byte
		if turn.AssistantText != "" {
			am := newMsg("AssistantMessage")
			setStr(am, "text", turn.AssistantText)
			step := newMsg("ConversationStep")
			setMsg(step, "assistant_message", am)
			stepBytes = append(stepBytes, marshal(step))
		}

		// AgentConversationTurnStructure (fields are bytes, not submessages)
		agentTurn := newMsg("AgentConversationTurnStructure")
		setBytes(agentTurn, "user_message", umBytes)
		for _, sb := range stepBytes {
			stepsField := field(agentTurn, "steps")
			list := agentTurn.Mutable(stepsField).List()
			list.Append(protoreflect.ValueOfBytes(sb))
		}

		// ConversationTurnStructure (oneof turn → agentConversationTurn)
		cts := newMsg("ConversationTurnStructure")
		setMsg(cts, "agent_conversation_turn", agentTurn)
		turnBytes = append(turnBytes, marshal(cts))
	}

	// --- System prompt blob ---
	systemJSON, _ := json.Marshal(map[string]string{"role": "system", "content": p.SystemPrompt})
	blobId := sha256Sum(systemJSON)
	p.BlobStore[hex.EncodeToString(blobId)] = systemJSON

	// --- ConversationStateStructure ---
	css := newMsg("ConversationStateStructure")
	// rootPromptMessagesJson: repeated bytes
	rootField := field(css, "root_prompt_messages_json")
	rootList := css.Mutable(rootField).List()
	rootList.Append(protoreflect.ValueOfBytes(blobId))
	// turns: repeated bytes (field 8) + turns_old (field 2) for compatibility
	turnsField := field(css, "turns")
	turnsList := css.Mutable(turnsField).List()
	for _, tb := range turnBytes {
		turnsList.Append(protoreflect.ValueOfBytes(tb))
	}
	turnsOldField := field(css, "turns_old")
	if turnsOldField != nil {
		turnsOldList := css.Mutable(turnsOldField).List()
		for _, tb := range turnBytes {
			turnsOldList.Append(protoreflect.ValueOfBytes(tb))
		}
	}

	// --- UserMessage (current) ---
	userMessage := newMsg("UserMessage")
	setStr(userMessage, "text", p.UserText)
	setStr(userMessage, "message_id", p.MessageId)

	// Images via SelectedContext
	if len(p.Images) > 0 {
		sc := newMsg("SelectedContext")
		imgsField := field(sc, "selected_images")
		imgsList := sc.Mutable(imgsField).List()
		for _, img := range p.Images {
			si := newMsg("SelectedImage")
			setStr(si, "uuid", generateId())
			setStr(si, "mime_type", img.MimeType)
			setBytes(si, "data", img.Data)
			imgsList.Append(protoreflect.ValueOfMessage(si.ProtoReflect()))
		}
		setMsg(userMessage, "selected_context", sc)
	}

	// --- UserMessageAction ---
	uma := newMsg("UserMessageAction")
	setMsg(uma, "user_message", userMessage)

	// --- ConversationAction ---
	ca := newMsg("ConversationAction")
	setMsg(ca, "user_message_action", uma)

	// --- ModelDetails ---
	md := newMsg("ModelDetails")
	setStr(md, "model_id", p.ModelId)
	setStr(md, "display_model_id", p.ModelId)
	setStr(md, "display_name", p.ModelId)

	// --- AgentRunRequest ---
	arr := newMsg("AgentRunRequest")
	setMsg(arr, "conversation_state", css)
	setMsg(arr, "action", ca)
	setMsg(arr, "model_details", md)
	setStr(arr, "conversation_id", p.ConversationId)

	// McpTools
	if len(p.McpTools) > 0 {
		mcpTools := newMsg("McpTools")
		toolsField := field(mcpTools, "mcp_tools")
		toolsList := mcpTools.Mutable(toolsField).List()
		for _, tool := range p.McpTools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
		setMsg(arr, "mcp_tools", mcpTools)
	}

	// --- AgentClientMessage ---
	acm := newMsg("AgentClientMessage")
	setMsg(acm, "run_request", arr)

	return marshal(acm)
}

// encodeRunRequestWithCheckpoint builds an AgentClientMessage using a raw checkpoint
// as conversation_state. The checkpoint bytes are embedded directly without deserialization.
func encodeRunRequestWithCheckpoint(p *RunRequestParams) []byte {
	// Build UserMessage
	userMessage := newMsg("UserMessage")
	setStr(userMessage, "text", p.UserText)
	setStr(userMessage, "message_id", p.MessageId)
	if len(p.Images) > 0 {
		sc := newMsg("SelectedContext")
		imgsField := field(sc, "selected_images")
		imgsList := sc.Mutable(imgsField).List()
		for _, img := range p.Images {
			si := newMsg("SelectedImage")
			setStr(si, "uuid", generateId())
			setStr(si, "mime_type", img.MimeType)
			setBytes(si, "data", img.Data)
			imgsList.Append(protoreflect.ValueOfMessage(si.ProtoReflect()))
		}
		setMsg(userMessage, "selected_context", sc)
	}

	// Build ConversationAction with UserMessageAction
	uma := newMsg("UserMessageAction")
	setMsg(uma, "user_message", userMessage)
	ca := newMsg("ConversationAction")
	setMsg(ca, "user_message_action", uma)
	caBytes := marshal(ca)

	// Build ModelDetails
	md := newMsg("ModelDetails")
	setStr(md, "model_id", p.ModelId)
	setStr(md, "display_model_id", p.ModelId)
	setStr(md, "display_name", p.ModelId)
	mdBytes := marshal(md)

	// Build McpTools
	var mcpToolsBytes []byte
	if len(p.McpTools) > 0 {
		mcpTools := newMsg("McpTools")
		toolsField := field(mcpTools, "mcp_tools")
		toolsList := mcpTools.Mutable(toolsField).List()
		for _, tool := range p.McpTools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
		mcpToolsBytes = marshal(mcpTools)
	}

	// Manually assemble AgentRunRequest using protowire to embed raw checkpoint
	var arrBuf []byte
	// field 1: conversation_state = raw checkpoint bytes (length-delimited)
	arrBuf = protowire.AppendTag(arrBuf, ARR_ConversationState, protowire.BytesType)
	arrBuf = protowire.AppendBytes(arrBuf, p.RawCheckpoint)
	// field 2: action = ConversationAction
	arrBuf = protowire.AppendTag(arrBuf, ARR_Action, protowire.BytesType)
	arrBuf = protowire.AppendBytes(arrBuf, caBytes)
	// field 3: model_details = ModelDetails
	arrBuf = protowire.AppendTag(arrBuf, ARR_ModelDetails, protowire.BytesType)
	arrBuf = protowire.AppendBytes(arrBuf, mdBytes)
	// field 4: mcp_tools = McpTools
	if len(mcpToolsBytes) > 0 {
		arrBuf = protowire.AppendTag(arrBuf, ARR_McpTools, protowire.BytesType)
		arrBuf = protowire.AppendBytes(arrBuf, mcpToolsBytes)
	}
	// field 5: conversation_id = string
	if p.ConversationId != "" {
		arrBuf = protowire.AppendTag(arrBuf, ARR_ConversationId, protowire.BytesType)
		arrBuf = protowire.AppendString(arrBuf, p.ConversationId)
	}

	// Wrap in AgentClientMessage field 1 (run_request)
	var acmBuf []byte
	acmBuf = protowire.AppendTag(acmBuf, ACM_RunRequest, protowire.BytesType)
	acmBuf = protowire.AppendBytes(acmBuf, arrBuf)

	log.Debugf("cursor encode: built RunRequest with checkpoint (%d bytes), total=%d bytes", len(p.RawCheckpoint), len(acmBuf))
	return acmBuf
}

// ResumeRequestParams holds data for a ResumeAction request.
type ResumeRequestParams struct {
	ModelId        string
	ConversationId string
	McpTools       []McpToolDef
}

// EncodeResumeRequest builds an AgentClientMessage with ResumeAction.
// Used to resume a conversation by conversation_id without re-sending full history.
func EncodeResumeRequest(p *ResumeRequestParams) []byte {
	// RequestContext with tools
	rc := newMsg("RequestContext")
	if len(p.McpTools) > 0 {
		toolsField := field(rc, "tools")
		toolsList := rc.Mutable(toolsField).List()
		for _, tool := range p.McpTools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
	}

	// ResumeAction
	ra := newMsg("ResumeAction")
	setMsg(ra, "request_context", rc)

	// ConversationAction with resume_action
	ca := newMsg("ConversationAction")
	setMsg(ca, "resume_action", ra)

	// ModelDetails
	md := newMsg("ModelDetails")
	setStr(md, "model_id", p.ModelId)
	setStr(md, "display_model_id", p.ModelId)
	setStr(md, "display_name", p.ModelId)

	// AgentRunRequest — no conversation_state needed for resume
	arr := newMsg("AgentRunRequest")
	setMsg(arr, "action", ca)
	setMsg(arr, "model_details", md)
	setStr(arr, "conversation_id", p.ConversationId)

	// McpTools at top level
	if len(p.McpTools) > 0 {
		mcpTools := newMsg("McpTools")
		toolsField := field(mcpTools, "mcp_tools")
		toolsList := mcpTools.Mutable(toolsField).List()
		for _, tool := range p.McpTools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
		setMsg(arr, "mcp_tools", mcpTools)
	}

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "run_request", arr)
	return marshal(acm)
}

// --- KV response encoders ---
// Mirrors handleKvMessage() in cursor-fetch.ts

// EncodeKvGetBlobResult responds to a getBlobArgs request.
func EncodeKvGetBlobResult(kvId uint32, blobData []byte) []byte {
	result := newMsg("GetBlobResult")
	if blobData != nil {
		setBytes(result, "blob_data", blobData)
	}

	kvc := newMsg("KvClientMessage")
	setUint32(kvc, "id", kvId)
	setMsg(kvc, "get_blob_result", result)

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "kv_client_message", kvc)
	return marshal(acm)
}

// EncodeKvSetBlobResult responds to a setBlobArgs request.
func EncodeKvSetBlobResult(kvId uint32) []byte {
	result := newMsg("SetBlobResult")

	kvc := newMsg("KvClientMessage")
	setUint32(kvc, "id", kvId)
	setMsg(kvc, "set_blob_result", result)

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "kv_client_message", kvc)
	return marshal(acm)
}

// --- Exec response encoders ---
// Mirrors handleExecMessage() and sendExec() in cursor-fetch.ts

// EncodeExecRequestContextResult responds to requestContextArgs with tool definitions.
func EncodeExecRequestContextResult(execMsgId uint32, execId string, tools []McpToolDef) []byte {
	// RequestContext with tools
	rc := newMsg("RequestContext")
	if len(tools) > 0 {
		toolsField := field(rc, "tools")
		toolsList := rc.Mutable(toolsField).List()
		for _, tool := range tools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
	}

	// RequestContextSuccess
	rcs := newMsg("RequestContextSuccess")
	setMsg(rcs, "request_context", rc)

	// RequestContextResult (oneof success)
	rcr := newMsg("RequestContextResult")
	setMsg(rcr, "success", rcs)

	return encodeExecClientMsg(execMsgId, execId, "request_context_result", rcr)
}

// EncodeExecMcpResult responds with MCP tool result.
func EncodeExecMcpResult(execMsgId uint32, execId string, content string, isError bool) []byte {
	textContent := newMsg("McpTextContent")
	setStr(textContent, "text", content)

	contentItem := newMsg("McpToolResultContentItem")
	setMsg(contentItem, "text", textContent)

	success := newMsg("McpSuccess")
	contentField := field(success, "content")
	contentList := success.Mutable(contentField).List()
	contentList.Append(protoreflect.ValueOfMessage(contentItem.ProtoReflect()))
	setBool(success, "is_error", isError)

	result := newMsg("McpResult")
	setMsg(result, "success", success)

	return encodeExecClientMsg(execMsgId, execId, "mcp_result", result)
}

// EncodeExecMcpError responds with MCP error.
func EncodeExecMcpError(execMsgId uint32, execId string, errMsg string) []byte {
	mcpErr := newMsg("McpError")
	setStr(mcpErr, "error", errMsg)

	result := newMsg("McpResult")
	setMsg(result, "error", mcpErr)

	return encodeExecClientMsg(execMsgId, execId, "mcp_result", result)
}

// --- Rejection encoders (mirror handleExecMessage rejections) ---

func EncodeExecReadRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("ReadRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("ReadResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "read_result", result)
}

func EncodeExecShellRejected(execMsgId uint32, execId string, command, workDir, reason string) []byte {
	rej := newMsg("ShellRejected")
	setStr(rej, "command", command)
	setStr(rej, "working_directory", workDir)
	setStr(rej, "reason", reason)
	result := newMsg("ShellResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "shell_result", result)
}

func EncodeExecWriteRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("WriteRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("WriteResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "write_result", result)
}

func EncodeExecDeleteRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("DeleteRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("DeleteResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "delete_result", result)
}

func EncodeExecLsRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("LsRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("LsResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "ls_result", result)
}

func EncodeExecGrepError(execMsgId uint32, execId string, errMsg string) []byte {
	grepErr := newMsg("GrepError")
	setStr(grepErr, "error", errMsg)
	result := newMsg("GrepResult")
	setMsg(result, "error", grepErr)
	return encodeExecClientMsg(execMsgId, execId, "grep_result", result)
}

func EncodeExecFetchError(execMsgId uint32, execId string, url, errMsg string) []byte {
	fetchErr := newMsg("FetchError")
	setStr(fetchErr, "url", url)
	setStr(fetchErr, "error", errMsg)
	result := newMsg("FetchResult")
	setMsg(result, "error", fetchErr)
	return encodeExecClientMsg(execMsgId, execId, "fetch_result", result)
}

func EncodeExecDiagnosticsResult(execMsgId uint32, execId string) []byte {
	result := newMsg("DiagnosticsResult")
	return encodeExecClientMsg(execMsgId, execId, "diagnostics_result", result)
}

func EncodeExecBackgroundShellSpawnRejected(execMsgId uint32, execId string, command, workDir, reason string) []byte {
	rej := newMsg("ShellRejected")
	setStr(rej, "command", command)
	setStr(rej, "working_directory", workDir)
	setStr(rej, "reason", reason)
	result := newMsg("BackgroundShellSpawnResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "background_shell_spawn_result", result)
}

func EncodeExecWriteShellStdinError(execMsgId uint32, execId string, errMsg string) []byte {
	wsErr := newMsg("WriteShellStdinError")
	setStr(wsErr, "error", errMsg)
	result := newMsg("WriteShellStdinResult")
	setMsg(result, "error", wsErr)
	return encodeExecClientMsg(execMsgId, execId, "write_shell_stdin_result", result)
}

// encodeExecClientMsg wraps an exec result in AgentClientMessage.
// Mirrors sendExec() in cursor-fetch.ts.
func encodeExecClientMsg(id uint32, execId string, resultFieldName string, resultMsg *dynamicpb.Message) []byte {
	ecm := newMsg("ExecClientMessage")
	setUint32(ecm, "id", id)
	// Force set exec_id even if empty - Cursor requires this field to be set
	ecm.Set(field(ecm, "exec_id"), protoreflect.ValueOfString(execId))

	// Debug: check if field exists
	fd := field(ecm, resultFieldName)
	if fd == nil {
		panic(fmt.Sprintf("field %q NOT FOUND in ExecClientMessage! Available fields: %v", resultFieldName, listFields(ecm)))
	}

	// Debug: log the actual field being set
	log.Debugf("encodeExecClientMsg: setting field %q (number=%d, kind=%s)", fd.Name(), fd.Number(), fd.Kind())

	ecm.Set(fd, protoreflect.ValueOfMessage(resultMsg.ProtoReflect()))

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "exec_client_message", ecm)
	return marshal(acm)
}

func listFields(msg *dynamicpb.Message) []string {
	var names []string
	for i := 0; i < msg.Descriptor().Fields().Len(); i++ {
		names = append(names, string(msg.Descriptor().Fields().Get(i).Name()))
	}
	return names
}

// --- Utilities ---

// jsonToProtobufValueBytes converts a JSON schema (json.RawMessage) to protobuf Value binary.
// This mirrors the TS pattern: toBinary(ValueSchema, fromJson(ValueSchema, jsonSchema))
func jsonToProtobufValueBytes(jsonData json.RawMessage) []byte {
	if len(jsonData) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(jsonData, &v); err != nil {
		return jsonData // fallback to raw JSON if parsing fails
	}
	pbVal, err := structpb.NewValue(v)
	if err != nil {
		return jsonData // fallback
	}
	b, err := proto.Marshal(pbVal)
	if err != nil {
		return jsonData // fallback
	}
	return b
}

// ProtobufValueBytesToJSON converts protobuf Value binary back to JSON.
// This mirrors the TS pattern: toJson(ValueSchema, fromBinary(ValueSchema, value))
func ProtobufValueBytesToJSON(data []byte) (interface{}, error) {
	val := &structpb.Value{}
	if err := proto.Unmarshal(data, val); err != nil {
		return nil, err
	}
	return val.AsInterface(), nil
}

func sha256Sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

var idCounter uint64

func generateId() string {
	idCounter++
	h := sha256.Sum256([]byte{byte(idCounter), byte(idCounter >> 8), byte(idCounter >> 16)})
	return hex.EncodeToString(h[:16])
}
