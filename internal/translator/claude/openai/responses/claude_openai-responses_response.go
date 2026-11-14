package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/MoeForge/moego/ojson"
	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

type claudeToResponsesState struct {
	translatorcommon.ApplyPatchErrorState
	ApplyPatchCalls map[string]*translatorcommon.ApplyPatchCallState
	// ToolSearch is upstream.claude.tool-search as pinned for this response.
	ToolSearch bool
	// RequestEcho holds the request fields echoed into the completed
	// response, extracted once in the constructor so the request body
	// itself is neither retained nor re-scanned per chunk.
	RequestEcho             responsesRequestEcho
	ToolWinners             map[string]responsesToolDescriptor
	ToolNames               claudeToolNames
	FuncItemAdded           map[int]bool
	FuncArgsSent            map[int]int
	FuncBlockStopped        map[int]bool
	FuncInputSnapshot       map[int]string
	FuncInputSnapshotErrors map[int]error
	FuncIdentityConflicts   map[int]bool
	CompletedEmitted        bool
	Seq                     int
	ResponseID              string
	CreatedAt               int64
	NextOutputIndex         int
	CurrentMsgID            string
	CurrentFCID             string
	InTextBlock             bool
	InFuncBlock             bool
	MessageOpen             bool
	ContentPartOpen         bool
	MessageOutputIndex      int
	FuncArgsBuf             map[int]*strings.Builder // index -> args
	FuncArgsDone            map[int]bool
	FuncItemDone            map[int]bool
	FuncItemStatus          map[int]string
	// function call bookkeeping for output aggregation
	FuncNames         map[int]string // Claude block index -> function name
	FuncCallIDs       map[int]string // Claude block index -> call id
	FuncCustom        map[int]bool   // Claude block index -> freeform custom tool
	FuncOutputIndices map[int]int    // Claude block index -> Responses output index
	// message text aggregation
	TextBuf            strings.Builder
	CurrentTextBuf     strings.Builder
	MessageAnnotations []any
	MessageItems       []claudeResponsesMessageItem
	// reasoning state
	ReasoningActive     bool
	ReasoningDeltasDone bool
	ReasoningItemID     string
	ReasoningBuf        strings.Builder
	ReasoningSignature  string
	ReasoningIndex      int
	ReasoningItems      []claudeResponsesReasoningItem
	// server-side web search state: a Claude server_tool_use block and its
	// web_search_tool_result block fold into one Responses web_search_call item.
	WebSearchByBlock  map[int]*claudeResponsesWebSearchItem
	WebSearchByToolID map[string]*claudeResponsesWebSearchItem
	WebSearchItems    []*claudeResponsesWebSearchItem
	StopReason        string
	FallbackModel     string
	Fallbacks         translatorcommon.ClaudeFallbackChain
	// usage aggregation
	Usage claudeResponsesUsageTokens
}

type claudeResponsesWebSearchItem struct {
	ToolUseID   string
	OutputIndex int
	InputBuf    strings.Builder
	Results     []byte
	Emitted     bool
	Status      string
}

func (item *claudeResponsesWebSearchItem) render() []byte {
	return buildResponsesWebSearchCallItemWithStatus(item.ToolUseID, claudeWebSearchQuery(item.InputBuf.String()), item.Status, item.Results)
}

type claudeResponsesMessageItem struct {
	ID          string
	OutputIndex int
	Text        string
	Annotations []any
	Status      string
}

type claudeResponsesReasoningItem struct {
	ID          string
	OutputIndex int
	Text        string
	Signature   string
	Status      string
}

type claudeResponsesUsageTokens struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	HasUsage                 bool
}

var dataTag = []byte("data:")

// ClaudeResponsesRedactedThinkingPrefix marks a Responses reasoning item whose
// encrypted_content carries an Anthropic redacted_thinking payload instead of a
// thinking signature. Responses has no redacted reasoning item type, and
// Anthropic requires redacted_thinking blocks to be replayed verbatim, so the
// payload rides in encrypted_content behind this marker and is restored on the
// way back. The marker is not a valid signature for any provider, so a foreign
// upstream drops the block instead of replaying an unusable value.
const ClaudeResponsesRedactedThinkingPrefix = "claude-redacted-thinking:"

// claudeReasoningCarrier returns the encrypted_content value for the Responses
// reasoning item that mirrors a Claude thinking or redacted_thinking block.
// Streaming thinking blocks usually announce an empty signature and fill it in
// through signature_delta, so an empty result here is expected and later
// replaced.
func claudeReasoningCarrier(contentBlock ojson.Result) string {
	if contentBlock.Get("type").String() == "redacted_thinking" {
		if data := contentBlock.Get("data"); data.Exists() && data.String() != "" {
			return ClaudeResponsesRedactedThinkingPrefix + data.String()
		}
		return ""
	}
	if signature := contentBlock.Get("signature"); signature.Exists() {
		return signature.String()
	}
	return ""
}

func (u *claudeResponsesUsageTokens) Merge(usage ojson.Result) {
	if !usage.Exists() {
		return
	}
	u.HasUsage = true
	if inputTokens := usage.Get("input_tokens"); inputTokens.Exists() {
		u.InputTokens = inputTokens.Int()
	}
	if outputTokens := usage.Get("output_tokens"); outputTokens.Exists() {
		u.OutputTokens = outputTokens.Int()
	}
	if cacheCreationInputTokens := usage.Get("cache_creation_input_tokens"); cacheCreationInputTokens.Exists() {
		u.CacheCreationInputTokens = cacheCreationInputTokens.Int()
	}
	if cacheReadInputTokens := usage.Get("cache_read_input_tokens"); cacheReadInputTokens.Exists() {
		u.CacheReadInputTokens = cacheReadInputTokens.Int()
	}
}

func (u claudeResponsesUsageTokens) OpenAIResponsesUsage() (inputTokens, outputTokens, totalTokens, cachedTokens int64) {
	cachedTokens = u.CacheReadInputTokens
	inputTokens = u.InputTokens + u.CacheCreationInputTokens + cachedTokens
	outputTokens = u.OutputTokens
	totalTokens = inputTokens + outputTokens
	return inputTokens, outputTokens, totalTokens, cachedTokens
}

func claudeResponsesIncompleteDetails(stopReason string) ([]byte, bool) {
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "max_tokens", "model_context_window_exceeded":
		return []byte(`{"reason":"max_output_tokens"}`), true
	case "refusal":
		// Safety classifiers stop the turn with a normal 200. Responses clients
		// treat content_filter as a retryable refusal instead of a finished answer:
		// https://platform.claude.com/docs/en/build-with-claude/handling-stop-reasons#refusal
		return []byte(`{"reason":"content_filter"}`), true
	}
	return nil, false
}

// claudeStopReasonPauseTurn reports Anthropic's server-tool iteration pause.
// The turn is unfinished but valid: the client continues it by sending the
// paused assistant content back unchanged, so Responses ends the response as
// completed with end_turn=false instead of an error:
// https://platform.claude.com/docs/en/build-with-claude/handling-stop-reasons#pause_turn
func claudeStopReasonPauseTurn(stopReason string) bool {
	return strings.EqualFold(strings.TrimSpace(stopReason), "pause_turn")
}

// claudeResponsesWebSearchStatus keeps a paused or tool_use-deferred search
// that never received its result in_progress, so the next request can send it
// back without a result.
func claudeResponsesWebSearchStatus(stopReason, status string, hasResults bool) string {
	if !hasResults && (claudeStopReasonPauseTurn(stopReason) || strings.EqualFold(strings.TrimSpace(stopReason), "tool_use")) {
		return "in_progress"
	}
	return status
}

func claudeResponsesOutputStatus(stopReason string) string {
	if _, incomplete := claudeResponsesIncompleteDetails(stopReason); incomplete {
		return "incomplete"
	}
	return "completed"
}

func claudeResponsesTerminalState(stopReason string) (eventType, status string, incompleteDetails []byte) {
	incompleteDetails, incomplete := claudeResponsesIncompleteDetails(stopReason)
	if incomplete {
		return "response.incomplete", "incomplete", incompleteDetails
	}
	return "response.completed", "completed", nil
}

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && ojson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && ojson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

// responsesRequestEcho carries the typed values of the Responses request
// fields echoed into response objects. Extraction walks the top level of
// the request once; apply writes the fields in the same fixed order and
// with the same typed conversions as the previous per-field lookups, so
// the emitted JSON is unchanged.
type responsesRequestEcho struct {
	instructions          string
	hasInstructions       bool
	maxOutputTokens       int64
	hasMaxOutputTokens    bool
	maxToolCalls          int64
	hasMaxToolCalls       bool
	model                 string
	hasModel              bool
	parallelToolCalls     bool
	hasParallelToolCalls  bool
	previousResponseID    string
	hasPreviousResponseID bool
	promptCacheKey        string
	hasPromptCacheKey     bool
	reasoning             any
	hasReasoning          bool
	safetyIdentifier      string
	hasSafetyIdentifier   bool
	serviceTier           string
	hasServiceTier        bool
	store                 bool
	hasStore              bool
	temperature           float64
	hasTemperature        bool
	text                  any
	hasText               bool
	toolChoice            any
	hasToolChoice         bool
	tools                 any
	hasTools              bool
	topLogprobs           int64
	hasTopLogprobs        bool
	topP                  float64
	hasTopP               bool
	truncation            string
	hasTruncation         bool
	user                  any
	hasUser               bool
	metadata              any
	hasMetadata           bool
}

func extractResponsesRequestEcho(root ojson.Result) responsesRequestEcho {
	var e responsesRequestEcho
	root.ForEach(func(key, value ojson.Result) bool {
		switch key.String() {
		case "instructions":
			if !e.hasInstructions {
				e.instructions, e.hasInstructions = value.String(), true
			}
		case "max_output_tokens":
			if !e.hasMaxOutputTokens {
				e.maxOutputTokens, e.hasMaxOutputTokens = value.Int(), true
			}
		case "max_tool_calls":
			if !e.hasMaxToolCalls {
				e.maxToolCalls, e.hasMaxToolCalls = value.Int(), true
			}
		case "model":
			if !e.hasModel {
				e.model, e.hasModel = value.String(), true
			}
		case "parallel_tool_calls":
			if !e.hasParallelToolCalls {
				e.parallelToolCalls, e.hasParallelToolCalls = value.Bool(), true
			}
		case "previous_response_id":
			if !e.hasPreviousResponseID {
				e.previousResponseID, e.hasPreviousResponseID = value.String(), true
			}
		case "prompt_cache_key":
			if !e.hasPromptCacheKey {
				e.promptCacheKey, e.hasPromptCacheKey = value.String(), true
			}
		case "reasoning":
			if !e.hasReasoning {
				e.reasoning, e.hasReasoning = value.Value(), true
			}
		case "safety_identifier":
			if !e.hasSafetyIdentifier {
				e.safetyIdentifier, e.hasSafetyIdentifier = value.String(), true
			}
		case "service_tier":
			if !e.hasServiceTier {
				e.serviceTier, e.hasServiceTier = value.String(), true
			}
		case "store":
			if !e.hasStore {
				e.store, e.hasStore = value.Bool(), true
			}
		case "temperature":
			if !e.hasTemperature {
				e.temperature, e.hasTemperature = value.Float(), true
			}
		case "text":
			if !e.hasText {
				e.text, e.hasText = value.Value(), true
			}
		case "tool_choice":
			if !e.hasToolChoice {
				e.toolChoice, e.hasToolChoice = value.Value(), true
			}
		case "tools":
			if !e.hasTools {
				e.tools, e.hasTools = value.Value(), true
			}
		case "top_logprobs":
			if !e.hasTopLogprobs {
				e.topLogprobs, e.hasTopLogprobs = value.Int(), true
			}
		case "top_p":
			if !e.hasTopP {
				e.topP, e.hasTopP = value.Float(), true
			}
		case "truncation":
			if !e.hasTruncation {
				e.truncation, e.hasTruncation = value.String(), true
			}
		case "user":
			if !e.hasUser {
				e.user, e.hasUser = value.Value(), true
			}
		case "metadata":
			if !e.hasMetadata {
				e.metadata, e.hasMetadata = value.Value(), true
			}
		}
		return true
	})
	return e
}

func (e responsesRequestEcho) applyDoc(doc *ojson.Doc, prefix string) {
	if e.hasInstructions {
		_ = doc.Set(prefix+"instructions", e.instructions)
	}
	if e.hasMaxOutputTokens {
		_ = doc.Set(prefix+"max_output_tokens", e.maxOutputTokens)
	}
	if e.hasMaxToolCalls {
		_ = doc.Set(prefix+"max_tool_calls", e.maxToolCalls)
	}
	if e.hasModel {
		_ = doc.Set(prefix+"model", e.model)
	}
	if e.hasParallelToolCalls {
		_ = doc.Set(prefix+"parallel_tool_calls", e.parallelToolCalls)
	}
	if e.hasPreviousResponseID {
		_ = doc.Set(prefix+"previous_response_id", e.previousResponseID)
	}
	if e.hasPromptCacheKey {
		_ = doc.Set(prefix+"prompt_cache_key", e.promptCacheKey)
	}
	if e.hasReasoning {
		_ = doc.Set(prefix+"reasoning", e.reasoning)
	}
	if e.hasSafetyIdentifier {
		_ = doc.Set(prefix+"safety_identifier", e.safetyIdentifier)
	}
	if e.hasServiceTier {
		_ = doc.Set(prefix+"service_tier", e.serviceTier)
	}
	if e.hasStore {
		_ = doc.Set(prefix+"store", e.store)
	}
	if e.hasTemperature {
		_ = doc.Set(prefix+"temperature", e.temperature)
	}
	if e.hasText {
		_ = doc.Set(prefix+"text", e.text)
	}
	if e.hasToolChoice {
		_ = doc.Set(prefix+"tool_choice", e.toolChoice)
	}
	if e.hasTools {
		_ = doc.Set(prefix+"tools", e.tools)
	}
	if e.hasTopLogprobs {
		_ = doc.Set(prefix+"top_logprobs", e.topLogprobs)
	}
	if e.hasTopP {
		_ = doc.Set(prefix+"top_p", e.topP)
	}
	if e.hasTruncation {
		_ = doc.Set(prefix+"truncation", e.truncation)
	}
	if e.hasUser {
		_ = doc.Set(prefix+"user", e.user)
	}
	if e.hasMetadata {
		_ = doc.Set(prefix+"metadata", e.metadata)
	}
}

// applyFunctionCallNamespaceFieldsDoc resolves a function call's (name,
// namespace) from the winners and tool names built once for this stream,
// matching splitResponsesQualifiedFunctionCallFromRequest without
// re-parsing the request on every tool call.
func (st *claudeToResponsesState) applyFunctionCallNamespaceFieldsDoc(doc *ojson.Doc, qualifiedName string, itemPath string) {
	qualifiedName = strings.TrimSpace(qualifiedName)
	name, namespace := qualifiedName, ""
	if qualifiedName != "" {
		identity := st.ToolNames.identity(qualifiedName)
		name = identity
		if descriptor, ok := st.ToolWinners[identity]; ok && !descriptor.direct {
			name, namespace = descriptor.childName, descriptor.namespace
		}
	}
	translatorcommon.SetResponsesToolCallIdentityDoc(doc, name, namespace, itemPath)
}

func emitEvent(event string, payload []byte) []byte {
	return translatorcommon.SSEEventData(event, payload)
}

func noSSEOutput(out [][]byte) [][]byte {
	if out == nil {
		return [][]byte{}
	}
	return out
}

func (st *claudeToResponsesState) appendMessageAnnotation(annotation any) {
	if annotation == nil {
		return
	}
	st.MessageAnnotations = append(st.MessageAnnotations, annotation)
}

func (st *claudeToResponsesState) allocateOutputIndex() int {
	index := st.NextOutputIndex
	st.NextOutputIndex++
	return index
}

func (st *claudeToResponsesState) messageOutputIndex() int {
	if st.MessageOutputIndex < 0 {
		st.MessageOutputIndex = st.allocateOutputIndex()
	}
	return st.MessageOutputIndex
}

func (st *claudeToResponsesState) functionOutputIndex(blockIndex int) int {
	if index, ok := st.FuncOutputIndices[blockIndex]; ok {
		return index
	}
	index := st.allocateOutputIndex()
	st.FuncOutputIndices[blockIndex] = index
	return index
}

// startWebSearch opens the Responses item that stands for a Claude server-side
// search block.
func (st *claudeToResponsesState) startWebSearch(blockIndex int, toolUseID string) *claudeResponsesWebSearchItem {
	item := &claudeResponsesWebSearchItem{ToolUseID: toolUseID, OutputIndex: st.allocateOutputIndex()}
	st.WebSearchByBlock[blockIndex] = item
	st.WebSearchByToolID[toolUseID] = item
	st.WebSearchItems = append(st.WebSearchItems, item)
	return item
}

// finalizeWebSearchWithStatus emits output_item.done once the result block has been seen,
// or at message_stop when the turn ended without one.
func (st *claudeToResponsesState) finalizeWebSearchWithStatus(item *claudeResponsesWebSearchItem, status string, nextSeq func() int) [][]byte {
	if item == nil || item.Emitted {
		return nil
	}
	item.Emitted = true
	item.Status = status
	doneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{}}`)
	_ = doneDoc.Set("sequence_number", nextSeq())
	_ = doneDoc.Set("output_index", item.OutputIndex)
	_ = doneDoc.SetRawBytes("item", item.render())
	return [][]byte{emitEvent("response.output_item.done", doneDoc.Bytes())}
}

// isApplyPatch consults the original request's winning declaration, not the
// sanitized upstream name or a discarded declaration with the same name.
func (st *claudeToResponsesState) isApplyPatch(name string) bool {
	d, ok := st.ToolWinners[st.ToolNames.identity(name)]
	return ok && d.toolType == "custom" && applypatch.IsCustomTool(d.tool)
}

// isToolSearch reports whether a Claude tool name maps to the client
// tool_search declaration.
func (st *claudeToResponsesState) isToolSearch(name string) bool {
	return st.ToolWinners[st.ToolNames.identity(name)].clientSearch
}

// validateApplyPatchSnapshots permits equivalent JSON spellings but never
// silently replaces one complete patch input with another.
func validateApplyPatchSnapshots(previous, current string) error {
	var call translatorcommon.ApplyPatchCallState
	if previous != "" {
		if _, _, errFinishArguments := call.FinishArguments(previous); errFinishArguments != nil {
			return errFinishArguments
		}
	}
	_, _, errFinishArguments := call.FinishArguments(current)
	return errFinishArguments
}

// finishClaudeApplyPatchArguments treats complete streamed JSON as a complete
// snapshot, not a prefix that a later snapshot may silently extend. A partial
// source can still be completed by a consistent full snapshot.
func finishClaudeApplyPatchArguments(call *translatorcommon.ApplyPatchCallState, arguments, snapshot string) (string, string, error) {
	if snapshot == "" {
		return call.FinishArguments(arguments)
	}
	if ojson.Valid(arguments) {
		if _, _, errFinishArguments := call.FinishArguments(arguments); errFinishArguments != nil {
			return "", "", errFinishArguments
		}
	}
	return call.FinishArguments(snapshot)
}

func (st *claudeToResponsesState) failToolInput(err error, nextSeq func() int) [][]byte {
	if st.ToolInputError() != nil {
		return nil
	}
	st.SetToolInputError(err)
	return [][]byte{emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, nextSeq()))}
}

func (st *claudeToResponsesState) emitFuncItem(idx int, force bool, nextSeq func() int) [][]byte {
	if st.FuncItemAdded[idx] || st.ToolInputError() != nil {
		return nil
	}
	name, callID := st.FuncNames[idx], st.FuncCallIDs[idx]
	if force && name == "" && len(st.ToolWinners) == 1 {
		for identity := range st.ToolWinners {
			if st.isApplyPatch(identity) {
				name = st.ToolNames.claudeName(identity)
				st.FuncNames[idx] = name
			}
		}
	}
	if st.isApplyPatch(name) {
		if st.FuncIdentityConflicts[idx] {
			return st.failToolInput(fmt.Errorf("conflicting apply_patch call identity"), nextSeq)
		}
		if errSnapshot := st.FuncInputSnapshotErrors[idx]; errSnapshot != nil {
			return st.failToolInput(errSnapshot, nextSeq)
		}
	}
	if !force && (name == "" || callID == "") {
		return nil
	}
	if callID == "" {
		callID = fmt.Sprintf("call_%s_%d", st.ResponseID, idx)
		st.FuncCallIDs[idx] = callID
	}
	d := st.ToolWinners[st.ToolNames.identity(name)]
	st.FuncCustom[idx] = d.toolType == "custom"
	outputIndex := st.functionOutputIndex(idx)
	if st.isToolSearch(name) {
		st.FuncItemAdded[idx] = true
		return [][]byte{toolSearchCallEvent("response.output_item.added", nextSeq(), outputIndex, buildResponsesToolSearchCallItem(callID, "in_progress", ""))}
	}
	itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`)
	itemID := fmt.Sprintf("fc_%s", callID)
	if st.FuncCustom[idx] {
		itemDoc = ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"in_progress","input":"","call_id":"","name":""}}`)
		itemID = fmt.Sprintf("ctc_%s", callID)
		if st.isApplyPatch(name) {
			localName := d.childName
			if d.direct {
				localName = d.name
			}
			st.ApplyPatchCalls[fmt.Sprint(idx)] = &translatorcommon.ApplyPatchCallState{
				ItemID: itemID, CallID: callID, Name: localName, Namespace: d.namespace, OutputIndex: outputIndex,
			}
		}
	}
	_ = itemDoc.Set("item.id", itemID)
	_ = itemDoc.Set("item.call_id", callID)
	st.applyFunctionCallNamespaceFieldsDoc(itemDoc, name, "item")
	_ = itemDoc.Set("sequence_number", nextSeq())
	_ = itemDoc.Set("output_index", outputIndex)
	st.FuncItemAdded[idx] = true
	return [][]byte{emitEvent("response.output_item.added", itemDoc.Bytes())}
}

func (st *claudeToResponsesState) emitPendingFuncArgs(idx int, nextSeq func() int) [][]byte {
	if !st.FuncItemAdded[idx] || st.ToolInputError() != nil || st.isToolSearch(st.FuncNames[idx]) {
		return nil
	}
	buf := st.FuncArgsBuf[idx]
	if buf == nil || buf.Len() <= st.FuncArgsSent[idx] {
		return nil
	}
	fragment := buf.String()[st.FuncArgsSent[idx]:]
	st.FuncArgsSent[idx] = buf.Len()
	if st.FuncCustom[idx] {
		if patchCall := st.ApplyPatchCalls[fmt.Sprint(idx)]; patchCall != nil {
			delta, errPushArguments := patchCall.PushArguments(fragment)
			if errPushArguments != nil {
				return st.failToolInput(errPushArguments, nextSeq)
			}
			if delta != "" {
				return [][]byte{emitEvent("response.custom_tool_call_input.delta", translatorcommon.ApplyPatchInputDelta(patchCall, delta, nextSeq()))}
			}
		}
		return nil
	}
	msgDoc := ojson.NewDocString(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
	_ = msgDoc.Set("sequence_number", nextSeq())
	_ = msgDoc.Set("item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
	_ = msgDoc.Set("output_index", st.functionOutputIndex(idx))
	_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(msgDoc, "delta", fragment)
	return [][]byte{emitEvent("response.function_call_arguments.delta", msgDoc.Bytes())}
}

func (st *claudeToResponsesState) finalizeFuncItem(idx int, status string, nextSeq func() int) [][]byte {
	if st.FuncItemDone[idx] || st.ToolInputError() != nil {
		return nil
	}
	out := st.emitFuncItem(idx, true, nextSeq)
	out = append(out, st.emitPendingFuncArgs(idx, nextSeq)...)
	if st.ToolInputError() != nil {
		return out
	}
	if st.FuncItemDone == nil {
		st.FuncItemDone = make(map[int]bool)
	}
	st.FuncItemDone[idx] = true
	if st.FuncItemStatus == nil {
		st.FuncItemStatus = make(map[int]string)
	}
	st.FuncItemStatus[idx] = status

	outputIndex := st.functionOutputIndex(idx)
	args := ""
	if buf := st.FuncArgsBuf[idx]; buf != nil {
		if buf.Len() > 0 {
			args = buf.String()
		}
	}
	callID := st.FuncCallIDs[idx]
	if callID == "" {
		callID = st.CurrentFCID
	}
	name := st.FuncNames[idx]

	if st.isToolSearch(name) {
		status = toolSearchCallStatus(status, args)
		st.FuncItemStatus[idx] = status
		out = append(out, toolSearchCallEvent("response.output_item.done", nextSeq(), outputIndex, buildResponsesToolSearchCallItem(callID, status, args)))
		st.InFuncBlock = false
		return out
	}
	if !st.FuncCustom[idx] && args == "" && status == "completed" {
		args = "{}"
	}

	if st.FuncCustom[idx] {
		input := ""
		patchCall := st.ApplyPatchCalls[fmt.Sprint(idx)]
		if patchCall != nil {
			tail, fullInput, errFinishArguments := finishClaudeApplyPatchArguments(patchCall, args, st.FuncInputSnapshot[idx])
			if errFinishArguments != nil {
				return append(out, st.failToolInput(errFinishArguments, nextSeq)...)
			}
			input = fullInput
			if tail != "" {
				out = append(out, emitEvent("response.custom_tool_call_input.delta", translatorcommon.ApplyPatchInputDelta(patchCall, tail, nextSeq())))
			}
		} else {
			input = unwrapCustomToolInput(args)
		}
		if !st.FuncArgsDone[idx] {
			if st.FuncArgsDone == nil {
				st.FuncArgsDone = make(map[int]bool)
			}
			st.FuncArgsDone[idx] = true
			if patchCall != nil {
				out = append(out, emitEvent("response.custom_tool_call_input.done", translatorcommon.ApplyPatchInputDone(patchCall, input, nextSeq())))
			} else {
				inputDoneDoc := ojson.NewDocString(`{"type":"response.custom_tool_call_input.done","sequence_number":0,"item_id":"","output_index":0,"input":""}`)
				_ = inputDoneDoc.Set("sequence_number", nextSeq())
				_ = inputDoneDoc.Set("item_id", fmt.Sprintf("ctc_%s", callID))
				_ = inputDoneDoc.Set("output_index", outputIndex)
				_ = inputDoneDoc.Set("input", input)
				out = append(out, emitEvent("response.custom_tool_call_input.done", inputDoneDoc.Bytes()))
			}
		}

		itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}}`)
		_ = itemDoneDoc.Set("sequence_number", nextSeq())
		_ = itemDoneDoc.Set("output_index", outputIndex)
		_ = itemDoneDoc.Set("item.id", fmt.Sprintf("ctc_%s", callID))
		_ = itemDoneDoc.Set("item.status", status)
		_ = itemDoneDoc.Set("item.input", input)
		_ = itemDoneDoc.Set("item.call_id", callID)
		st.applyFunctionCallNamespaceFieldsDoc(itemDoneDoc, name, "item")
		out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))
	} else {
		if !st.FuncArgsDone[idx] {
			if st.FuncArgsDone == nil {
				st.FuncArgsDone = make(map[int]bool)
			}
			st.FuncArgsDone[idx] = true
			fcDoneDoc := ojson.NewDocString(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
			_ = fcDoneDoc.Set("sequence_number", nextSeq())
			_ = fcDoneDoc.Set("item_id", fmt.Sprintf("fc_%s", callID))
			_ = fcDoneDoc.Set("output_index", outputIndex)
			_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(fcDoneDoc, "arguments", args)
			out = append(out, emitEvent("response.function_call_arguments.done", fcDoneDoc.Bytes()))
		}

		itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
		_ = itemDoneDoc.Set("sequence_number", nextSeq())
		_ = itemDoneDoc.Set("output_index", outputIndex)
		_ = itemDoneDoc.Set("item.id", fmt.Sprintf("fc_%s", callID))
		_ = itemDoneDoc.Set("item.status", status)
		_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(itemDoneDoc, "item.arguments", args)
		_ = itemDoneDoc.Set("item.call_id", callID)
		st.applyFunctionCallNamespaceFieldsDoc(itemDoneDoc, name, "item")
		out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))
	}
	st.InFuncBlock = false
	return out
}

func (st *claudeToResponsesState) finalizeReasoningDeltas(nextSeq func() int) [][]byte {
	if !st.ReasoningActive || st.ReasoningDeltasDone {
		return nil
	}
	st.ReasoningDeltasDone = true
	full := st.ReasoningBuf.String()
	var out [][]byte
	textDoneDoc := ojson.NewDocString(`{"type":"response.reasoning_summary_text.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"text":""}`)
	_ = textDoneDoc.Set("sequence_number", nextSeq())
	_ = textDoneDoc.Set("item_id", st.ReasoningItemID)
	_ = textDoneDoc.Set("output_index", st.ReasoningIndex)
	_ = textDoneDoc.Set("text", full)
	out = append(out, emitEvent("response.reasoning_summary_text.done", textDoneDoc.Bytes()))
	partDoneDoc := ojson.NewDocString(`{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
	_ = partDoneDoc.Set("sequence_number", nextSeq())
	_ = partDoneDoc.Set("item_id", st.ReasoningItemID)
	_ = partDoneDoc.Set("output_index", st.ReasoningIndex)
	_ = partDoneDoc.Set("part.text", full)
	out = append(out, emitEvent("response.reasoning_summary_part.done", partDoneDoc.Bytes()))
	return out
}

func (st *claudeToResponsesState) finalizeReasoningItem(status string, nextSeq func() int) [][]byte {
	if !st.ReasoningActive && st.ReasoningItemID == "" {
		return nil
	}
	var out [][]byte
	out = append(out, st.finalizeReasoningDeltas(nextSeq)...)

	full := st.ReasoningBuf.String()
	itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"completed","encrypted_content":"","summary":[]}}`)
	_ = itemDoneDoc.Set("sequence_number", nextSeq())
	_ = itemDoneDoc.Set("output_index", st.ReasoningIndex)
	_ = itemDoneDoc.Set("item.id", st.ReasoningItemID)
	_ = itemDoneDoc.Set("item.status", status)
	_ = itemDoneDoc.Set("item.encrypted_content", st.ReasoningSignature)
	summary := []byte(`{"type":"summary_text","text":""}`)
	summary, _ = ojson.SetBytes(summary, "text", full)
	translatorcommon.SetRawArrayItemsDoc(itemDoneDoc, "item.summary", [][]byte{summary})
	out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))
	st.ReasoningItems = append(st.ReasoningItems, claudeResponsesReasoningItem{
		ID:          st.ReasoningItemID,
		OutputIndex: st.ReasoningIndex,
		Text:        full,
		Signature:   st.ReasoningSignature,
		Status:      status,
	})
	st.ReasoningActive = false
	st.ReasoningItemID = ""
	st.ReasoningBuf.Reset()
	st.ReasoningSignature = ""
	st.ReasoningIndex = -1
	return out
}

func (st *claudeToResponsesState) finalizeAssistantMessage(nextSeq func() int) [][]byte {
	if !st.MessageOpen {
		return nil
	}
	fullText := st.TextBuf.String()
	outputIndex := st.messageOutputIndex()
	status := claudeResponsesOutputStatus(st.StopReason)
	var out [][]byte
	doneDoc := ojson.NewDocString(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
	_ = doneDoc.Set("sequence_number", nextSeq())
	_ = doneDoc.Set("item_id", st.CurrentMsgID)
	_ = doneDoc.Set("output_index", outputIndex)
	_ = doneDoc.Set("text", fullText)
	out = append(out, emitEvent("response.output_text.done", doneDoc.Bytes()))

	partDoneDoc := ojson.NewDocString(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
	_ = partDoneDoc.Set("sequence_number", nextSeq())
	_ = partDoneDoc.Set("item_id", st.CurrentMsgID)
	_ = partDoneDoc.Set("output_index", outputIndex)
	_ = partDoneDoc.Set("part.text", fullText)
	if len(st.MessageAnnotations) > 0 {
		_ = partDoneDoc.Set("part.annotations", st.MessageAnnotations)
	}
	out = append(out, emitEvent("response.content_part.done", partDoneDoc.Bytes()))

	finalDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}}`)
	_ = finalDoc.Set("sequence_number", nextSeq())
	_ = finalDoc.Set("output_index", outputIndex)
	_ = finalDoc.Set("item.id", st.CurrentMsgID)
	_ = finalDoc.Set("item.status", status)
	_ = finalDoc.Set("item.content.0.text", fullText)
	if len(st.MessageAnnotations) > 0 {
		_ = finalDoc.Set("item.content.0.annotations", st.MessageAnnotations)
	}
	out = append(out, emitEvent("response.output_item.done", finalDoc.Bytes()))

	st.MessageItems = append(st.MessageItems, claudeResponsesMessageItem{
		ID:          st.CurrentMsgID,
		OutputIndex: outputIndex,
		Text:        fullText,
		Annotations: append([]any(nil), st.MessageAnnotations...),
		Status:      status,
	})
	st.InTextBlock = false
	st.MessageOpen = false
	st.ContentPartOpen = false
	st.CurrentMsgID = ""
	st.MessageOutputIndex = -1
	st.TextBuf.Reset()
	st.CurrentTextBuf.Reset()
	st.MessageAnnotations = nil
	return out
}

func newClaudeToResponsesState(requestJSON []byte, toolSearch bool) *claudeToResponsesState {
	root := util.ParseGJSONBytesNoCopy(bytes.Clone(requestJSON))
	winners := responsesToolWinners(root, toolSearch)
	return &claudeToResponsesState{
		ToolSearch:              toolSearch,
		RequestEcho:             extractResponsesRequestEcho(root),
		ToolWinners:             winners,
		ToolNames:               buildClaudeToolNamesWithWinners(root, winners, toolSearch),
		ApplyPatchCalls:         make(map[string]*translatorcommon.ApplyPatchCallState),
		FuncItemAdded:           make(map[int]bool),
		FuncArgsSent:            make(map[int]int),
		FuncBlockStopped:        make(map[int]bool),
		FuncInputSnapshot:       make(map[int]string),
		FuncInputSnapshotErrors: make(map[int]error),
		FuncIdentityConflicts:   make(map[int]bool),
		MessageOutputIndex:      -1,
		ReasoningIndex:          -1,
		FuncArgsBuf:             make(map[int]*strings.Builder),
		FuncNames:               make(map[int]string),
		FuncCallIDs:             make(map[int]string),
		FuncCustom:              make(map[int]bool),
		FuncOutputIndices:       make(map[int]int),
		WebSearchByBlock:        make(map[int]*claudeResponsesWebSearchItem),
		WebSearchByToolID:       make(map[string]*claudeResponsesWebSearchItem),
	}
}

// ConvertClaudeResponseToOpenAIResponses converts Claude SSE to OpenAI Responses SSE events.
func ConvertClaudeResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = newClaudeToResponsesState(pickRequestJSON(originalRequestRawJSON, requestRawJSON), ClientToolSearchEnabledFor(ctx))
	}
	st := (*param).(*claudeToResponsesState)
	if st.ToolInputError() != nil || st.CompletedEmitted {
		return nil
	}

	// Expect `data: {..}` from Claude clients
	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	ev := root.Get("type").String()
	var out [][]byte

	nextSeq := func() int { st.Seq++; return st.Seq }

	switch ev {
	case "message_start":
		if msg := root.Get("message"); msg.Exists() {
			st.ResponseID = strings.Clone(msg.Get("id").String())
			st.CreatedAt = time.Now().Unix()
			// Reset per-message aggregation state
			st.TextBuf.Reset()
			st.CurrentTextBuf.Reset()
			st.MessageAnnotations = nil
			st.MessageItems = nil
			st.ReasoningBuf.Reset()
			st.ReasoningActive = false
			st.ReasoningDeltasDone = false
			st.NextOutputIndex = 0
			st.InTextBlock = false
			st.InFuncBlock = false
			st.MessageOpen = false
			st.ContentPartOpen = false
			st.CurrentMsgID = ""
			st.CurrentFCID = ""
			st.MessageOutputIndex = -1
			st.ReasoningItemID = ""
			st.ReasoningSignature = ""
			st.ReasoningIndex = -1
			st.ReasoningItems = nil
			st.StopReason = ""
			st.FallbackModel = ""
			st.Fallbacks.Reset()
			st.ApplyPatchCalls = make(map[string]*translatorcommon.ApplyPatchCallState)
			st.FuncItemAdded = make(map[int]bool)
			st.FuncArgsSent = make(map[int]int)
			st.FuncBlockStopped = make(map[int]bool)
			st.FuncInputSnapshot = make(map[int]string)
			st.FuncInputSnapshotErrors = make(map[int]error)
			st.FuncIdentityConflicts = make(map[int]bool)
			st.FuncArgsBuf = make(map[int]*strings.Builder)
			st.FuncArgsDone = make(map[int]bool)
			st.FuncItemDone = make(map[int]bool)
			st.FuncItemStatus = make(map[int]string)
			st.FuncNames = make(map[int]string)
			st.FuncCallIDs = make(map[int]string)
			st.FuncCustom = make(map[int]bool)
			st.FuncOutputIndices = make(map[int]int)
			st.Usage = claudeResponsesUsageTokens{}
			st.Usage.Merge(msg.Get("usage"))
			// response.created
			createdDoc := ojson.NewDocString(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
			_ = createdDoc.Set("sequence_number", nextSeq())
			_ = createdDoc.Set("response.id", st.ResponseID)
			_ = createdDoc.Set("response.created_at", st.CreatedAt)
			requestModelName := translatorcommon.RequestModelName(originalRequestRawJSON, requestRawJSON)
			if requestModelName == "" {
				requestModelName = modelName
			}
			upstreamModel := strings.TrimSpace(msg.Get("model").String())
			if fromModel, toModel, ok := translatorcommon.ClaudePreTokenFallbackModels(originalRequestRawJSON, requestRawJSON, requestModelName, upstreamModel); ok {
				st.FallbackModel = toModel
				st.Fallbacks.RecordPreToken(fromModel, toModel, "", nil)
				requestModelName = toModel
			}
			if requestModelName != "" {
				_ = createdDoc.Set("response.model", requestModelName)
			}
			if latestFB := st.Fallbacks.Latest(); len(latestFB) > 0 {
				_ = createdDoc.SetRawBytes("response.fallback", latestFB)
				_ = createdDoc.SetRawBytes("response.fallbacks", st.Fallbacks.ArrayBytes())
			}
			out = append(out, emitEvent("response.created", createdDoc.Bytes()))
			// response.in_progress
			inprogDoc := ojson.NewDocString(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","output":[]}}`)
			_ = inprogDoc.Set("sequence_number", nextSeq())
			_ = inprogDoc.Set("response.id", st.ResponseID)
			_ = inprogDoc.Set("response.created_at", st.CreatedAt)
			if requestModelName != "" {
				_ = inprogDoc.Set("response.model", requestModelName)
			}
			if latestFB := st.Fallbacks.Latest(); len(latestFB) > 0 {
				_ = inprogDoc.SetRawBytes("response.fallback", latestFB)
				_ = inprogDoc.SetRawBytes("response.fallbacks", st.Fallbacks.ArrayBytes())
			}
			out = append(out, emitEvent("response.in_progress", inprogDoc.Bytes()))
		}
	case "content_block_start":
		cb := root.Get("content_block")
		if !cb.Exists() {
			return noSSEOutput(out)
		}
		idx := int(root.Get("index").Int())
		typ := cb.Get("type").String()

		// Keep adjacent text blocks in the same assistant message.
		if typ != "text" {
			out = append(out, st.finalizeAssistantMessage(nextSeq)...)
		}
		// Finalize previous reasoning item
		if st.ReasoningActive || st.ReasoningItemID != "" {
			out = append(out, st.finalizeReasoningItem("completed", nextSeq)...)
		}
		// Finalize any previous completed function calls
		for prevIdx := range st.FuncCallIDs {
			if !st.FuncItemDone[prevIdx] && prevIdx != idx {
				// Patch calls may interleave; a new block is not a completion
				// snapshot for a still-open (or not-yet-named) call.
				if (st.isApplyPatch(st.FuncNames[prevIdx]) || st.FuncNames[prevIdx] == "") && !st.FuncBlockStopped[prevIdx] {
					continue
				}
				out = append(out, st.finalizeFuncItem(prevIdx, "completed", nextSeq)...)
				if st.ToolInputError() != nil {
					return out
				}
			}
		}
		// Finalize any previous web search items
		for _, item := range st.WebSearchItems {
			if !item.Emitted && item.Results != nil {
				out = append(out, st.finalizeWebSearchWithStatus(item, "completed", nextSeq)...)
			}
		}

		if typ == "fallback" {
			toModel := strings.TrimSpace(cb.Get("to.model").String())
			fromModel := strings.TrimSpace(cb.Get("from.model").String())
			if toModel != "" {
				st.FallbackModel = strings.Clone(toModel)
			}
			defaultFrom := fromModel
			if defaultFrom == "" {
				defaultFrom = st.RequestEcho.model
			}
			if defaultFrom == "" {
				defaultFrom = modelName
			}
			boundaryIdx := st.NextOutputIndex
			if fbBytes := st.Fallbacks.RecordBlock(cb, defaultFrom, st.FallbackModel, "output_index", &boundaryIdx); len(fbBytes) > 0 {
				inprogDoc := ojson.NewDocString(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","output":[]}}`)
				_ = inprogDoc.Set("sequence_number", nextSeq())
				_ = inprogDoc.Set("response.id", st.ResponseID)
				_ = inprogDoc.Set("response.created_at", st.CreatedAt)
				if st.FallbackModel != "" {
					_ = inprogDoc.Set("response.model", st.FallbackModel)
				}
				_ = inprogDoc.SetRawBytes("response.fallback", fbBytes)
				_ = inprogDoc.SetRawBytes("response.fallbacks", st.Fallbacks.ArrayBytes())
				out = append(out, emitEvent("response.in_progress", inprogDoc.Bytes()))
			}
			return noSSEOutput(out)
		}

		if typ == "text" {
			st.InTextBlock = true
			outputIndex := st.messageOutputIndex()
			if st.CurrentMsgID == "" {
				st.CurrentMsgID = fmt.Sprintf("msg_%s_%d", st.ResponseID, len(st.MessageItems))
			}
			if !st.MessageOpen {
				itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`)
				_ = itemDoc.Set("sequence_number", nextSeq())
				_ = itemDoc.Set("output_index", outputIndex)
				_ = itemDoc.Set("item.id", st.CurrentMsgID)
				out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))
				st.MessageOpen = true
			}
			if !st.ContentPartOpen {
				partDoc := ojson.NewDocString(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
				_ = partDoc.Set("sequence_number", nextSeq())
				_ = partDoc.Set("item_id", st.CurrentMsgID)
				_ = partDoc.Set("output_index", outputIndex)
				out = append(out, emitEvent("response.content_part.added", partDoc.Bytes()))
				st.ContentPartOpen = true
			}
		} else if typ == "tool_use" {
			st.InFuncBlock = true
			callID, name := strings.Clone(cb.Get("id").String()), strings.Clone(cb.Get("name").String())
			oldID, oldName := st.FuncCallIDs[idx], st.FuncNames[idx]
			// Pending identity evidence must survive later matching updates.
			if callID != "" && oldID != "" && callID != oldID {
				st.FuncIdentityConflicts[idx] = true
			}
			if st.isApplyPatch(oldName) || st.isApplyPatch(name) {
				if st.FuncIdentityConflicts[idx] || (name != "" && oldName != "" && st.ToolNames.identity(name) != st.ToolNames.identity(oldName)) {
					return append(out, st.failToolInput(fmt.Errorf("conflicting apply_patch call identity"), nextSeq)...)
				}
			}
			if !st.FuncItemAdded[idx] {
				// Keep the block key even before its ID arrives so terminal
				// validation cannot overlook an unnamed or ID-less call.
				if callID != "" || oldID == "" {
					st.FuncCallIDs[idx] = callID
				}
			}
			if name != "" && !st.FuncItemAdded[idx] {
				st.FuncNames[idx] = name
			}
			st.CurrentFCID = st.FuncCallIDs[idx]
			st.functionOutputIndex(idx)
			if st.FuncArgsBuf[idx] == nil {
				st.FuncArgsBuf[idx] = &strings.Builder{}
			}
			// Empty start input is a Claude placeholder, not an arguments fragment.
			// Retain populated snapshot evidence without fabricating progress.
			if input := cb.Get("input"); input.Exists() && (!input.IsObject() || len(input.Map()) > 0) {
				// Validate without emitting or finishing the live decoder; defer
				// failures until classification selects the patch bridge.
				if errValidateApplyPatchSnapshots := validateApplyPatchSnapshots(st.FuncInputSnapshot[idx], input.Raw); errValidateApplyPatchSnapshots != nil && st.FuncInputSnapshotErrors[idx] == nil {
					st.FuncInputSnapshotErrors[idx] = errValidateApplyPatchSnapshots
				}
				// Item completion does not seal the response. Compare late snapshots
				// against this call's finished decoder without emitting more input.
				if st.FuncItemDone[idx] {
					if patchCall := st.ApplyPatchCalls[fmt.Sprint(idx)]; patchCall != nil {
						if _, _, errFinishArguments := patchCall.FinishArguments(input.Raw); errFinishArguments != nil {
							return append(out, st.failToolInput(errFinishArguments, nextSeq)...)
						}
					}
				}
				st.FuncInputSnapshot[idx] = strings.Clone(input.Raw)
			}
			if st.isApplyPatch(st.FuncNames[idx]) {
				if errSnapshot := st.FuncInputSnapshotErrors[idx]; errSnapshot != nil {
					return append(out, st.failToolInput(errSnapshot, nextSeq)...)
				}
			}
			out = append(out, st.emitFuncItem(idx, false, nextSeq)...)
			out = append(out, st.emitPendingFuncArgs(idx, nextSeq)...)
		} else if typ == "server_tool_use" {
			if name := cb.Get("name").String(); name != claudeWebSearchToolName {
				// Reachability guard: only web_search can be enabled on Claude by
				// this translator, so anything else means a new upstream tool.
				log.Debugf("claude->responses: unmapped server_tool_use %q at block %d", name, idx)
			} else {
				item := st.startWebSearch(idx, strings.Clone(cb.Get("id").String()))
				addedDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"web_search_call","status":"in_progress","action":{"type":"search","query":""}}}`)
				_ = addedDoc.Set("sequence_number", nextSeq())
				_ = addedDoc.Set("output_index", item.OutputIndex)
				_ = addedDoc.Set("item.id", responsesWebSearchCallID(item.ToolUseID))
				out = append(out, emitEvent("response.output_item.added", addedDoc.Bytes()))
			}
		} else if typ == "web_search_tool_result" {
			// A result block carries its full content up front and has no deltas.
			// Results are stored here and the item is closed when the next block
			// starts or at message_stop so the final stop_reason is respected.
			toolUseID := strings.Clone(cb.Get("tool_use_id").String())
			if item := st.WebSearchByToolID[toolUseID]; item != nil {
				item.Results = claudeWebSearchResultsToResponses(cb.Get("content"))
			} else if toolUseID != "" {
				// A continuation after pause_turn delivers the result of a search
				// whose server_tool_use arrived in the previous response. Surface it
				// under the same id so the client keeps the result in its history.
				item := st.startWebSearch(idx, toolUseID)
				item.Results = claudeWebSearchResultsToResponses(cb.Get("content"))
				addedDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"web_search_call","status":"in_progress","action":{"type":"search","query":""}}}`)
				_ = addedDoc.Set("sequence_number", nextSeq())
				_ = addedDoc.Set("output_index", item.OutputIndex)
				_ = addedDoc.Set("item.id", responsesWebSearchCallID(item.ToolUseID))
				out = append(out, emitEvent("response.output_item.added", addedDoc.Bytes()))
			} else {
				log.Debugf("claude->responses: web_search_tool_result without matching server_tool_use at block %d", idx)
			}
		} else if typ == "thinking" || typ == "redacted_thinking" {
			// start reasoning item
			st.ReasoningActive = true
			st.ReasoningDeltasDone = false
			st.ReasoningIndex = st.allocateOutputIndex()
			st.ReasoningBuf.Reset()
			st.ReasoningSignature = strings.Clone(claudeReasoningCarrier(cb))
			st.ReasoningItemID = fmt.Sprintf("rs_%s_%d", st.ResponseID, idx)
			itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","encrypted_content":"","summary":[]}}`)
			_ = itemDoc.Set("sequence_number", nextSeq())
			_ = itemDoc.Set("output_index", st.ReasoningIndex)
			_ = itemDoc.Set("item.id", st.ReasoningItemID)
			_ = itemDoc.Set("item.encrypted_content", st.ReasoningSignature)
			out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))
			// add a summary part placeholder
			partDoc := ojson.NewDocString(`{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
			_ = partDoc.Set("sequence_number", nextSeq())
			_ = partDoc.Set("item_id", st.ReasoningItemID)
			_ = partDoc.Set("output_index", st.ReasoningIndex)
			out = append(out, emitEvent("response.reasoning_summary_part.added", partDoc.Bytes()))
		}
	case "content_block_delta":
		d := root.Get("delta")
		if !d.Exists() {
			return noSSEOutput(out)
		}
		dt := d.Get("type").String()
		if dt == "text_delta" {
			if t := d.Get("text"); t.Exists() {
				msgDoc := ojson.NewDocString(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
				_ = msgDoc.Set("sequence_number", nextSeq())
				_ = msgDoc.Set("item_id", st.CurrentMsgID)
				_ = msgDoc.Set("output_index", st.messageOutputIndex())
				_ = msgDoc.Set("delta", t.String())
				out = append(out, emitEvent("response.output_text.delta", msgDoc.Bytes()))
				// aggregate text for response.output
				st.TextBuf.WriteString(t.String())
				st.CurrentTextBuf.WriteString(t.String())
			}
		} else if dt == "input_json_delta" {
			if item := st.WebSearchByBlock[int(root.Get("index").Int())]; item != nil {
				if pj := d.Get("partial_json"); pj.Exists() {
					item.InputBuf.WriteString(pj.String())
				}
				return [][]byte{}
			}
			idx := int(root.Get("index").Int())
			if pj := d.Get("partial_json"); pj.Exists() {
				if st.FuncArgsBuf[idx] == nil {
					st.FuncArgsBuf[idx] = &strings.Builder{}
				}
				st.FuncArgsBuf[idx].WriteString(pj.String())
				out = append(out, st.emitPendingFuncArgs(idx, nextSeq)...)
			}
		} else if dt == "thinking_delta" {
			if st.ReasoningActive {
				if t := d.Get("thinking"); t.Exists() {
					st.ReasoningBuf.WriteString(t.String())
					msgDoc := ojson.NewDocString(`{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`)
					_ = msgDoc.Set("sequence_number", nextSeq())
					_ = msgDoc.Set("item_id", st.ReasoningItemID)
					_ = msgDoc.Set("output_index", st.ReasoningIndex)
					_ = msgDoc.Set("delta", t.String())
					out = append(out, emitEvent("response.reasoning_summary_text.delta", msgDoc.Bytes()))
				}
			}
		} else if dt == "signature_delta" {
			if st.ReasoningActive {
				if signature := d.Get("signature"); signature.Exists() && signature.String() != "" {
					st.ReasoningSignature = strings.Clone(signature.String())
				}
			}
			return [][]byte{}
		} else if dt == "citations_delta" {
			if citation := d.Get("citation"); citation.Exists() {
				st.appendMessageAnnotation(ojson.Parse(strings.Clone(citation.Raw)).Value())
			}
			return [][]byte{}
		}
	case "content_block_stop":
		st.FuncBlockStopped[int(root.Get("index").Int())] = true
		if st.InTextBlock {
			st.InTextBlock = false
		} else if st.InFuncBlock {
			st.InFuncBlock = false
		} else if st.ReasoningActive {
			out = append(out, st.finalizeReasoningDeltas(nextSeq)...)
		}
		return noSSEOutput(out)
	case "message_delta":
		usage := root.Get("usage")
		st.Usage.Merge(usage)
		if stopReason := root.Get("delta.stop_reason"); stopReason.Exists() {
			st.StopReason = strings.Clone(stopReason.String())
		}
		defaultFrom := st.RequestEcho.model
		if defaultFrom == "" {
			defaultFrom = modelName
		}
		if iterModel, newHops := st.Fallbacks.RecordIterations(usage.Get("iterations"), defaultFrom, "", nil); iterModel != "" {
			st.FallbackModel = iterModel
			if len(newHops) > 0 {
				inprogDoc := ojson.NewDocString(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","output":[]}}`)
				_ = inprogDoc.Set("sequence_number", nextSeq())
				_ = inprogDoc.Set("response.id", st.ResponseID)
				_ = inprogDoc.Set("response.created_at", st.CreatedAt)
				_ = inprogDoc.Set("response.model", st.FallbackModel)
				_ = inprogDoc.SetRawBytes("response.fallback", st.Fallbacks.Latest())
				_ = inprogDoc.SetRawBytes("response.fallbacks", st.Fallbacks.ArrayBytes())
				return [][]byte{emitEvent("response.in_progress", inprogDoc.Bytes())}
			}
		}
		return [][]byte{}
	case "message_stop":
		toolStatus := claudeResponsesOutputStatus(st.StopReason)
		if st.ReasoningActive || st.ReasoningItemID != "" {
			out = append(out, st.finalizeReasoningItem(toolStatus, nextSeq)...)
		}
		out = append(out, st.finalizeAssistantMessage(nextSeq)...)
		for idx := range st.FuncCallIDs {
			if !st.FuncItemDone[idx] {
				out = append(out, st.finalizeFuncItem(idx, toolStatus, nextSeq)...)
				if st.ToolInputError() != nil {
					return out
				}
			}
		}
		for _, item := range st.WebSearchItems {
			if !item.Emitted {
				out = append(out, st.finalizeWebSearchWithStatus(item, claudeResponsesWebSearchStatus(st.StopReason, toolStatus, len(item.Results) > 0), nextSeq)...)
			}
		}

		eventType, responseStatus, incompleteDetails := claudeResponsesTerminalState(st.StopReason)
		completedDoc := ojson.NewDocString(`{"type":"","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"","background":false,"error":null}}`)
		_ = completedDoc.Set("type", eventType)
		_ = completedDoc.Set("sequence_number", nextSeq())
		_ = completedDoc.Set("response.id", st.ResponseID)
		_ = completedDoc.Set("response.created_at", st.CreatedAt)
		_ = completedDoc.Set("response.status", responseStatus)
		if len(incompleteDetails) > 0 {
			_ = completedDoc.SetRawBytes("response.incomplete_details", incompleteDetails)
		}
		if claudeStopReasonPauseTurn(st.StopReason) {
			_ = completedDoc.Set("response.end_turn", false)
		}
		// Inject original request fields into response as per docs/response.completed.json
		st.RequestEcho.applyDoc(completedDoc, "response.")
		if st.FallbackModel != "" {
			_ = completedDoc.Set("response.model", st.FallbackModel)
		}
		if latestFB := st.Fallbacks.Latest(); len(latestFB) > 0 {
			_ = completedDoc.SetRawBytes("response.fallback", latestFB)
			_ = completedDoc.SetRawBytes("response.fallbacks", st.Fallbacks.ArrayBytes())
		}

		// Build response.output from aggregated state
		outputsWrapperDoc := ojson.NewDocString(`{"arr":[]}`)
		hasOutputs := false
		// reasoning items
		for _, reasoning := range st.ReasoningItems {
			status := reasoning.Status
			if status == "" {
				status = "completed"
			}
			itemDoc := ojson.NewDocString(`{"id":"","type":"reasoning","status":"completed","encrypted_content":"","summary":[]}`)
			_ = itemDoc.Set("id", reasoning.ID)
			_ = itemDoc.Set("status", status)
			_ = itemDoc.Set("encrypted_content", reasoning.Signature)
			summary := []byte(`{"type":"summary_text","text":""}`)
			summary, _ = ojson.SetBytes(summary, "text", reasoning.Text)
			translatorcommon.SetRawArrayItemsDoc(itemDoc, "summary", [][]byte{summary})
			_ = outputsWrapperDoc.SetRawBytes(fmt.Sprintf("arr.%d", reasoning.OutputIndex), itemDoc.Bytes())
			hasOutputs = true
		}
		// assistant message items
		for _, message := range st.MessageItems {
			itemDoc := ojson.NewDocString(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
			_ = itemDoc.Set("id", message.ID)
			_ = itemDoc.Set("status", message.Status)
			_ = itemDoc.Set("content.0.text", message.Text)
			if len(message.Annotations) > 0 {
				_ = itemDoc.Set("content.0.annotations", message.Annotations)
			}
			_ = outputsWrapperDoc.SetRawBytes(fmt.Sprintf("arr.%d", message.OutputIndex), itemDoc.Bytes())
			hasOutputs = true
		}
		// web_search_call items
		for _, item := range st.WebSearchItems {
			status := item.Status
			if status == "" {
				status = "completed"
			}
			item.Status = status
			_ = outputsWrapperDoc.SetRawBytes(fmt.Sprintf("arr.%d", item.OutputIndex), item.render())
			hasOutputs = true
		}
		// function_call items (in ascending index order for determinism)
		if len(st.FuncArgsBuf) > 0 {
			// collect indices
			idxs := make([]int, 0, len(st.FuncArgsBuf))
			for idx := range st.FuncArgsBuf {
				idxs = append(idxs, idx)
			}
			// simple sort (small N), avoid adding new imports
			for i := 0; i < len(idxs); i++ {
				for j := i + 1; j < len(idxs); j++ {
					if idxs[j] < idxs[i] {
						idxs[i], idxs[j] = idxs[j], idxs[i]
					}
				}
			}
			for _, idx := range idxs {
				status := st.FuncItemStatus[idx]
				if status == "" {
					status = "completed"
				}
				args := ""
				if !st.FuncCustom[idx] && status == "completed" {
					args = "{}"
				}
				if b := st.FuncArgsBuf[idx]; b != nil && b.Len() > 0 {
					args = b.String()
				}
				callID := st.FuncCallIDs[idx]
				name := st.FuncNames[idx]
				if callID == "" && st.CurrentFCID != "" {
					callID = st.CurrentFCID
				}
				if st.isToolSearch(name) {
					_ = outputsWrapperDoc.SetRawBytes(fmt.Sprintf("arr.%d", st.FuncOutputIndices[idx]), buildResponsesToolSearchCallItem(callID, status, args))
				} else if st.FuncCustom[idx] {
					itemDoc := ojson.NewDocString(`{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}`)
					_ = itemDoc.Set("id", fmt.Sprintf("ctc_%s", callID))
					_ = itemDoc.Set("status", status)
					input := ""
					if patchCall := st.ApplyPatchCalls[fmt.Sprint(idx)]; patchCall != nil {
						input = patchCall.Decoder.Input()
					} else {
						input = unwrapCustomToolInput(args)
					}
					_ = itemDoc.Set("input", input)
					_ = itemDoc.Set("call_id", callID)
					st.applyFunctionCallNamespaceFieldsDoc(itemDoc, name, "")
					_ = outputsWrapperDoc.SetRawBytes(fmt.Sprintf("arr.%d", st.FuncOutputIndices[idx]), itemDoc.Bytes())
				} else {
					itemDoc := ojson.NewDocString(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
					_ = itemDoc.Set("id", fmt.Sprintf("fc_%s", callID))
					_ = itemDoc.Set("status", status)
					_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(itemDoc, "arguments", args)
					_ = itemDoc.Set("call_id", callID)
					st.applyFunctionCallNamespaceFieldsDoc(itemDoc, name, "")
					_ = outputsWrapperDoc.SetRawBytes(fmt.Sprintf("arr.%d", st.FuncOutputIndices[idx]), itemDoc.Bytes())
				}
				hasOutputs = true
			}
		}
		if hasOutputs {
			outputsWrapper := outputsWrapperDoc.Bytes()
			if ojson.GetBytesNoCopy(outputsWrapper, "arr.#").Int() > 0 {
				_ = completedDoc.SetRawBytes("response.output", []byte(ojson.GetBytesNoCopy(outputsWrapper, "arr").Raw))
			}
		}

		reasoningLength := 0
		for _, reasoning := range st.ReasoningItems {
			reasoningLength += len(reasoning.Text)
		}
		reasoningTokens := int64(reasoningLength / 4)
		usagePresent := st.Usage.HasUsage || reasoningTokens > 0
		if usagePresent {
			inputTokens, outputTokens, totalTokens, cachedTokens := st.Usage.OpenAIResponsesUsage()
			_ = completedDoc.Set("response.usage.input_tokens", inputTokens)
			_ = completedDoc.Set("response.usage.input_tokens_details.cached_tokens", cachedTokens)
			_ = completedDoc.Set("response.usage.output_tokens", outputTokens)
			_ = completedDoc.Set("response.usage.output_tokens_details.reasoning_tokens", reasoningTokens)
			if totalTokens > 0 || st.Usage.HasUsage {
				_ = completedDoc.Set("response.usage.total_tokens", totalTokens)
			}
		}
		st.CompletedEmitted = true
		out = append(out, emitEvent(eventType, completedDoc.Bytes()))
	}

	return noSSEOutput(out)
}

// ConvertClaudeResponseToOpenAIResponsesNonStream aggregates Claude SSE into a single OpenAI Responses JSON.
func ConvertClaudeResponseToOpenAIResponsesNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	rawJSON, nativeModel := translatorcommon.ClaudeMessagesJSONToSSE(rawJSON)
	// Aggregate Claude SSE lines into a single OpenAI Responses JSON (non-stream)
	// We follow the same aggregation logic as the streaming variant but produce
	// one final object matching docs/out.json structure.

	// Collect SSE data: lines start with "data: "; ignore others
	var chunks [][]byte
	remaining := rawJSON
	for len(remaining) > 0 {
		var line []byte
		idx := bytes.IndexByte(remaining, '\n')
		if idx >= 0 {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		} else {
			line = remaining
			remaining = nil
		}
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		chunks = append(chunks, line[len(dataTag):])
	}

	reqBytes := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	st := newClaudeToResponsesState(reqBytes, ClientToolSearchEnabledFor(ctx))
	if param != nil {
		*param = st
	}

	// Base OpenAI Responses (non-stream) object
	outDoc := ojson.NewDocString(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null,"output":[],"usage":{"input_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens":0,"output_tokens_details":{},"total_tokens":0}}`)

	// Aggregation state
	var (
		responseID    string
		createdAt     int64
		stopReason    string
		usageTokens   claudeResponsesUsageTokens
		fallbackModel string
		fallbacks     translatorcommon.ClaudeFallbackChain
	)

	type nonStreamOutputItem struct {
		outputIndex   int
		itemType      string
		id            string
		callID        string
		name          string
		text          strings.Builder
		signature     string
		annotations   []any
		args          strings.Builder
		inputSnapshot string
		results       []byte
	}

	blockToItem := make(map[int]*nonStreamOutputItem)
	webSearchByToolID := make(map[string]*nonStreamOutputItem)
	outputItems := make([]*nonStreamOutputItem, 0)
	nextOutputIndex := 0
	messageCount := 0
	var activeMessageItem *nonStreamOutputItem
	var pendingAnnotations []any

	allocateOutputIndex := func() int {
		outputIndex := nextOutputIndex
		nextOutputIndex++
		return outputIndex
	}
	newOutputItem := func(itemType string, blockIndex int) *nonStreamOutputItem {
		item := &nonStreamOutputItem{
			outputIndex: allocateOutputIndex(),
			itemType:    itemType,
		}
		outputItems = append(outputItems, item)
		blockToItem[blockIndex] = item
		return item
	}

	// Walk through SSE chunks to fill state
	for _, ch := range chunks {
		root := util.ParseGJSONBytesNoCopy(ch)
		ev := root.Get("type").String()
		// Match the streaming path: no input after the protocol terminator.
		if ev == "message_stop" {
			break
		}

		switch ev {
		case "message_start":
			if msg := root.Get("message"); msg.Exists() {
				responseID = msg.Get("id").String()
				createdAt = time.Now().Unix()
				usageTokens.Merge(msg.Get("usage"))
				defaultReqModel := st.RequestEcho.model
				if defaultReqModel == "" {
					defaultReqModel = modelName
				}
				if fromModel, toModel, ok := translatorcommon.ClaudePreTokenFallbackModels(originalRequestRawJSON, requestRawJSON, defaultReqModel, msg.Get("model").String()); ok {
					fallbackModel = toModel
					fallbacks.RecordPreToken(fromModel, toModel, "", nil)
				}
			}

		case "content_block_start":
			cb := root.Get("content_block")
			if !cb.Exists() {
				continue
			}
			idx := int(root.Get("index").Int())
			typ := cb.Get("type").String()
			if typ != "text" {
				activeMessageItem = nil
			}
			switch typ {
			case "fallback":
				toModel := strings.TrimSpace(cb.Get("to.model").String())
				fromModel := strings.TrimSpace(cb.Get("from.model").String())
				if toModel != "" {
					fallbackModel = strings.Clone(toModel)
				}
				defaultFrom := fromModel
				if defaultFrom == "" {
					defaultFrom = st.RequestEcho.model
				}
				if defaultFrom == "" {
					defaultFrom = modelName
				}
				boundaryIdx := nextOutputIndex
				fallbacks.RecordBlock(cb, defaultFrom, fallbackModel, "output_index", &boundaryIdx)
			case "text":
				item := activeMessageItem
				if item == nil {
					item = newOutputItem("message", idx)
					item.id = fmt.Sprintf("msg_%s_%d", responseID, messageCount)
					messageCount++
				} else {
					blockToItem[idx] = item
				}
				if len(pendingAnnotations) > 0 {
					item.annotations = append(item.annotations, pendingAnnotations...)
					pendingAnnotations = nil
				}
				activeMessageItem = item
			case "tool_use":
				itemType := "function_call"
				if st.isToolSearch(cb.Get("name").String()) {
					itemType = "tool_search_call"
				} else if st.ToolWinners[st.ToolNames.identity(cb.Get("name").String())].toolType == "custom" {
					itemType = "custom_tool_call"
				}
				item := blockToItem[idx]
				if item == nil {
					item = newOutputItem(itemType, idx)
				}
				name, callID := cb.Get("name").String(), cb.Get("id").String()
				if callID != "" && item.callID != "" && callID != item.callID {
					st.FuncIdentityConflicts[idx] = true
				}
				if st.isApplyPatch(item.name) || st.isApplyPatch(name) {
					if st.FuncIdentityConflicts[idx] || (name != "" && item.name != "" && st.ToolNames.identity(name) != st.ToolNames.identity(item.name)) {
						st.SetToolInputError(fmt.Errorf("conflicting apply_patch call identity"))
						return []byte(ojson.GetBytesNoCopy(translatorcommon.ApplyPatchFailure(responseID, 0), "response").Raw)
					}
				}
				if name != "" {
					item.name = name
					item.itemType = itemType
				}
				if callID != "" {
					item.callID = callID
				}
				if input := cb.Get("input"); input.Exists() && (!input.IsObject() || len(input.Map()) > 0) {
					if errValidateApplyPatchSnapshots := validateApplyPatchSnapshots(item.inputSnapshot, input.Raw); errValidateApplyPatchSnapshots != nil && st.FuncInputSnapshotErrors[idx] == nil {
						st.FuncInputSnapshotErrors[idx] = errValidateApplyPatchSnapshots
					}
					item.inputSnapshot = input.Raw
				}
				if st.isApplyPatch(item.name) {
					if errSnapshot := st.FuncInputSnapshotErrors[idx]; errSnapshot != nil {
						st.SetToolInputError(errSnapshot)
						return []byte(ojson.GetBytesNoCopy(translatorcommon.ApplyPatchFailure(responseID, 0), "response").Raw)
					}
				}
				switch item.itemType {
				case "custom_tool_call":
					item.id = fmt.Sprintf("ctc_%s", item.callID)
				case "tool_search_call":
					item.id = responsesToolSearchIDPrefix + item.callID
				default:
					item.id = fmt.Sprintf("fc_%s", item.callID)
				}
			case "server_tool_use":
				name := cb.Get("name").String()
				if name != claudeWebSearchToolName {
					log.Debugf("claude->responses: unmapped server_tool_use %q at block %d", name, idx)
					continue
				}
				toolUseID := cb.Get("id").String()
				item := newOutputItem("web_search_call", idx)
				item.id = responsesWebSearchCallID(toolUseID)
				item.callID = toolUseID
				webSearchByToolID[toolUseID] = item
				// Streaming announces an empty input and fills it through
				// input_json_delta; only seed when the query is already present.
				if input := cb.Get("input"); input.IsObject() && claudeWebSearchQuery(input.Raw) != "" {
					item.args.WriteString(input.Raw)
				}
			case "web_search_tool_result":
				toolUseID := cb.Get("tool_use_id").String()
				if item := webSearchByToolID[toolUseID]; item != nil {
					item.results = claudeWebSearchResultsToResponses(cb.Get("content"))
				} else if toolUseID != "" {
					// Continuation after pause_turn: the search started in the
					// previous response; keep its result under the same id.
					item := newOutputItem("web_search_call", idx)
					item.id = responsesWebSearchCallID(toolUseID)
					item.callID = toolUseID
					item.results = claudeWebSearchResultsToResponses(cb.Get("content"))
					webSearchByToolID[toolUseID] = item
				} else {
					log.Debugf("claude->responses: web_search_tool_result without matching server_tool_use at block %d", idx)
				}
			case "thinking", "redacted_thinking":
				item := newOutputItem("reasoning", idx)
				item.id = fmt.Sprintf("rs_%s_%d", responseID, idx)
				item.signature = claudeReasoningCarrier(cb)
			}

		case "content_block_delta":
			d := root.Get("delta")
			if !d.Exists() {
				continue
			}
			idx := int(root.Get("index").Int())
			item := blockToItem[idx]
			dt := d.Get("type").String()
			switch dt {
			case "text_delta":
				if item != nil && item.itemType == "message" {
					if t := d.Get("text"); t.Exists() {
						item.text.WriteString(t.String())
					}
				}
			case "input_json_delta":
				if item != nil && (item.itemType == "function_call" || item.itemType == "custom_tool_call" || item.itemType == "tool_search_call" || item.itemType == "web_search_call") {
					if pj := d.Get("partial_json"); pj.Exists() {
						item.args.WriteString(pj.String())
					}
				}
			case "thinking_delta":
				if item != nil && item.itemType == "reasoning" {
					if t := d.Get("thinking"); t.Exists() {
						item.text.WriteString(t.String())
					}
				}
			case "signature_delta":
				if item != nil && item.itemType == "reasoning" {
					if signature := d.Get("signature"); signature.Exists() && signature.String() != "" {
						item.signature = signature.String()
					}
				}
			case "citations_delta":
				if citation := d.Get("citation"); citation.Exists() {
					val := ojson.Parse(citation.Raw).Value()
					if item != nil && item.itemType == "message" {
						item.annotations = append(item.annotations, val)
					} else if activeMessageItem != nil {
						activeMessageItem.annotations = append(activeMessageItem.annotations, val)
					} else {
						pendingAnnotations = append(pendingAnnotations, val)
					}
				}
			}

		case "content_block_stop":
			// Output items are finalized after all deltas have been aggregated.

		case "message_delta":
			usage := root.Get("usage")
			usageTokens.Merge(usage)
			if value := root.Get("delta.stop_reason"); value.Exists() {
				stopReason = value.String()
			}
			defaultFrom := st.RequestEcho.model
			if defaultFrom == "" {
				defaultFrom = modelName
			}
			if iterModel, _ := fallbacks.RecordIterations(usage.Get("iterations"), defaultFrom, "", nil); iterModel != "" {
				fallbackModel = iterModel
			}
		}
	}

	// Populate base fields
	_, responseStatus, incompleteDetails := claudeResponsesTerminalState(stopReason)
	_ = outDoc.Set("id", responseID)
	_ = outDoc.Set("created_at", createdAt)
	_ = outDoc.Set("status", responseStatus)
	if len(incompleteDetails) > 0 {
		_ = outDoc.SetRawBytes("incomplete_details", incompleteDetails)
	}
	if claudeStopReasonPauseTurn(stopReason) {
		_ = outDoc.Set("end_turn", false)
	}

	// Inject request echo fields as top-level (similar to streaming variant)
	st.RequestEcho.applyDoc(outDoc, "")

	// A native response's model is authoritative; otherwise retain request echo
	// behavior, including the existing SSE path.
	if nativeModel != "" {
		_ = outDoc.Set("model", nativeModel)
	}
	if fallbackModel != "" {
		_ = outDoc.Set("model", fallbackModel)
	}
	if latestFB := fallbacks.Latest(); len(latestFB) > 0 {
		_ = outDoc.SetRawBytes("fallback", latestFB)
		_ = outDoc.SetRawBytes("fallbacks", fallbacks.ArrayBytes())
	}

	// Build output array in the order of the original content blocks.
	outputs := make([][]byte, 0, len(outputItems))
	for i, outputItem := range outputItems {
		itemStatus := "completed"
		if responseStatus == "incomplete" && i == len(outputItems)-1 {
			itemStatus = "incomplete"
		}
		var item []byte
		switch outputItem.itemType {
		case "reasoning":
			itemDoc := ojson.NewDocString(`{"id":"","type":"reasoning","status":"completed","encrypted_content":"","summary":[]}`)
			_ = itemDoc.Set("id", outputItem.id)
			_ = itemDoc.Set("status", itemStatus)
			_ = itemDoc.Set("encrypted_content", outputItem.signature)
			summary := []byte(`{"type":"summary_text","text":""}`)
			summary, _ = ojson.SetBytes(summary, "text", outputItem.text.String())
			_ = itemDoc.SetRawBytes("summary", translatorcommon.JoinRawArray([][]byte{summary}))
			item = itemDoc.Bytes()
		case "web_search_call":
			item = buildResponsesWebSearchCallItemWithStatus(outputItem.callID, claudeWebSearchQuery(outputItem.args.String()), claudeResponsesWebSearchStatus(stopReason, itemStatus, len(outputItem.results) > 0), outputItem.results)
		case "message":
			itemDoc := ojson.NewDocString(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
			_ = itemDoc.Set("id", outputItem.id)
			_ = itemDoc.Set("status", itemStatus)
			_ = itemDoc.Set("content.0.text", outputItem.text.String())
			if len(outputItem.annotations) > 0 {
				_ = itemDoc.Set("content.0.annotations", outputItem.annotations)
			}
			item = itemDoc.Bytes()
		case "tool_search_call":
			args := outputItem.args.String()
			item = buildResponsesToolSearchCallItem(outputItem.callID, toolSearchCallStatus(itemStatus, args), args)
		case "function_call", "custom_tool_call":
			if outputItem.itemType == "custom_tool_call" {
				itemDoc := ojson.NewDocString(`{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}`)
				_ = itemDoc.Set("id", outputItem.id)
				_ = itemDoc.Set("status", itemStatus)
				input := ""
				if st.isApplyPatch(outputItem.name) {
					patchCall := &translatorcommon.ApplyPatchCallState{}
					if _, errPushArguments := patchCall.PushArguments(outputItem.args.String()); errPushArguments != nil {
						st.SetToolInputError(errPushArguments)
						return []byte(ojson.GetBytesNoCopy(translatorcommon.ApplyPatchFailure(responseID, 0), "response").Raw)
					}
					_, fullInput, errFinishArguments := finishClaudeApplyPatchArguments(patchCall, outputItem.args.String(), outputItem.inputSnapshot)
					if errFinishArguments != nil {
						st.SetToolInputError(errFinishArguments)
						return []byte(ojson.GetBytesNoCopy(translatorcommon.ApplyPatchFailure(responseID, 0), "response").Raw)
					}
					input = fullInput
				} else {
					input = unwrapCustomToolInput(outputItem.args.String())
				}
				_ = itemDoc.Set("input", input)
				_ = itemDoc.Set("call_id", outputItem.callID)
				st.applyFunctionCallNamespaceFieldsDoc(itemDoc, outputItem.name, "")
				item = itemDoc.Bytes()
				break
			}
			args := outputItem.args.String()
			if args == "" && itemStatus == "completed" {
				args = "{}"
			}
			itemDoc := ojson.NewDocString(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
			_ = itemDoc.Set("id", outputItem.id)
			_ = itemDoc.Set("status", itemStatus)
			_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(itemDoc, "arguments", args)
			_ = itemDoc.Set("call_id", outputItem.callID)
			st.applyFunctionCallNamespaceFieldsDoc(itemDoc, outputItem.name, "")
			item = itemDoc.Bytes()
		}
		if len(item) > 0 {
			outputs = append(outputs, item)
		}
	}
	if len(outputs) > 0 {
		_ = outDoc.SetRawBytes("output", translatorcommon.JoinRawArray(outputs))
	}

	// Usage
	inputTokens, outputTokens, totalTokens, cachedTokens := usageTokens.OpenAIResponsesUsage()
	if inputTokens != 0 {
		_ = outDoc.Set("usage.input_tokens", inputTokens)
	}
	if cachedTokens != 0 {
		_ = outDoc.Set("usage.input_tokens_details.cached_tokens", cachedTokens)
	}
	if outputTokens != 0 {
		_ = outDoc.Set("usage.output_tokens", outputTokens)
	}
	if totalTokens != 0 {
		_ = outDoc.Set("usage.total_tokens", totalTokens)
	}
	reasoningLength := 0
	for _, outputItem := range outputItems {
		if outputItem.itemType == "reasoning" {
			reasoningLength += outputItem.text.Len()
		}
	}
	if reasoningLength > 0 {
		// Rough estimate similar to chat completions
		reasoningTokens := int64(reasoningLength / 4)
		if reasoningTokens > 0 {
			_ = outDoc.Set("usage.output_tokens_details.reasoning_tokens", reasoningTokens)
		}
	}

	return outDoc.Bytes()
}

// FinalizeToolInput rejects a patch-enabled stream lacking its source terminator.
func (st *claudeToResponsesState) FinalizeToolInput() [][]byte {
	if st.ToolInputError() != nil || st.CompletedEmitted {
		return nil
	}
	enabled := false
	for name := range st.ToolWinners {
		if st.isApplyPatch(name) {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil
	}
	st.SetToolInputError(fmt.Errorf("upstream apply_patch stream ended before protocol completion"))
	st.Seq++
	return [][]byte{emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, st.Seq))}
}
