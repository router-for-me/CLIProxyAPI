package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

type geminiDetachedReasoningItem struct {
	Index     int
	ID        string
	Signature string
}

type geminiCompletedMessageItem struct {
	ID          string
	Text        string
	Status      string
	Annotations [][]byte
}

type geminiCompletedReasoningItem struct {
	ID        string
	Signature string
	Text      string
}

type geminiFunctionCallEvidence struct {
	PartIndex    int
	HasPartIndex bool
	ApplyPatch   bool
	RawName      string
	UpstreamID   string
	Input        string
	HasInput     bool
	Err          error
	PatchCall    *translatorcommon.ApplyPatchCallState
}

// Gemini usage frames are cumulative snapshots, not additive deltas.
type geminiResponsesUsage struct {
	Present    bool
	Prompt     int64
	Candidates int64
	Thoughts   int64
	Total      int64
	Cached     int64
}

func (usage *geminiResponsesUsage) Merge(root ojson.Result) bool {
	metadata := root.Get("usageMetadata")
	if !metadata.Exists() {
		metadata = root.Get("cpaUsageMetadata")
	}
	if !metadata.Exists() {
		return false
	}
	usage.Present = true
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"promptTokenCount", &usage.Prompt},
		{"candidatesTokenCount", &usage.Candidates},
		{"thoughtsTokenCount", &usage.Thoughts},
		{"totalTokenCount", &usage.Total},
		{"cachedContentTokenCount", &usage.Cached},
	} {
		if value := metadata.Get(field.name); value.Exists() {
			*field.value = value.Int()
		}
	}
	return true
}

func (usage geminiResponsesUsage) JSON() []byte {
	outDoc := ojson.NewDocString(`{"input_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":0}`)
	_ = outDoc.Set("input_tokens", usage.Prompt)
	_ = outDoc.Set("input_tokens_details.cached_tokens", usage.Cached)
	_ = outDoc.Set("output_tokens", usage.Candidates+usage.Thoughts)
	_ = outDoc.Set("output_tokens_details.reasoning_tokens", usage.Thoughts)
	_ = outDoc.Set("total_tokens", usage.Total)
	return outDoc.Bytes()
}

func geminiResponsesTerminalState(finishReason string) (eventType, status string, incompleteDetails []byte) {
	switch strings.ToUpper(strings.TrimSpace(finishReason)) {
	case "MAX_TOKENS":
		return "response.incomplete", "incomplete", []byte(`{"reason":"max_output_tokens"}`)
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "MALFORMED_RESPONSE", "MALFORMED_FUNCTION_CALL":
		return "response.incomplete", "incomplete", []byte(`{"reason":"content_filter"}`)
	default:
		return "response.completed", "completed", nil
	}
}

type geminiToResponsesState struct {
	translatorcommon.ApplyPatchErrorState
	Seq          int
	ResponseID   string
	CreatedAt    int64
	Started      bool
	Completed    bool
	FinishReason string
	Usage        geminiResponsesUsage

	// message aggregation
	MsgOpened    bool
	MsgClosed    bool
	MsgIndex     int
	CurrentMsgID string
	ItemTextBuf  strings.Builder

	// reasoning aggregation
	ReasoningOpened           bool
	ReasoningIndex            int
	ReasoningItemID           string
	ReasoningEnc              string
	ReasoningDirection        string
	ReasoningTargetKind       string
	ReasoningBuf              strings.Builder
	ReasoningPendingDeltas    []string
	ReasoningClosed           bool
	PendingReasoningSignature string
	DetachedReasoning         map[int]geminiDetachedReasoningItem
	CompletedMessages         map[int]geminiCompletedMessageItem
	CompletedReasoning        map[int]geminiCompletedReasoningItem
	SeenReasoningSignatures   map[string]bool
	LastSemanticKind          string
	HiddenTextSignatures      map[string][]string

	// function call aggregation (keyed by output_index)
	NextIndex              int
	FuncArgsBuf            map[int]*strings.Builder
	FuncInputBuf           map[int]string
	FuncCustom             map[int]bool
	FuncToolSearch         map[int]bool
	FuncNames              map[int]string
	FuncNamespaces         map[int]string
	FuncCallIDs            map[int]string
	FuncDone               map[int]bool
	SanitizedNameMap       map[string]string
	ToolIdentityMap        map[string]util.ResponsesToolIdentity
	RequestJSON            []byte
	RequestResolved        bool
	ToolMapsResolved       bool
	FunctionEvidence       map[string]*geminiFunctionCallEvidence
	ActiveStreamingFunc    *translatorcommon.VertexStreamingToolCall
	ActiveStreamingPatch   *translatorcommon.ApplyPatchCallState
	HasFailedStreamingFunc bool

	// web search aggregation
	WebSearchStreamMode          bool
	WebSearchOpened              bool
	WebSearchDone                bool
	WebSearchIndex               int
	WebSearchItemID              string
	WebSearchDoneItem            []byte
	WebSearchQuery               string
	WebSearchQueries             []string
	WebSearchSources             [][]byte
	WebSearchAnnotations         [][]byte
	WebSearchAnnotationsAttached bool
	WebSearchBufferedDeltas      []string
	WebSearchBufferedParts       []geminiStreamBufferedPart
	RawGroundingMetadata         ojson.Result
	PartMappings                 []GeminiPartMapping
	StreamPartIndex              int
	CurrentLogicalPartIndex      int
	CurrentPartKind              string
	HasSeenFirstPart             bool
	TextPartRunActive            bool
	CurrentMsgRuneOffset         int64
	EmittedAnnotationCount       map[int]int
}

type geminiStreamBufferedPart struct {
	PartIndex int
	Text      string
}

// responseIDCounter provides a process-wide unique counter for synthesized response identifiers.
var responseIDCounter uint64

// funcCallIDCounter provides a process-wide unique counter for function call identifiers.
var funcCallIDCounter uint64

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && ojson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && ojson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

func unwrapRequestRoot(root ojson.Result) ojson.Result {
	req := root.Get("request")
	if !req.Exists() {
		return root
	}
	if req.Get("model").Exists() || req.Get("input").Exists() || req.Get("instructions").Exists() {
		return req
	}
	return root
}

func unwrapGeminiResponseRoot(root ojson.Result) ojson.Result {
	resp := root.Get("response")
	if resp.Exists() {
		// Vertex-style Gemini responses wrap the actual payload in a "response" object.
		if resp.Get("candidates").Exists() || resp.Get("responseId").Exists() || resp.Get("usageMetadata").Exists() || resp.Get("cpaUsageMetadata").Exists() {
			return resp
		}
	}
	if !root.Get("candidates.0.content.parts").Exists() && !ojson.Valid(root.Raw) {
		if repaired := closeTruncatedJSON(root.Raw); repaired != "" {
			if repairedRoot := ojson.Parse(repaired); repairedRoot.Get("candidates.0.content.parts").Exists() {
				return repairedRoot
			}
		}
	}
	return root
}

func closeTruncatedJSON(raw string) string {
	var stack []byte
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 && stack[len(stack)-1] == c {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if !inString && len(stack) == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw) + len(stack) + 2)
	b.WriteString(raw)
	if inString {
		if escaped {
			b.WriteByte('\\')
		}
		b.WriteByte('"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		b.WriteByte(stack[i])
	}
	return b.String()
}

func emitEvent(event string, payload []byte) []byte {
	return translatorcommon.SSEEventData(event, payload)
}

func hasEffectiveGoogleSearchTool(rawJSON []byte) bool {
	if len(rawJSON) == 0 {
		return false
	}
	if ojson.GetBytesNoCopy(rawJSON, "requestType").String() == "web_search" {
		return true
	}
	for _, path := range []string{"request.tools", "tools"} {
		tools := ojson.GetBytesNoCopy(rawJSON, path)
		if !tools.IsArray() {
			continue
		}
		for _, tool := range tools.Array() {
			if tool.Get("googleSearch").Exists() {
				return true
			}
		}
	}
	return false
}

func isUpstreamGeminiRequest(rawJSON []byte) bool {
	if len(rawJSON) == 0 {
		return false
	}
	if ojson.GetBytesNoCopy(rawJSON, "requestType").Exists() {
		return true
	}
	for _, path := range []string{"contents", "request.contents"} {
		if ojson.GetBytesNoCopy(rawJSON, path).Exists() {
			return true
		}
	}
	return false
}

func determineWebSearchStreamMode(modelName, requestModelName string, originalRequestRawJSON, requestRawJSON, reqJSON []byte) bool {
	if len(originalRequestRawJSON) > 0 {
		origRoot := unwrapRequestRoot(util.ParseGJSONBytesNoCopy(originalRequestRawJSON))
		if !AllowsResponsesWebSearchToolChoice(origRoot) {
			return false
		}
	}
	if len(requestRawJSON) > 0 {
		reqRoot := unwrapRequestRoot(util.ParseGJSONBytesNoCopy(requestRawJSON))
		if reqRoot.Get("tool_choice").Exists() && !AllowsResponsesWebSearchToolChoice(reqRoot) {
			return false
		}
		if isUpstreamGeminiRequest(requestRawJSON) || hasEffectiveGoogleSearchTool(requestRawJSON) {
			return hasEffectiveGoogleSearchTool(requestRawJSON)
		}
	}
	if len(reqJSON) == 0 {
		reqJSON = pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	}
	if len(reqJSON) > 0 {
		reqRoot := unwrapRequestRoot(util.ParseGJSONBytesNoCopy(reqJSON))
		return HasResponsesWebSearchTool(reqRoot) &&
			AllowsResponsesWebSearchToolChoice(reqRoot) &&
			(ModelSupportsWebSearch(modelName) || ModelSupportsWebSearch(requestModelName))
	}
	return false
}

// ConvertGeminiResponseToOpenAIResponses converts Gemini SSE chunks into OpenAI Responses SSE events.
func ConvertGeminiResponseToOpenAIResponses(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		reqJSON := bytes.Clone(pickRequestJSON(originalRequestRawJSON, requestRawJSON))
		*param = &geminiToResponsesState{
			FuncArgsBuf:             make(map[int]*strings.Builder),
			FuncInputBuf:            make(map[int]string),
			FuncCustom:              make(map[int]bool),
			FuncToolSearch:          make(map[int]bool),
			FuncNames:               make(map[int]string),
			FuncNamespaces:          make(map[int]string),
			FuncCallIDs:             make(map[int]string),
			FuncDone:                make(map[int]bool),
			DetachedReasoning:       make(map[int]geminiDetachedReasoningItem),
			CompletedMessages:       make(map[int]geminiCompletedMessageItem),
			CompletedReasoning:      make(map[int]geminiCompletedReasoningItem),
			SeenReasoningSignatures: make(map[string]bool),
			SanitizedNameMap:        util.SanitizedToolNameMap(originalRequestRawJSON),
			ToolIdentityMap:         util.ResponsesToolReverseIdentityMap(reqJSON),
			RequestJSON:             reqJSON,
			RequestResolved:         true,
			ToolMapsResolved:        true,
			EmittedAnnotationCount:  make(map[int]int),
		}
	}
	st := (*param).(*geminiToResponsesState)
	if !st.RequestResolved {
		st.RequestJSON = bytes.Clone(pickRequestJSON(originalRequestRawJSON, requestRawJSON))
		st.RequestResolved = true
	}
	reqJSON := st.RequestJSON
	if st.FuncArgsBuf == nil {
		st.FuncArgsBuf = make(map[int]*strings.Builder)
	}
	if st.FuncInputBuf == nil {
		st.FuncInputBuf = make(map[int]string)
	}
	if st.FuncCustom == nil {
		st.FuncCustom = make(map[int]bool)
	}
	if st.FuncToolSearch == nil {
		st.FuncToolSearch = make(map[int]bool)
	}
	if st.FuncNames == nil {
		st.FuncNames = make(map[int]string)
	}
	if st.FuncNamespaces == nil {
		st.FuncNamespaces = make(map[int]string)
	}
	if st.FuncCallIDs == nil {
		st.FuncCallIDs = make(map[int]string)
	}
	if st.FuncDone == nil {
		st.FuncDone = make(map[int]bool)
	}
	if st.DetachedReasoning == nil {
		st.DetachedReasoning = make(map[int]geminiDetachedReasoningItem)
	}
	if st.CompletedMessages == nil {
		st.CompletedMessages = make(map[int]geminiCompletedMessageItem)
	}
	if st.CompletedReasoning == nil {
		st.CompletedReasoning = make(map[int]geminiCompletedReasoningItem)
	}
	if st.SeenReasoningSignatures == nil {
		st.SeenReasoningSignatures = make(map[string]bool)
	}
	if !st.ToolMapsResolved {
		if st.SanitizedNameMap == nil {
			st.SanitizedNameMap = util.SanitizedToolNameMap(originalRequestRawJSON)
		}
		if st.ToolIdentityMap == nil {
			st.ToolIdentityMap = util.ResponsesToolReverseIdentityMap(reqJSON)
		}
		st.ToolMapsResolved = true
	}
	if st.EmittedAnnotationCount == nil {
		st.EmittedAnnotationCount = make(map[int]int)
	}

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	rawJSON = bytes.TrimSpace(rawJSON)
	if len(rawJSON) == 0 || st.Completed {
		return [][]byte{}
	}
	done := bytes.Equal(rawJSON, []byte("[DONE]"))
	if done {
		if !st.Started || st.ActiveStreamingFunc != nil || st.HasFailedStreamingFunc {
			return [][]byte{}
		}
		if st.FinishReason == "" {
			st.FinishReason = "STOP"
		}
		rawJSON = []byte(`{}`)
	}

	root := util.ParseGJSONBytesNoCopy(rawJSON)
	if !root.Exists() {
		return [][]byte{}
	}
	root = unwrapGeminiResponseRoot(root)
	hasUsage := st.Usage.Merge(root)
	messageStatus := "completed"

	var out [][]byte
	nextSeq := func() int { st.Seq++; return st.Seq }

	reasoningEncryptedContent := func() string {
		if st.ReasoningEnc == "" || st.ReasoningDirection == "" {
			return st.ReasoningEnc
		}
		return encodeGeminiResponsesCarrier(st.ReasoningEnc, st.ReasoningDirection, st.ReasoningTargetKind)
	}
	finalizeWebSearch := func() {
		if !st.WebSearchOpened || st.WebSearchDone {
			return
		}
		if st.WebSearchQuery == "" && len(st.WebSearchQueries) > 0 {
			st.WebSearchQuery = st.WebSearchQueries[0]
		}
		if st.WebSearchQuery == "" && len(reqJSON) > 0 {
			reqRoot := unwrapRequestRoot(util.ParseGJSONBytesNoCopy(reqJSON))
			st.WebSearchQuery = strings.Clone(ExtractResponsesWebSearchQuery(reqRoot))
		}

		completedDoc := ojson.NewDocString(`{"type":"response.web_search_call.completed","sequence_number":0,"output_index":0,"item_id":""}`)
		_ = completedDoc.Set("sequence_number", nextSeq())
		_ = completedDoc.Set("output_index", st.WebSearchIndex)
		_ = completedDoc.Set("item_id", st.WebSearchItemID)
		out = append(out, emitEvent("response.web_search_call.completed", completedDoc.Bytes()))

		doneItem := BuildResponsesWebSearchCallItem(st.WebSearchItemID, st.WebSearchQuery, st.WebSearchQueries, st.WebSearchSources)
		st.WebSearchDoneItem = doneItem
		doneEventDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0}`)
		_ = doneEventDoc.Set("sequence_number", nextSeq())
		_ = doneEventDoc.Set("output_index", st.WebSearchIndex)
		_ = doneEventDoc.SetRawBytes("item", doneItem)
		out = append(out, emitEvent("response.output_item.done", doneEventDoc.Bytes()))
		st.WebSearchDone = true
	}
	openReasoning := func() {
		if st.ReasoningOpened || st.ReasoningClosed || (st.ReasoningBuf.Len() == 0 && st.ReasoningEnc == "") {
			return
		}
		finalizeWebSearch()
		st.ReasoningOpened = true
		st.ReasoningIndex = st.NextIndex
		st.NextIndex++
		st.ReasoningItemID = fmt.Sprintf("rs_%s_%d", st.ResponseID, st.ReasoningIndex)
		itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","encrypted_content":"","summary":[]}}`)
		_ = itemDoc.Set("sequence_number", nextSeq())
		_ = itemDoc.Set("output_index", st.ReasoningIndex)
		_ = itemDoc.Set("item.id", st.ReasoningItemID)
		_ = itemDoc.Set("item.encrypted_content", reasoningEncryptedContent())
		out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))
		partAddedDoc := ojson.NewDocString(`{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
		_ = partAddedDoc.Set("sequence_number", nextSeq())
		_ = partAddedDoc.Set("item_id", st.ReasoningItemID)
		_ = partAddedDoc.Set("output_index", st.ReasoningIndex)
		out = append(out, emitEvent("response.reasoning_summary_part.added", partAddedDoc.Bytes()))
		for _, delta := range st.ReasoningPendingDeltas {
			msgDoc := ojson.NewDocString(`{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`)
			_ = msgDoc.Set("sequence_number", nextSeq())
			_ = msgDoc.Set("item_id", st.ReasoningItemID)
			_ = msgDoc.Set("output_index", st.ReasoningIndex)
			_ = msgDoc.Set("delta", delta)
			out = append(out, emitEvent("response.reasoning_summary_text.delta", msgDoc.Bytes()))
		}
		st.ReasoningPendingDeltas = nil
	}

	// Helper to finalize reasoning summary events in correct order.
	// It emits response.reasoning_summary_text.done followed by
	// response.reasoning_summary_part.done exactly once.
	finalizeReasoning := func() {
		openReasoning()
		if !st.ReasoningOpened || st.ReasoningClosed {
			return
		}
		full := st.ReasoningBuf.String()
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

		itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":""}]}}`)
		_ = itemDoneDoc.Set("sequence_number", nextSeq())
		_ = itemDoneDoc.Set("item.id", st.ReasoningItemID)
		_ = itemDoneDoc.Set("output_index", st.ReasoningIndex)
		_ = itemDoneDoc.Set("item.encrypted_content", reasoningEncryptedContent())
		_ = itemDoneDoc.Set("item.summary.0.text", full)
		out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))

		st.CompletedReasoning[st.ReasoningIndex] = geminiCompletedReasoningItem{
			ID:        st.ReasoningItemID,
			Signature: reasoningEncryptedContent(),
			Text:      full,
		}
		st.ReasoningClosed = true
	}

	resetReasoning := func() {
		st.ReasoningOpened = false
		st.ReasoningClosed = false
		st.ReasoningIndex = 0
		st.ReasoningItemID = ""
		st.ReasoningEnc = ""
		st.ReasoningDirection = ""
		st.ReasoningTargetKind = ""
		st.ReasoningBuf.Reset()
		st.ReasoningPendingDeltas = nil
	}

	openWebSearch := func() {
		if st.WebSearchOpened {
			return
		}
		finalizeReasoning()
		st.WebSearchOpened = true
		st.WebSearchIndex = st.NextIndex
		st.NextIndex++
		st.WebSearchItemID = fmt.Sprintf("ws_%s", strings.TrimPrefix(st.ResponseID, "resp_"))
		if st.WebSearchQuery == "" && len(st.WebSearchQueries) > 0 {
			st.WebSearchQuery = st.WebSearchQueries[0]
		}
		if st.WebSearchQuery == "" && len(reqJSON) > 0 {
			reqRoot := unwrapRequestRoot(util.ParseGJSONBytesNoCopy(reqJSON))
			st.WebSearchQuery = strings.Clone(ExtractResponsesWebSearchQuery(reqRoot))
		}

		addedDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"web_search_call","status":"in_progress","action":{"type":"search","query":""}}}`)
		_ = addedDoc.Set("sequence_number", nextSeq())
		_ = addedDoc.Set("output_index", st.WebSearchIndex)
		_ = addedDoc.Set("item.id", st.WebSearchItemID)
		_ = addedDoc.Set("item.action.query", st.WebSearchQuery)
		out = append(out, emitEvent("response.output_item.added", addedDoc.Bytes()))

		searchingDoc := ojson.NewDocString(`{"type":"response.web_search_call.searching","sequence_number":0,"output_index":0,"item_id":""}`)
		_ = searchingDoc.Set("sequence_number", nextSeq())
		_ = searchingDoc.Set("output_index", st.WebSearchIndex)
		_ = searchingDoc.Set("item_id", st.WebSearchItemID)
		out = append(out, emitEvent("response.web_search_call.searching", searchingDoc.Bytes()))
	}

	flushWebSearchBufferedText := func() {
		finalizeWebSearch()
		if len(st.WebSearchBufferedDeltas) == 0 {
			return
		}
		if st.MsgClosed {
			st.MsgOpened = false
			st.MsgClosed = false
			st.ItemTextBuf.Reset()
			st.CurrentMsgRuneOffset = 0
		}
		if !st.MsgOpened {
			st.MsgOpened = true
			st.MsgIndex = st.NextIndex
			st.NextIndex++
			st.CurrentMsgID = fmt.Sprintf("msg_%s_%d", st.ResponseID, st.MsgIndex)
			itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`)
			_ = itemDoc.Set("sequence_number", nextSeq())
			_ = itemDoc.Set("output_index", st.MsgIndex)
			_ = itemDoc.Set("item.id", st.CurrentMsgID)
			out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))
			partAddedDoc := ojson.NewDocString(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
			_ = partAddedDoc.Set("sequence_number", nextSeq())
			_ = partAddedDoc.Set("item_id", st.CurrentMsgID)
			_ = partAddedDoc.Set("output_index", st.MsgIndex)
			out = append(out, emitEvent("response.content_part.added", partAddedDoc.Bytes()))
			st.ItemTextBuf.Reset()
			st.CurrentMsgRuneOffset = 0
		}
		for _, delta := range st.WebSearchBufferedDeltas {
			st.ItemTextBuf.WriteString(delta)
			msgDoc := ojson.NewDocString(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
			_ = msgDoc.Set("sequence_number", nextSeq())
			_ = msgDoc.Set("item_id", st.CurrentMsgID)
			_ = msgDoc.Set("output_index", st.MsgIndex)
			_ = msgDoc.Set("delta", delta)
			out = append(out, emitEvent("response.output_text.delta", msgDoc.Bytes()))
		}
		for _, bp := range st.WebSearchBufferedParts {
			n := len(st.PartMappings)
			if n > 0 && st.PartMappings[n-1].PartIndex == bp.PartIndex && st.PartMappings[n-1].MessageIndex == st.MsgIndex {
				st.PartMappings[n-1].PartText += bp.Text
			} else {
				st.PartMappings = append(st.PartMappings, GeminiPartMapping{
					PartIndex:      bp.PartIndex,
					MessageIndex:   st.MsgIndex,
					StartRuneInMsg: st.CurrentMsgRuneOffset,
					PartText:       bp.Text,
				})
			}
			st.CurrentMsgRuneOffset += int64(utf8.RuneCountInString(bp.Text))
		}
		st.WebSearchBufferedDeltas = nil
		st.WebSearchBufferedParts = nil
	}

	emitNewCitationAnnotations := func(msgIndex int, itemID string, annotations [][]byte) {
		emitted := st.EmittedAnnotationCount[msgIndex]
		for annIdx := emitted; annIdx < len(annotations); annIdx++ {
			annEventDoc := ojson.NewDocString(`{"type":"response.output_text.annotation.added","sequence_number":0,"response_id":"","item_id":"","output_index":0,"content_index":0,"annotation_index":0}`)
			_ = annEventDoc.Set("sequence_number", nextSeq())
			_ = annEventDoc.Set("response_id", st.ResponseID)
			_ = annEventDoc.Set("item_id", itemID)
			_ = annEventDoc.Set("output_index", msgIndex)
			_ = annEventDoc.Set("content_index", 0)
			_ = annEventDoc.Set("annotation_index", annIdx)
			_ = annEventDoc.SetRawBytes("annotation", annotations[annIdx])
			out = append(out, emitEvent("response.output_text.annotation.added", annEventDoc.Bytes()))
		}
		if len(annotations) > emitted {
			st.EmittedAnnotationCount[msgIndex] = len(annotations)
		}
	}

	// Helper to finalize the assistant message in correct order.
	// It emits response.output_text.annotation.added for any new citations,
	// then response.output_text.done, response.content_part.done,
	// and response.output_item.done exactly once.
	finalizeMessage := func() {
		finalizeWebSearch()
		if len(st.WebSearchBufferedDeltas) > 0 {
			flushWebSearchBufferedText()
		}
		if !st.MsgOpened || st.MsgClosed {
			return
		}
		fullText := st.ItemTextBuf.String()
		var msgCitations [][]byte
		if st.RawGroundingMetadata.Exists() {
			cMap := BuildResponsesURLCitationsForMessages(st.RawGroundingMetadata, st.PartMappings, []string{fullText})
			msgCitations = cMap[st.MsgIndex]
			if len(msgCitations) == 0 && len(st.CompletedMessages) == 0 && len(cMap[0]) > 0 {
				msgCitations = cMap[0]
			}
			st.WebSearchAnnotations = msgCitations
		}
		emitNewCitationAnnotations(st.MsgIndex, st.CurrentMsgID, msgCitations)
		doneDoc := ojson.NewDocString(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
		_ = doneDoc.Set("sequence_number", nextSeq())
		_ = doneDoc.Set("item_id", st.CurrentMsgID)
		_ = doneDoc.Set("output_index", st.MsgIndex)
		_ = doneDoc.Set("text", fullText)
		out = append(out, emitEvent("response.output_text.done", doneDoc.Bytes()))
		partDoneDoc := ojson.NewDocString(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
		_ = partDoneDoc.Set("sequence_number", nextSeq())
		_ = partDoneDoc.Set("item_id", st.CurrentMsgID)
		_ = partDoneDoc.Set("output_index", st.MsgIndex)
		_ = partDoneDoc.Set("part.text", fullText)
		if len(msgCitations) > 0 {
			_ = partDoneDoc.SetRawBytes("part.annotations", translatorcommon.JoinRawArray(msgCitations))
		}
		out = append(out, emitEvent("response.content_part.done", partDoneDoc.Bytes()))
		finalDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}}`)
		_ = finalDoc.Set("sequence_number", nextSeq())
		_ = finalDoc.Set("output_index", st.MsgIndex)
		_ = finalDoc.Set("item.id", st.CurrentMsgID)
		_ = finalDoc.Set("item.status", messageStatus)
		_ = finalDoc.Set("item.content.0.text", fullText)
		if len(msgCitations) > 0 {
			_ = finalDoc.SetRawBytes("item.content.0.annotations", translatorcommon.JoinRawArray(msgCitations))
			st.WebSearchAnnotationsAttached = true
		}
		out = append(out, emitEvent("response.output_item.done", finalDoc.Bytes()))

		st.CompletedMessages[st.MsgIndex] = geminiCompletedMessageItem{
			ID:          st.CurrentMsgID,
			Text:        fullText,
			Status:      messageStatus,
			Annotations: msgCitations,
		}
		st.MsgClosed = true
		st.CurrentMsgRuneOffset = 0
	}

	emitLateCitations := func() {
		if !st.RawGroundingMetadata.Exists() || len(st.CompletedMessages) == 0 {
			return
		}
		msgTexts := make([]string, 0, len(st.CompletedMessages))
		for idx := 0; idx < st.NextIndex; idx++ {
			if msg, ok := st.CompletedMessages[idx]; ok {
				msgTexts = append(msgTexts, msg.Text)
			}
		}
		lateCitationsMap := BuildResponsesURLCitationsForMessages(st.RawGroundingMetadata, st.PartMappings, msgTexts)
		if lateCitationsMap == nil {
			return
		}
		for idx := 0; idx < st.NextIndex; idx++ {
			completedMessage, ok := st.CompletedMessages[idx]
			if !ok {
				continue
			}
			lateCites := lateCitationsMap[idx]
			if len(lateCites) == 0 && len(st.CompletedMessages) == 1 && len(lateCitationsMap[0]) > 0 {
				lateCites = lateCitationsMap[0]
			}
			annotations := MergeCitationAnnotations(completedMessage.Annotations, lateCites)
			emitNewCitationAnnotations(idx, completedMessage.ID, annotations)
			if len(annotations) > 0 {
				completedMessage.Annotations = annotations
				st.CompletedMessages[idx] = completedMessage
			}
		}
	}

	emitDetachedReasoning := func(signature, direction, targetKind string) {
		signature = strings.TrimSpace(signature)
		if signature == "" || st.SeenReasoningSignatures[signature] {
			return
		}
		finalizeReasoning()
		finalizeMessage()
		idx := st.NextIndex
		st.NextIndex++
		placement := "before"
		if direction == geminiResponsesCarrierPrevious {
			placement = "after"
		}
		itemID := fmt.Sprintf("rs_%s_detached_%s_%d", st.ResponseID, placement, idx)
		carrierSignature := encodeGeminiResponsesCarrier(signature, direction, targetKind)

		addedDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","encrypted_content":"","summary":[]}}`)
		_ = addedDoc.Set("sequence_number", nextSeq())
		_ = addedDoc.Set("output_index", idx)
		_ = addedDoc.Set("item.id", itemID)
		_ = addedDoc.Set("item.encrypted_content", carrierSignature)
		out = append(out, emitEvent("response.output_item.added", addedDoc.Bytes()))

		doneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","encrypted_content":"","summary":[]}}`)
		_ = doneDoc.Set("sequence_number", nextSeq())
		_ = doneDoc.Set("output_index", idx)
		_ = doneDoc.Set("item.id", itemID)
		_ = doneDoc.Set("item.encrypted_content", carrierSignature)
		out = append(out, emitEvent("response.output_item.done", doneDoc.Bytes()))

		st.DetachedReasoning[idx] = geminiDetachedReasoningItem{Index: idx, ID: itemID, Signature: carrierSignature}
		st.SeenReasoningSignatures[signature] = true
	}
	emitTrailingDetachedReasoning := func(signature string) {
		switch st.LastSemanticKind {
		case geminiResponsesCarrierText:
			signature = strings.TrimSpace(signature)
			if signature == "" || st.SeenReasoningSignatures[signature] {
				return
			}
			finalizeReasoning()
			finalizeMessage()
			// LastSemanticKind also includes thought text. Never bind a later
			// thought signature to a visible message from before that thought.
			if !st.MsgOpened || (st.ReasoningOpened && st.ReasoningIndex > st.MsgIndex) {
				emitDetachedReasoning(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
				return
			}
			if st.HiddenTextSignatures == nil {
				st.HiddenTextSignatures = make(map[string][]string)
			}
			signatures := append(st.HiddenTextSignatures[st.CurrentMsgID], signature)
			// Keep failed writes in the prefix so a later successful write cannot
			// move a newer signature ahead of an earlier fallback carrier.
			st.HiddenTextSignatures[st.CurrentMsgID] = signatures
			if cacheGeminiResponsesTextSignatures(modelName, st.CurrentMsgID, st.ItemTextBuf.String(), signatures) {
				st.SeenReasoningSignatures[signature] = true
				return
			}
			// Preserve replay continuity if the cache cannot accept the signature.
			emitDetachedReasoning(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		case geminiResponsesCarrierFunction:
			emitDetachedReasoning(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierFunction)
		default:
			emitDetachedReasoning(signature, geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)
		}
	}

	// Initialize per-response fields and emit created/in_progress once
	if !st.Started {
		st.ResponseID = strings.Clone(root.Get("responseId").String())
		if st.ResponseID == "" {
			st.ResponseID = fmt.Sprintf("resp_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&responseIDCounter, 1))
		}
		if !strings.HasPrefix(st.ResponseID, "resp_") {
			st.ResponseID = fmt.Sprintf("resp_%s", st.ResponseID)
		}
		if v := root.Get("createTime"); v.Exists() {
			if t, errParseCreateTime := time.Parse(time.RFC3339Nano, v.String()); errParseCreateTime == nil {
				st.CreatedAt = t.Unix()
			}
		}
		if st.CreatedAt == 0 {
			st.CreatedAt = time.Now().Unix()
		}

		createdDoc := ojson.NewDocString(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
		_ = createdDoc.Set("sequence_number", nextSeq())
		_ = createdDoc.Set("response.id", st.ResponseID)
		_ = createdDoc.Set("response.created_at", st.CreatedAt)
		requestModelName := translatorcommon.RequestModelName(originalRequestRawJSON, requestRawJSON)
		if requestModelName == "" && len(reqJSON) > 0 {
			requestModelName = translatorcommon.RequestModelName(reqJSON, nil)
		}
		if requestModelName == "" {
			requestModelName = modelName
		}
		if requestModelName != "" {
			_ = createdDoc.Set("response.model", requestModelName)
		}
		out = append(out, emitEvent("response.created", createdDoc.Bytes()))

		inprogDoc := ojson.NewDocString(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","output":[]}}`)
		_ = inprogDoc.Set("sequence_number", nextSeq())
		_ = inprogDoc.Set("response.id", st.ResponseID)
		_ = inprogDoc.Set("response.created_at", st.CreatedAt)
		if requestModelName != "" {
			_ = inprogDoc.Set("response.model", requestModelName)
		}
		out = append(out, emitEvent("response.in_progress", inprogDoc.Bytes()))

		st.Started = true
		st.NextIndex = 0
		st.WebSearchStreamMode = determineWebSearchStreamMode(modelName, requestModelName, originalRequestRawJSON, requestRawJSON, reqJSON)
	}

	// Handle groundingMetadata for web search
	if gm := ExtractGroundingMetadata(root); gm.Exists() {
		if st.RawGroundingMetadata.Exists() {
			st.RawGroundingMetadata = MergeGroundingMetadata(st.RawGroundingMetadata, gm)
		} else {
			st.RawGroundingMetadata = MergeGroundingMetadata(ojson.Result{}, gm)
		}
		mergedGM := st.RawGroundingMetadata
		queries := ExtractGroundingQueries(mergedGM)
		if len(queries) > 0 {
			st.WebSearchQueries = queries
			if st.WebSearchQuery == "" {
				st.WebSearchQuery = queries[0]
			}
		}
		if sources := ExtractGroundingSources(mergedGM); len(sources) > 0 {
			st.WebSearchSources = sources
		}
		// Function calls, thoughts, or signature boundaries may finalize the search
		// item before later grounding frames arrive. Keep the cached completed item
		// aligned with the latest queries and sources so response.completed is complete.
		if st.WebSearchDone {
			st.WebSearchDoneItem = BuildResponsesWebSearchCallItem(st.WebSearchItemID, st.WebSearchQuery, st.WebSearchQueries, st.WebSearchSources)
		}

		if !st.WebSearchOpened && HasValidWebGrounding(mergedGM) {
			openWebSearch()
		}

		emitLateCitations()
	}

	// Handle parts (text/thought/functionCall)
	if parts := root.Get("candidates.0.content.parts"); parts.Exists() && parts.IsArray() {
		parts.ForEach(func(partIdxInChunk, part ojson.Result) bool {
			explicitPartIndex := -1
			if p := part.Get("partIndex"); p.Exists() {
				explicitPartIndex = int(p.Int())
			} else if p := part.Get("index"); p.Exists() {
				explicitPartIndex = int(p.Int())
			}

			signature := strings.Clone(strings.TrimSpace(part.Get("thoughtSignature").String()))
			if signature == "" {
				signature = strings.Clone(strings.TrimSpace(part.Get("thought_signature").String()))
			}
			functionCall := part.Get("functionCall")
			text := part.Get("text")
			isThought := part.Get("thought").Bool()

			var partKind string
			switch {
			case isThought:
				partKind = "thought"
			case functionCall.Exists():
				partKind = "function"
			case text.Exists():
				partKind = "text"
			default:
				partKind = "unknown"
			}

			var currentPartIndex int
			if explicitPartIndex >= 0 {
				currentPartIndex = explicitPartIndex
				st.CurrentLogicalPartIndex = explicitPartIndex
				st.CurrentPartKind = partKind
				st.HasSeenFirstPart = true
				st.TextPartRunActive = (partKind == "text")
			} else {
				if !st.HasSeenFirstPart {
					st.HasSeenFirstPart = true
					st.CurrentLogicalPartIndex = 0
					st.CurrentPartKind = partKind
					currentPartIndex = 0
					if partKind == "text" {
						st.TextPartRunActive = true
					}
				} else {
					if partIdxInChunk.Int() > 0 {
						st.CurrentLogicalPartIndex++
						st.CurrentPartKind = partKind
						st.TextPartRunActive = (partKind == "text")
					} else if partKind != st.CurrentPartKind {
						st.CurrentLogicalPartIndex++
						st.CurrentPartKind = partKind
						st.TextPartRunActive = (partKind == "text")
					} else if partKind == "function" {
						st.CurrentLogicalPartIndex++
						st.CurrentPartKind = partKind
						st.TextPartRunActive = false
					} else if partKind == "text" && !st.TextPartRunActive {
						st.CurrentLogicalPartIndex++
						st.CurrentPartKind = partKind
						st.TextPartRunActive = true
					}
					currentPartIndex = st.CurrentLogicalPartIndex
				}
			}
			st.StreamPartIndex = currentPartIndex
			isStreamingFuncContinuation := functionCall.Exists() && st.ActiveStreamingFunc != nil && st.ActiveStreamingFunc.CanContinueWith(functionCall)
			if functionCall.Exists() && !isStreamingFuncContinuation && st.PendingReasoningSignature != "" {
				if signature == "" {
					emitDetachedReasoning(st.PendingReasoningSignature, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
				} else {
					emitTrailingDetachedReasoning(st.PendingReasoningSignature)
				}
				st.PendingReasoningSignature = ""
			}
			reasoningActive := (st.ReasoningOpened && !st.ReasoningClosed) || (!st.ReasoningOpened && (st.ReasoningBuf.Len() > 0 || st.ReasoningEnc != ""))
			if signature != "" && !isThought {
				if isStreamingFuncContinuation {
					if !st.SeenReasoningSignatures[signature] {
						if st.PendingReasoningSignature != "" && st.PendingReasoningSignature != signature {
							emitTrailingDetachedReasoning(st.PendingReasoningSignature)
						}
						st.PendingReasoningSignature = signature
					}
				} else if reasoningActive {
					switch {
					case st.ReasoningEnc == "" || st.ReasoningEnc == signature:
						st.ReasoningEnc = signature
						switch {
						case functionCall.Exists():
							st.ReasoningDirection = geminiResponsesCarrierNext
							st.ReasoningTargetKind = geminiResponsesCarrierFunction
						case text.Exists() && text.String() != "":
							st.ReasoningDirection = geminiResponsesCarrierNext
							st.ReasoningTargetKind = geminiResponsesCarrierText
						default:
							st.ReasoningDirection = geminiResponsesCarrierStandalone
							st.ReasoningTargetKind = geminiResponsesCarrierText
						}
						st.SeenReasoningSignatures[signature] = true
					default:
						finalizeReasoning()
						if functionCall.Exists() {
							emitDetachedReasoning(signature, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
						} else if !st.SeenReasoningSignatures[signature] {
							st.PendingReasoningSignature = signature
						}
					}
					if text.Exists() && text.String() == "" && !functionCall.Exists() {
						finalizeReasoning()
						return true
					}
				} else {
					switch {
					case functionCall.Exists():
						emitDetachedReasoning(signature, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
					case text.Exists() && text.String() != "":
						if st.PendingReasoningSignature != "" && st.PendingReasoningSignature != signature {
							emitTrailingDetachedReasoning(st.PendingReasoningSignature)
							st.PendingReasoningSignature = ""
						}
						if !st.SeenReasoningSignatures[signature] {
							st.PendingReasoningSignature = signature
						}
					case text.Exists() && text.String() == "":
						if st.PendingReasoningSignature != "" {
							pendingSignature := st.PendingReasoningSignature
							st.PendingReasoningSignature = ""
							if pendingSignature != signature {
								emitTrailingDetachedReasoning(pendingSignature)
							}
						}
						if st.MsgOpened || len(st.FuncDone) > 0 || len(st.WebSearchBufferedDeltas) > 0 {
							emitTrailingDetachedReasoning(signature)
						} else if !st.SeenReasoningSignatures[signature] {
							st.PendingReasoningSignature = signature
						}
						return true
					}
				}
			}

			// Reasoning text
			if isThought {
				if len(st.WebSearchBufferedDeltas) > 0 {
					finalizeMessage()
				}
				if st.PendingReasoningSignature != "" && st.MsgOpened && !st.MsgClosed {
					emitTrailingDetachedReasoning(st.PendingReasoningSignature)
					st.PendingReasoningSignature = ""
				}
				incomingSignature := ""
				if signature != "" && signature != geminiResponsesThoughtSignature {
					if st.PendingReasoningSignature != "" {
						if st.PendingReasoningSignature != signature {
							emitDetachedReasoning(st.PendingReasoningSignature, geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)
						}
						st.PendingReasoningSignature = ""
					}
					incomingSignature = signature
				} else if st.PendingReasoningSignature != "" {
					incomingSignature = st.PendingReasoningSignature
					st.PendingReasoningSignature = ""
				}
				if st.ReasoningOpened && !st.ReasoningClosed && incomingSignature != "" && st.ReasoningEnc != "" && incomingSignature != st.ReasoningEnc {
					finalizeReasoning()
					resetReasoning()
				}
				if st.ReasoningClosed {
					finalizeMessage()
					resetReasoning()
				} else if !st.ReasoningOpened && st.ReasoningBuf.Len() == 0 && st.MsgOpened && !st.MsgClosed {
					finalizeMessage()
				}
				if incomingSignature != "" {
					st.ReasoningEnc = incomingSignature
					st.ReasoningDirection = geminiResponsesCarrierStandalone
					st.ReasoningTargetKind = geminiResponsesCarrierText
					st.SeenReasoningSignatures[incomingSignature] = true
				}
				if t := part.Get("text"); t.Exists() && t.String() != "" {
					st.LastSemanticKind = geminiResponsesCarrierText
					st.ReasoningBuf.WriteString(t.String())
					if st.ReasoningOpened {
						msgDoc := ojson.NewDocString(`{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`)
						_ = msgDoc.Set("sequence_number", nextSeq())
						_ = msgDoc.Set("item_id", st.ReasoningItemID)
						_ = msgDoc.Set("output_index", st.ReasoningIndex)
						_ = msgDoc.Set("delta", t.String())
						out = append(out, emitEvent("response.reasoning_summary_text.delta", msgDoc.Bytes()))
					} else {
						st.ReasoningPendingDeltas = append(st.ReasoningPendingDeltas, strings.Clone(t.String()))
					}
				}
				if !st.ReasoningOpened && st.ReasoningEnc != "" {
					openReasoning()
				}
				return true
			}

			// Assistant visible text
			if t := part.Get("text"); t.Exists() && t.String() != "" {
				if signature == "" && st.PendingReasoningSignature != "" && ((st.MsgOpened && !st.MsgClosed) || len(st.WebSearchBufferedDeltas) > 0) {
					emitTrailingDetachedReasoning(st.PendingReasoningSignature)
					st.PendingReasoningSignature = ""
				}
				// Responses output items are sequential: finish reasoning before
				// opening the visible message. A signature that arrives later is
				// cached with the message and recombined on replay.
				finalizeReasoning()

				if st.MsgClosed {
					st.MsgOpened = false
					st.MsgClosed = false
					st.ItemTextBuf.Reset()
					st.CurrentMsgRuneOffset = 0
				}

				// In web search stream mode, buffer deltas until web_search_call is finalized
				// (stream end / a later output item) so the completed search item includes
				// incremental sources and strictly precedes the message.
				if st.WebSearchStreamMode && !st.WebSearchDone {
					textStr := strings.Clone(t.String())
					st.LastSemanticKind = geminiResponsesCarrierText
					st.WebSearchBufferedDeltas = append(st.WebSearchBufferedDeltas, textStr)
					n := len(st.WebSearchBufferedParts)
					if n > 0 && st.WebSearchBufferedParts[n-1].PartIndex == currentPartIndex {
						st.WebSearchBufferedParts[n-1].Text += textStr
					} else {
						st.WebSearchBufferedParts = append(st.WebSearchBufferedParts, geminiStreamBufferedPart{
							PartIndex: currentPartIndex,
							Text:      textStr,
						})
					}
					st.TextPartRunActive = true
					return true
				}

				if !st.MsgOpened {
					st.MsgOpened = true
					st.MsgIndex = st.NextIndex
					st.NextIndex++
					st.CurrentMsgID = fmt.Sprintf("msg_%s_%d", st.ResponseID, st.MsgIndex)
					itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`)
					_ = itemDoc.Set("sequence_number", nextSeq())
					_ = itemDoc.Set("output_index", st.MsgIndex)
					_ = itemDoc.Set("item.id", st.CurrentMsgID)
					out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))
					partAddedDoc := ojson.NewDocString(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
					_ = partAddedDoc.Set("sequence_number", nextSeq())
					_ = partAddedDoc.Set("item_id", st.CurrentMsgID)
					_ = partAddedDoc.Set("output_index", st.MsgIndex)
					out = append(out, emitEvent("response.content_part.added", partAddedDoc.Bytes()))
					st.ItemTextBuf.Reset()
					st.CurrentMsgRuneOffset = 0
				}
				st.LastSemanticKind = geminiResponsesCarrierText
				st.ItemTextBuf.WriteString(t.String())
				n := len(st.PartMappings)
				if n > 0 && st.PartMappings[n-1].PartIndex == currentPartIndex && st.PartMappings[n-1].MessageIndex == st.MsgIndex {
					st.PartMappings[n-1].PartText += t.String()
				} else {
					st.PartMappings = append(st.PartMappings, GeminiPartMapping{
						PartIndex:      currentPartIndex,
						MessageIndex:   st.MsgIndex,
						StartRuneInMsg: st.CurrentMsgRuneOffset,
						PartText:       strings.Clone(t.String()),
					})
				}
				st.CurrentMsgRuneOffset += int64(utf8.RuneCountInString(t.String()))
				msgDoc := ojson.NewDocString(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
				_ = msgDoc.Set("sequence_number", nextSeq())
				_ = msgDoc.Set("item_id", st.CurrentMsgID)
				_ = msgDoc.Set("output_index", st.MsgIndex)
				_ = msgDoc.Set("delta", t.String())
				out = append(out, emitEvent("response.output_text.delta", msgDoc.Bytes()))
				st.TextPartRunActive = true
				return true
			}

			// Function call
			if fc := part.Get("functionCall"); fc.Exists() {
				// Before emitting function-call outputs, finalize reasoning, web search, and the message (if open).
				// Responses streaming requires message done events before the next output_item.added.
				finalizeReasoning()
				finalizeWebSearch()
				if len(st.WebSearchBufferedDeltas) > 0 {
					flushWebSearchBufferedText()
				}
				finalizeMessage()
				st.LastSemanticKind = geminiResponsesCarrierFunction

				if isStreamingFuncContinuation || translatorcommon.IsVertexStreamingFunctionCall(fc) {
					if !isStreamingFuncContinuation {
						if st.ActiveStreamingFunc != nil {
							st.HasFailedStreamingFunc = true
							st.ActiveStreamingFunc = nil
							st.ActiveStreamingPatch = nil
						}
						rawName := strings.Clone(strings.TrimSpace(fc.Get("name").String()))
						if rawName == "" {
							st.HasFailedStreamingFunc = true
							return true
						}
						identity, hasIdentity := st.ToolIdentityMap[rawName]
						if !hasIdentity {
							restored := strings.Clone(util.RestoreSanitizedToolName(st.SanitizedNameMap, rawName))
							identity = util.ResponsesToolIdentity{Name: restored}
						}
						name := strings.Clone(identity.Name)
						namespace := strings.Clone(identity.Namespace)
						isCustom := identity.Custom
						isToolSearch := identity.ToolSearch

						idx := st.NextIndex
						st.NextIndex++
						if st.FuncArgsBuf[idx] == nil {
							st.FuncArgsBuf[idx] = &strings.Builder{}
						}
						upstreamID := strings.Clone(strings.TrimSpace(fc.Get("id").String()))
						callID := upstreamID
						if callID == "" {
							callID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&funcCallIDCounter, 1))
						}
						st.FuncCallIDs[idx] = callID
						st.FuncNames[idx] = name
						st.FuncNamespaces[idx] = namespace
						st.FuncCustom[idx] = isCustom
						st.FuncToolSearch[idx] = isToolSearch
						st.ActiveStreamingFunc = translatorcommon.NewVertexStreamingToolCall(rawName, name, upstreamID, callID, idx)

						if isToolSearch {
							itemID := openAIResponsesToolSearchCallItemID(callID)
							itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{}}`)
							_ = itemDoc.Set("sequence_number", nextSeq())
							_ = itemDoc.Set("output_index", idx)
							_ = itemDoc.SetRawBytes("item", buildOpenAIResponsesToolSearchCallItem(itemID, callID, "in_progress", "{}"))
							out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))
						} else if isCustom {
							if identity.ApplyPatch {
								st.ActiveStreamingPatch = &translatorcommon.ApplyPatchCallState{ItemID: fmt.Sprintf("ctc_%s", callID), CallID: callID, Name: name, Namespace: namespace, OutputIndex: idx}
							}
							itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"in_progress","input":"","call_id":"","name":""}}`)
							_ = itemDoc.Set("sequence_number", nextSeq())
							_ = itemDoc.Set("output_index", idx)
							_ = itemDoc.Set("item.id", fmt.Sprintf("ctc_%s", callID))
							_ = itemDoc.Set("item.call_id", callID)
							translatorcommon.SetResponsesToolCallIdentityDoc(itemDoc, name, namespace, "item")
							out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))
						} else {
							itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`)
							_ = itemDoc.Set("sequence_number", nextSeq())
							_ = itemDoc.Set("output_index", idx)
							_ = itemDoc.Set("item.id", fmt.Sprintf("fc_%s", callID))
							_ = itemDoc.Set("item.call_id", callID)
							translatorcommon.SetResponsesToolCallIdentityDoc(itemDoc, name, namespace, "item")
							out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))
						}
					}

					active := st.ActiveStreamingFunc
					idx := active.Index
					delta, fcDone := active.Assembler.PushFunctionCall(fc)
					if active.Assembler.Err() != nil {
						st.HasFailedStreamingFunc = true
						st.ActiveStreamingFunc = nil
						if st.ActiveStreamingPatch != nil {
							st.SetToolInputError(active.Assembler.Err())
							st.ActiveStreamingPatch = nil
							st.Completed = true
							out = append(out, emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, nextSeq())))
							return false
						}
						return true
					}
					if delta != "" {
						st.FuncArgsBuf[idx].WriteString(delta)
						if st.FuncCustom[idx] {
							if st.ActiveStreamingPatch != nil {
								inputDelta, errPush := st.ActiveStreamingPatch.PushArguments(delta)
								if errPush != nil {
									st.SetToolInputError(errPush)
									st.Completed = true
									out = append(out, emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, nextSeq())))
									return false
								}
								if inputDelta != "" {
									out = append(out, emitEvent("response.custom_tool_call_input.delta", translatorcommon.ApplyPatchInputDelta(st.ActiveStreamingPatch, inputDelta, nextSeq())))
								}
							}
						} else if !st.FuncToolSearch[idx] {
							adDoc := ojson.NewDocString(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
							_ = adDoc.Set("sequence_number", nextSeq())
							_ = adDoc.Set("item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
							_ = adDoc.Set("output_index", idx)
							_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(adDoc, "delta", delta)
							out = append(out, emitEvent("response.function_call_arguments.delta", adDoc.Bytes()))
						}
					}

					if fcDone {
						fullArgs := active.Assembler.FullArguments()
						st.ActiveStreamingFunc = nil
						if st.FuncToolSearch[idx] {
							itemStatus := "completed"
							if fr := root.Get("candidates.0.finishReason").String(); fr != "" {
								_, itemStatus, _ = geminiResponsesTerminalState(fr)
							} else if st.FinishReason != "" {
								_, itemStatus, _ = geminiResponsesTerminalState(st.FinishReason)
							}
							itemID := openAIResponsesToolSearchCallItemID(st.FuncCallIDs[idx])
							itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{}}`)
							_ = itemDoneDoc.Set("sequence_number", nextSeq())
							_ = itemDoneDoc.Set("output_index", idx)
							_ = itemDoneDoc.SetRawBytes("item", buildOpenAIResponsesToolSearchCallItem(itemID, st.FuncCallIDs[idx], itemStatus, fullArgs))
							out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))
						} else if st.FuncCustom[idx] {
							var inputStr string
							if st.ActiveStreamingPatch != nil {
								tail, fullInput, errFinishArguments := st.ActiveStreamingPatch.FinishArguments("")
								if errFinishArguments != nil {
									st.SetToolInputError(errFinishArguments)
									st.Completed = true
									out = append(out, emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, nextSeq())))
									return false
								}
								if tail != "" {
									out = append(out, emitEvent("response.custom_tool_call_input.delta", translatorcommon.ApplyPatchInputDelta(st.ActiveStreamingPatch, tail, nextSeq())))
								}
								inputStr = fullInput
							} else {
								inputStr = strings.Clone(util.UnwrapResponsesCustomToolInput(fullArgs))
							}
							st.FuncInputBuf[idx] = strings.Clone(inputStr)
							var inputDone []byte
							if st.ActiveStreamingPatch != nil {
								inputDone = translatorcommon.ApplyPatchInputDone(st.ActiveStreamingPatch, inputStr, nextSeq())
								st.ActiveStreamingPatch = nil
							} else {
								inputDoneDoc := ojson.NewDocString(`{"type":"response.custom_tool_call_input.done","sequence_number":0,"item_id":"","output_index":0,"input":""}`)
								_ = inputDoneDoc.Set("sequence_number", nextSeq())
								_ = inputDoneDoc.Set("item_id", fmt.Sprintf("ctc_%s", st.FuncCallIDs[idx]))
								_ = inputDoneDoc.Set("output_index", idx)
								_ = inputDoneDoc.Set("input", inputStr)
								inputDone = inputDoneDoc.Bytes()
							}
							out = append(out, emitEvent("response.custom_tool_call_input.done", inputDone))

							itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}}`)
							_ = itemDoneDoc.Set("sequence_number", nextSeq())
							_ = itemDoneDoc.Set("output_index", idx)
							_ = itemDoneDoc.Set("item.id", fmt.Sprintf("ctc_%s", st.FuncCallIDs[idx]))
							_ = itemDoneDoc.Set("item.input", inputStr)
							_ = itemDoneDoc.Set("item.call_id", st.FuncCallIDs[idx])
							translatorcommon.SetResponsesToolCallIdentityDoc(itemDoneDoc, st.FuncNames[idx], st.FuncNamespaces[idx], "item")
							out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))
						} else {
							fcDoneEventDoc := ojson.NewDocString(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
							_ = fcDoneEventDoc.Set("sequence_number", nextSeq())
							_ = fcDoneEventDoc.Set("item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
							_ = fcDoneEventDoc.Set("output_index", idx)
							_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(fcDoneEventDoc, "arguments", fullArgs)
							out = append(out, emitEvent("response.function_call_arguments.done", fcDoneEventDoc.Bytes()))

							itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
							_ = itemDoneDoc.Set("sequence_number", nextSeq())
							_ = itemDoneDoc.Set("output_index", idx)
							_ = itemDoneDoc.Set("item.id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
							_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(itemDoneDoc, "item.arguments", fullArgs)
							_ = itemDoneDoc.Set("item.call_id", st.FuncCallIDs[idx])
							translatorcommon.SetResponsesToolCallIdentityDoc(itemDoneDoc, st.FuncNames[idx], st.FuncNamespaces[idx], "item")
							out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))
						}
						st.FuncDone[idx] = true
						if st.PendingReasoningSignature != "" {
							emitTrailingDetachedReasoning(st.PendingReasoningSignature)
							st.PendingReasoningSignature = ""
						}
					}
					return true
				}

				if st.ActiveStreamingFunc != nil {
					st.HasFailedStreamingFunc = true
					st.ActiveStreamingFunc = nil
					st.ActiveStreamingPatch = nil
				}

				evidence := geminiRecordFunctionEvidence(st, fc, explicitPartIndex, ojson.ValidBytes(rawJSON))
				if evidence.ApplyPatch && evidence.Err != nil {
					st.SetToolInputError(evidence.Err)
					st.Completed = true
					out = append(out, emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, nextSeq())))
					return false
				}
				if evidence.RawName == "" {
					return true
				}
				rawName := strings.Clone(fc.Get("name").String())
				if evidence.ApplyPatch {
					rawName = evidence.RawName
				}
				identity, hasIdentity := st.ToolIdentityMap[rawName]
				if !hasIdentity {
					restored := strings.Clone(util.RestoreSanitizedToolName(st.SanitizedNameMap, rawName))
					identity = util.ResponsesToolIdentity{Name: restored}
				}
				name := strings.Clone(identity.Name)
				namespace := strings.Clone(identity.Namespace)
				isCustom := identity.Custom
				isToolSearch := identity.ToolSearch
				if evidence.ApplyPatch && evidence.PatchCall != nil {
					if _, _, errFinishArguments := evidence.PatchCall.FinishArguments(fc.Get("args").Raw); errFinishArguments != nil {
						st.SetToolInputError(errFinishArguments)
						st.Completed = true
						out = append(out, emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, nextSeq())))
						return false
					}
					return true
				}

				idx := st.NextIndex
				st.NextIndex++
				// Ensure buffers
				if st.FuncArgsBuf[idx] == nil {
					st.FuncArgsBuf[idx] = &strings.Builder{}
				}
				if identity.ApplyPatch || isToolSearch {
					st.FuncCallIDs[idx] = strings.Clone(evidence.UpstreamID)
				}
				if st.FuncCallIDs[idx] == "" {
					st.FuncCallIDs[idx] = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&funcCallIDCounter, 1))
				}
				st.FuncNames[idx] = name
				st.FuncNamespaces[idx] = namespace
				st.FuncCustom[idx] = isCustom
				st.FuncToolSearch[idx] = isToolSearch

				argsJSON := "{}"
				if args := fc.Get("args"); args.Exists() {
					argsJSON = args.Raw
				}
				if st.FuncArgsBuf[idx].Len() == 0 && argsJSON != "" {
					st.FuncArgsBuf[idx].WriteString(argsJSON)
				}

				if isToolSearch {
					itemStatus := "completed"
					if fr := root.Get("candidates.0.finishReason").String(); fr != "" {
						_, itemStatus, _ = geminiResponsesTerminalState(fr)
					} else if st.FinishReason != "" {
						_, itemStatus, _ = geminiResponsesTerminalState(st.FinishReason)
					}
					itemID := openAIResponsesToolSearchCallItemID(st.FuncCallIDs[idx])
					itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{}}`)
					_ = itemDoc.Set("sequence_number", nextSeq())
					_ = itemDoc.Set("output_index", idx)
					_ = itemDoc.SetRawBytes("item", buildOpenAIResponsesToolSearchCallItem(itemID, st.FuncCallIDs[idx], "in_progress", argsJSON))
					out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))

					if !st.FuncDone[idx] {
						itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{}}`)
						_ = itemDoneDoc.Set("sequence_number", nextSeq())
						_ = itemDoneDoc.Set("output_index", idx)
						_ = itemDoneDoc.SetRawBytes("item", buildOpenAIResponsesToolSearchCallItem(itemID, st.FuncCallIDs[idx], itemStatus, argsJSON))
						out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))

						st.FuncDone[idx] = true
					}
				} else if isCustom {
					inputStr := strings.Clone(util.UnwrapResponsesCustomToolInput(argsJSON))
					var patchCall *translatorcommon.ApplyPatchCallState
					if identity.ApplyPatch {
						patchCall = &translatorcommon.ApplyPatchCallState{ItemID: fmt.Sprintf("ctc_%s", st.FuncCallIDs[idx]), CallID: st.FuncCallIDs[idx], Name: name, Namespace: namespace, OutputIndex: idx}
						_, input, errFinishArguments := patchCall.FinishArguments(argsJSON)
						if !ojson.ValidBytes(rawJSON) {
							errFinishArguments = fmt.Errorf("invalid Gemini apply_patch response JSON")
						}
						if errFinishArguments != nil {
							st.SetToolInputError(errFinishArguments)
							st.Completed = true
							out = append(out, emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, nextSeq())))
							return false
						}
						inputStr = strings.Clone(input)
						evidence.PatchCall = patchCall
					}
					st.FuncInputBuf[idx] = strings.Clone(inputStr)

					// Emit item.added for custom tool call
					itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"in_progress","input":"","call_id":"","name":""}}`)
					_ = itemDoc.Set("sequence_number", nextSeq())
					_ = itemDoc.Set("output_index", idx)
					_ = itemDoc.Set("item.id", fmt.Sprintf("ctc_%s", st.FuncCallIDs[idx]))
					_ = itemDoc.Set("item.call_id", st.FuncCallIDs[idx])
					translatorcommon.SetResponsesToolCallIdentityDoc(itemDoc, name, namespace, "item")
					out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))

					// Gemini delivers complete arguments; this delta is not an early preview.
					if patchCall != nil && inputStr != "" {
						out = append(out, emitEvent("response.custom_tool_call_input.delta", translatorcommon.ApplyPatchInputDelta(patchCall, inputStr, nextSeq())))
					}
					// Emit custom tool call input.done
					if !st.FuncDone[idx] {
						var inputDone []byte
						if patchCall != nil {
							inputDone = translatorcommon.ApplyPatchInputDone(patchCall, inputStr, nextSeq())
						} else {
							inputDoneDoc := ojson.NewDocString(`{"type":"response.custom_tool_call_input.done","sequence_number":0,"item_id":"","output_index":0,"input":""}`)
							_ = inputDoneDoc.Set("sequence_number", nextSeq())
							_ = inputDoneDoc.Set("item_id", fmt.Sprintf("ctc_%s", st.FuncCallIDs[idx]))
							_ = inputDoneDoc.Set("output_index", idx)
							_ = inputDoneDoc.Set("input", inputStr)
							inputDone = inputDoneDoc.Bytes()
						}
						out = append(out, emitEvent("response.custom_tool_call_input.done", inputDone))

						itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}}`)
						_ = itemDoneDoc.Set("sequence_number", nextSeq())
						_ = itemDoneDoc.Set("output_index", idx)
						_ = itemDoneDoc.Set("item.id", fmt.Sprintf("ctc_%s", st.FuncCallIDs[idx]))
						_ = itemDoneDoc.Set("item.input", inputStr)
						_ = itemDoneDoc.Set("item.call_id", st.FuncCallIDs[idx])
						translatorcommon.SetResponsesToolCallIdentityDoc(itemDoneDoc, name, namespace, "item")
						out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))

						st.FuncDone[idx] = true
					}
				} else {
					// Emit item.added for function call
					itemDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`)
					_ = itemDoc.Set("sequence_number", nextSeq())
					_ = itemDoc.Set("output_index", idx)
					_ = itemDoc.Set("item.id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
					_ = itemDoc.Set("item.call_id", st.FuncCallIDs[idx])
					translatorcommon.SetResponsesToolCallIdentityDoc(itemDoc, name, namespace, "item")
					out = append(out, emitEvent("response.output_item.added", itemDoc.Bytes()))

					// Emit arguments delta (full args in one chunk).
					// When Gemini omits args, emit "{}" to keep Responses streaming event order consistent.
					if argsJSON != "" {
						adDoc := ojson.NewDocString(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
						_ = adDoc.Set("sequence_number", nextSeq())
						_ = adDoc.Set("item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
						_ = adDoc.Set("output_index", idx)
						_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(adDoc, "delta", argsJSON)
						out = append(out, emitEvent("response.function_call_arguments.delta", adDoc.Bytes()))
					}

					// Gemini emits the full function call payload at once, so we can finalize it immediately.
					if !st.FuncDone[idx] {
						fcDoneDoc := ojson.NewDocString(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
						_ = fcDoneDoc.Set("sequence_number", nextSeq())
						_ = fcDoneDoc.Set("item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
						_ = fcDoneDoc.Set("output_index", idx)
						_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(fcDoneDoc, "arguments", argsJSON)
						out = append(out, emitEvent("response.function_call_arguments.done", fcDoneDoc.Bytes()))

						itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
						_ = itemDoneDoc.Set("sequence_number", nextSeq())
						_ = itemDoneDoc.Set("output_index", idx)
						_ = itemDoneDoc.Set("item.id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
						_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(itemDoneDoc, "item.arguments", argsJSON)
						_ = itemDoneDoc.Set("item.call_id", st.FuncCallIDs[idx])
						translatorcommon.SetResponsesToolCallIdentityDoc(itemDoneDoc, name, namespace, "item")
						out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))

						st.FuncDone[idx] = true
					}
				}

				return true
			}

			return true
		})
	}

	if st.Completed {
		return out
	}

	// Preserve the first source finish, including across a usage-only tail or [DONE].
	if fr := root.Get("candidates.0.finishReason").String(); fr != "" && st.FinishReason == "" {
		st.FinishReason = strings.Clone(fr)
	}
	if st.FinishReason != "" && st.ActiveStreamingFunc == nil && !st.HasFailedStreamingFunc {
		if errIdentity := geminiPendingIdentityError(st); errIdentity != nil {
			st.SetToolInputError(errIdentity)
			st.Completed = true
			return append(out, emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, nextSeq())))
		}
		if !done && !hasUsage {
			return out
		}
		eventType, status, incompleteDetails := geminiResponsesTerminalState(st.FinishReason)
		messageStatus = status
		if st.PendingReasoningSignature != "" {
			emitTrailingDetachedReasoning(st.PendingReasoningSignature)
			st.PendingReasoningSignature = ""
		}
		// Finalize web search with the complete incremental sources, then reasoning,
		// then the message so web_search_call precedes later output items.
		finalizeWebSearch()
		finalizeReasoning()
		finalizeMessage()

		// Close function calls
		if len(st.FuncArgsBuf) > 0 {
			// sort indices (small N); avoid extra imports
			idxs := make([]int, 0, len(st.FuncArgsBuf))
			for idx := range st.FuncArgsBuf {
				idxs = append(idxs, idx)
			}
			for i := 0; i < len(idxs); i++ {
				for j := i + 1; j < len(idxs); j++ {
					if idxs[j] < idxs[i] {
						idxs[i], idxs[j] = idxs[j], idxs[i]
					}
				}
			}
			for _, idx := range idxs {
				if st.FuncDone[idx] {
					continue
				}
				if st.FuncToolSearch[idx] {
					args := "{}"
					if b := st.FuncArgsBuf[idx]; b != nil && b.Len() > 0 {
						args = b.String()
					}
					itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{}}`)
					_ = itemDoneDoc.Set("sequence_number", nextSeq())
					_ = itemDoneDoc.Set("output_index", idx)
					_ = itemDoneDoc.SetRawBytes("item", buildOpenAIResponsesToolSearchCallItem(openAIResponsesToolSearchCallItemID(st.FuncCallIDs[idx]), st.FuncCallIDs[idx], status, args))
					out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))
				} else if st.FuncCustom[idx] {
					inputStr := st.FuncInputBuf[idx]
					inputDoneDoc := ojson.NewDocString(`{"type":"response.custom_tool_call_input.done","sequence_number":0,"item_id":"","output_index":0,"input":""}`)
					_ = inputDoneDoc.Set("sequence_number", nextSeq())
					_ = inputDoneDoc.Set("item_id", fmt.Sprintf("ctc_%s", st.FuncCallIDs[idx]))
					_ = inputDoneDoc.Set("output_index", idx)
					_ = inputDoneDoc.Set("input", inputStr)
					out = append(out, emitEvent("response.custom_tool_call_input.done", inputDoneDoc.Bytes()))

					itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}}`)
					_ = itemDoneDoc.Set("sequence_number", nextSeq())
					_ = itemDoneDoc.Set("output_index", idx)
					_ = itemDoneDoc.Set("item.id", fmt.Sprintf("ctc_%s", st.FuncCallIDs[idx]))
					_ = itemDoneDoc.Set("item.input", inputStr)
					_ = itemDoneDoc.Set("item.call_id", st.FuncCallIDs[idx])
					translatorcommon.SetResponsesToolCallIdentityDoc(itemDoneDoc, st.FuncNames[idx], st.FuncNamespaces[idx], "item")
					out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))
				} else {
					args := "{}"
					if b := st.FuncArgsBuf[idx]; b != nil && b.Len() > 0 {
						args = b.String()
					}
					fcDoneDoc := ojson.NewDocString(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
					_ = fcDoneDoc.Set("sequence_number", nextSeq())
					_ = fcDoneDoc.Set("item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
					_ = fcDoneDoc.Set("output_index", idx)
					_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(fcDoneDoc, "arguments", args)
					out = append(out, emitEvent("response.function_call_arguments.done", fcDoneDoc.Bytes()))

					itemDoneDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
					_ = itemDoneDoc.Set("sequence_number", nextSeq())
					_ = itemDoneDoc.Set("output_index", idx)
					_ = itemDoneDoc.Set("item.id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
					_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(itemDoneDoc, "item.arguments", args)
					_ = itemDoneDoc.Set("item.call_id", st.FuncCallIDs[idx])
					translatorcommon.SetResponsesToolCallIdentityDoc(itemDoneDoc, st.FuncNames[idx], st.FuncNamespaces[idx], "item")
					out = append(out, emitEvent("response.output_item.done", itemDoneDoc.Bytes()))
				}
				st.FuncDone[idx] = true
			}
		}

		// Reasoning already finalized above if present

		// Build the terminal response with aggregated outputs and request echo fields
		completedDoc := ojson.NewDocString(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null}}`)
		_ = completedDoc.Set("type", eventType)
		_ = completedDoc.Set("response.status", status)
		if incompleteDetails != nil {
			_ = completedDoc.SetRawBytes("response.incomplete_details", incompleteDetails)
		}
		_ = completedDoc.Set("sequence_number", nextSeq())
		_ = completedDoc.Set("response.id", st.ResponseID)
		_ = completedDoc.Set("response.created_at", st.CreatedAt)

		if len(reqJSON) > 0 {
			req := unwrapRequestRoot(util.ParseGJSONBytesNoCopy(reqJSON))
			if v := req.Get("instructions"); v.Exists() {
				_ = completedDoc.Set("response.instructions", v.String())
			}
			if v := req.Get("max_output_tokens"); v.Exists() {
				_ = completedDoc.Set("response.max_output_tokens", v.Int())
			}
			if v := req.Get("max_tool_calls"); v.Exists() {
				_ = completedDoc.Set("response.max_tool_calls", v.Int())
			}
			if v := req.Get("model"); v.Exists() {
				_ = completedDoc.Set("response.model", v.String())
			}
			if v := req.Get("parallel_tool_calls"); v.Exists() {
				_ = completedDoc.Set("response.parallel_tool_calls", v.Bool())
			}
			if v := req.Get("previous_response_id"); v.Exists() {
				_ = completedDoc.Set("response.previous_response_id", v.String())
			}
			if v := req.Get("prompt_cache_key"); v.Exists() {
				_ = completedDoc.Set("response.prompt_cache_key", v.String())
			}
			if v := req.Get("reasoning"); v.Exists() {
				_ = completedDoc.Set("response.reasoning", v.Value())
			}
			if v := req.Get("safety_identifier"); v.Exists() {
				_ = completedDoc.Set("response.safety_identifier", v.String())
			}
			if v := req.Get("service_tier"); v.Exists() {
				_ = completedDoc.Set("response.service_tier", v.String())
			}
			if v := req.Get("store"); v.Exists() {
				_ = completedDoc.Set("response.store", v.Bool())
			}
			if v := req.Get("temperature"); v.Exists() {
				_ = completedDoc.Set("response.temperature", v.Float())
			}
			if v := req.Get("text"); v.Exists() {
				_ = completedDoc.Set("response.text", v.Value())
			}
			if v := req.Get("tool_choice"); v.Exists() {
				_ = completedDoc.Set("response.tool_choice", v.Value())
			}
			if v := req.Get("tools"); v.Exists() {
				_ = completedDoc.Set("response.tools", v.Value())
			}
			if v := req.Get("top_logprobs"); v.Exists() {
				_ = completedDoc.Set("response.top_logprobs", v.Int())
			}
			if v := req.Get("top_p"); v.Exists() {
				_ = completedDoc.Set("response.top_p", v.Float())
			}
			if v := req.Get("truncation"); v.Exists() {
				_ = completedDoc.Set("response.truncation", v.String())
			}
			if v := req.Get("user"); v.Exists() {
				_ = completedDoc.Set("response.user", v.Value())
			}
			if v := req.Get("metadata"); v.Exists() {
				_ = completedDoc.Set("response.metadata", v.Value())
			}
		}

		emitLateCitations()

		// Compose outputs in output_index order.
		outputs := make([][]byte, 0, st.NextIndex)
		for idx := 0; idx < st.NextIndex; idx++ {
			if st.WebSearchDone && idx == st.WebSearchIndex {
				outputs = append(outputs, BuildResponsesWebSearchCallItem(st.WebSearchItemID, st.WebSearchQuery, st.WebSearchQueries, st.WebSearchSources))
				continue
			}
			if completedReasoning, ok := st.CompletedReasoning[idx]; ok {
				itemDoc := ojson.NewDocString(`{"id":"","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":""}]}`)
				_ = itemDoc.Set("id", completedReasoning.ID)
				_ = itemDoc.Set("encrypted_content", completedReasoning.Signature)
				_ = itemDoc.Set("summary.0.text", completedReasoning.Text)
				outputs = append(outputs, itemDoc.Bytes())
				continue
			}
			if completedMessage, ok := st.CompletedMessages[idx]; ok {
				itemDoc := ojson.NewDocString(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
				_ = itemDoc.Set("id", completedMessage.ID)
				_ = itemDoc.Set("status", completedMessage.Status)
				_ = itemDoc.Set("content.0.text", completedMessage.Text)
				if len(completedMessage.Annotations) > 0 {
					_ = itemDoc.SetRawBytes("content.0.annotations", translatorcommon.JoinRawArray(completedMessage.Annotations))
				}
				outputs = append(outputs, itemDoc.Bytes())
				continue
			}
			if detached, ok := st.DetachedReasoning[idx]; ok {
				itemDoc := ojson.NewDocString(`{"id":"","type":"reasoning","encrypted_content":"","summary":[]}`)
				_ = itemDoc.Set("id", detached.ID)
				_ = itemDoc.Set("encrypted_content", detached.Signature)
				outputs = append(outputs, itemDoc.Bytes())
				continue
			}

			if callID, ok := st.FuncCallIDs[idx]; ok && callID != "" {
				if st.FuncToolSearch[idx] {
					args := "{}"
					if b := st.FuncArgsBuf[idx]; b != nil && b.Len() > 0 {
						args = b.String()
					}
					outputs = append(outputs, buildOpenAIResponsesToolSearchCallItem(openAIResponsesToolSearchCallItemID(callID), callID, status, args))
				} else if st.FuncCustom[idx] {
					inputStr := st.FuncInputBuf[idx]
					itemDoc := ojson.NewDocString(`{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}`)
					_ = itemDoc.Set("id", fmt.Sprintf("ctc_%s", callID))
					_ = itemDoc.Set("input", inputStr)
					_ = itemDoc.Set("call_id", callID)
					translatorcommon.SetResponsesToolCallIdentityDoc(itemDoc, st.FuncNames[idx], st.FuncNamespaces[idx], "")
					outputs = append(outputs, itemDoc.Bytes())
				} else {
					args := "{}"
					if b := st.FuncArgsBuf[idx]; b != nil && b.Len() > 0 {
						args = b.String()
					}
					itemDoc := ojson.NewDocString(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
					_ = itemDoc.Set("id", fmt.Sprintf("fc_%s", callID))
					_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(itemDoc, "arguments", args)
					_ = itemDoc.Set("call_id", callID)
					translatorcommon.SetResponsesToolCallIdentityDoc(itemDoc, st.FuncNames[idx], st.FuncNamespaces[idx], "")
					outputs = append(outputs, itemDoc.Bytes())
				}
			}
		}
		if len(outputs) > 0 {
			_ = completedDoc.SetRawBytes("response.output", translatorcommon.JoinRawArray(outputs))
		}
		if st.WebSearchDone {
			_ = completedDoc.Set("response.tool_usage.web_search.num_requests", 1)
		}

		if st.Usage.Present {
			_ = completedDoc.SetRawBytes("response.usage", st.Usage.JSON())
		}

		out = append(out, emitEvent(eventType, completedDoc.Bytes()))
		st.Completed = true
	}

	return out
}

// ConvertGeminiResponseToOpenAIResponsesNonStream aggregates Gemini response JSON into a single OpenAI Responses JSON object.
func ConvertGeminiResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	root = unwrapGeminiResponseRoot(root)
	reqJSON := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	sanitizedNameMap := util.SanitizedToolNameMap(originalRequestRawJSON)
	toolIdentityMap := util.ResponsesToolReverseIdentityMap(reqJSON)

	// Base response scaffold
	respDoc := ojson.NewDocString(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null}`)
	_, status, incompleteDetails := geminiResponsesTerminalState(root.Get("candidates.0.finishReason").String())
	_ = respDoc.Set("status", status)
	if incompleteDetails != nil {
		_ = respDoc.SetRawBytes("incomplete_details", incompleteDetails)
	}

	// id: prefer provider responseId, otherwise synthesize
	id := root.Get("responseId").String()
	if id == "" {
		id = fmt.Sprintf("resp_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&responseIDCounter, 1))
	}
	// Normalize to response-style id (prefix resp_ if missing)
	if !strings.HasPrefix(id, "resp_") {
		id = fmt.Sprintf("resp_%s", id)
	}
	_ = respDoc.Set("id", id)

	// created_at: map from createTime if available
	createdAt := time.Now().Unix()
	if v := root.Get("createTime"); v.Exists() {
		if t, errParseCreateTime := time.Parse(time.RFC3339Nano, v.String()); errParseCreateTime == nil {
			createdAt = t.Unix()
		}
	}
	_ = respDoc.Set("created_at", createdAt)

	// Echo request fields when present; fallback model from response modelVersion
	if len(reqJSON) > 0 {
		req := unwrapRequestRoot(util.ParseGJSONBytesNoCopy(reqJSON))
		if v := req.Get("instructions"); v.Exists() {
			_ = respDoc.Set("instructions", v.String())
		}
		if v := req.Get("max_output_tokens"); v.Exists() {
			_ = respDoc.Set("max_output_tokens", v.Int())
		}
		if v := req.Get("max_tool_calls"); v.Exists() {
			_ = respDoc.Set("max_tool_calls", v.Int())
		}
		if v := req.Get("model"); v.Exists() {
			_ = respDoc.Set("model", v.String())
		} else if v = root.Get("modelVersion"); v.Exists() {
			_ = respDoc.Set("model", v.String())
		}
		if v := req.Get("parallel_tool_calls"); v.Exists() {
			_ = respDoc.Set("parallel_tool_calls", v.Bool())
		}
		if v := req.Get("previous_response_id"); v.Exists() {
			_ = respDoc.Set("previous_response_id", v.String())
		}
		if v := req.Get("prompt_cache_key"); v.Exists() {
			_ = respDoc.Set("prompt_cache_key", v.String())
		}
		if v := req.Get("reasoning"); v.Exists() {
			_ = respDoc.Set("reasoning", v.Value())
		}
		if v := req.Get("safety_identifier"); v.Exists() {
			_ = respDoc.Set("safety_identifier", v.String())
		}
		if v := req.Get("service_tier"); v.Exists() {
			_ = respDoc.Set("service_tier", v.String())
		}
		if v := req.Get("store"); v.Exists() {
			_ = respDoc.Set("store", v.Bool())
		}
		if v := req.Get("temperature"); v.Exists() {
			_ = respDoc.Set("temperature", v.Float())
		}
		if v := req.Get("text"); v.Exists() {
			_ = respDoc.Set("text", v.Value())
		}
		if v := req.Get("tool_choice"); v.Exists() {
			_ = respDoc.Set("tool_choice", v.Value())
		}
		if v := req.Get("tools"); v.Exists() {
			_ = respDoc.Set("tools", v.Value())
		}
		if v := req.Get("top_logprobs"); v.Exists() {
			_ = respDoc.Set("top_logprobs", v.Int())
		}
		if v := req.Get("top_p"); v.Exists() {
			_ = respDoc.Set("top_p", v.Float())
		}
		if v := req.Get("truncation"); v.Exists() {
			_ = respDoc.Set("truncation", v.String())
		}
		if v := req.Get("user"); v.Exists() {
			_ = respDoc.Set("user", v.Value())
		}
		if v := req.Get("metadata"); v.Exists() {
			_ = respDoc.Set("metadata", v.Value())
		}
	} else if v := root.Get("modelVersion"); v.Exists() {
		_ = respDoc.Set("model", v.String())
	}

	// Build outputs from candidates[0].content.parts
	var reasoningText strings.Builder
	var reasoningEncrypted string
	var reasoningDirection string
	var reasoningTargetKind string
	type nonStreamReasoningOutput struct {
		text       string
		signature  string
		direction  string
		targetKind string
	}
	type nonStreamFunctionOutput struct {
		item      []byte
		signature string
	}
	type nonStreamOutputOrder struct {
		kind  string
		index int
	}
	type nonStreamDetachedOutput struct {
		signature  string
		direction  string
		targetKind string
	}
	type nonStreamMessageOutput struct {
		text       string
		signatures []string
	}
	var reasoningOutputs []nonStreamReasoningOutput
	var functionOutputs []nonStreamFunctionOutput
	var messageOutputs []nonStreamMessageOutput
	var outputOrder []nonStreamOutputOrder
	reasoningOutputSignatures := make(map[string]bool)
	flushReasoningOutput := func() {
		if reasoningText.Len() == 0 && reasoningEncrypted == "" {
			return
		}
		reasoningIndex := len(reasoningOutputs)
		reasoningOutputs = append(reasoningOutputs, nonStreamReasoningOutput{text: reasoningText.String(), signature: reasoningEncrypted, direction: reasoningDirection, targetKind: reasoningTargetKind})
		outputOrder = append(outputOrder, nonStreamOutputOrder{kind: "reasoning", index: reasoningIndex})
		if reasoningEncrypted != "" {
			reasoningOutputSignatures[reasoningEncrypted] = true
		}
		reasoningText.Reset()
		reasoningEncrypted = ""
		reasoningDirection = ""
		reasoningTargetKind = ""
	}
	var detachedReasoningOutputs []nonStreamDetachedOutput
	var currentMessageText strings.Builder
	var currentMessageSignatures []string
	var partMappings []GeminiPartMapping
	var currentMsgRuneOffset int64
	flushMessageOutput := func() {
		if currentMessageText.Len() == 0 {
			return
		}
		messageIndex := len(messageOutputs)
		messageOutputs = append(messageOutputs, nonStreamMessageOutput{text: currentMessageText.String(), signatures: append([]string(nil), currentMessageSignatures...)})
		outputOrder = append(outputOrder, nonStreamOutputOrder{kind: "message", index: messageIndex})
		currentMessageText.Reset()
		currentMessageSignatures = nil
		currentMsgRuneOffset = 0
	}

	var toolInputError error
	evidenceState := &geminiToResponsesState{ToolIdentityMap: toolIdentityMap}
	var outputs [][]byte
	appendOutput := func(itemJSON []byte) {
		outputs = append(outputs, itemJSON)
	}
	detachedOutputIndex := 0
	seenDetachedOutputs := make(map[string]bool)
	appendDetachedOutput := func(signature, direction, targetKind string) {
		if signature == "" || seenDetachedOutputs[signature] {
			return
		}
		seenDetachedOutputs[signature] = true
		placement := "before"
		if direction == geminiResponsesCarrierPrevious {
			placement = "after"
		}
		itemDoc := ojson.NewDocString(`{"id":"","type":"reasoning","encrypted_content":"","summary":[]}`)
		_ = itemDoc.Set("id", fmt.Sprintf("rs_%s_detached_%s_%d", strings.TrimPrefix(id, "resp_"), placement, detachedOutputIndex))
		_ = itemDoc.Set("encrypted_content", encodeGeminiResponsesCarrier(signature, direction, targetKind))
		detachedOutputIndex++
		appendOutput(itemDoc.Bytes())
	}

	if parts := root.Get("candidates.0.content.parts"); parts.Exists() && parts.IsArray() {
		parts.ForEach(func(key, p ojson.Result) bool {
			partIdx := int(key.Int())
			if pIdx := p.Get("partIndex"); pIdx.Exists() {
				partIdx = int(pIdx.Int())
			} else if pIdx := p.Get("index"); pIdx.Exists() {
				partIdx = int(pIdx.Int())
			}
			signature := strings.TrimSpace(p.Get("thoughtSignature").String())
			if signature == "" {
				signature = strings.TrimSpace(p.Get("thought_signature").String())
			}
			if p.Get("thought").Bool() {
				flushMessageOutput()
				currentMsgRuneOffset = 0
				if signature != "" && reasoningEncrypted != "" && signature != reasoningEncrypted {
					flushReasoningOutput()
				}
				if t := p.Get("text"); t.Exists() {
					reasoningText.WriteString(t.String())
				}
				if signature != "" {
					reasoningEncrypted = signature
					reasoningDirection = geminiResponsesCarrierStandalone
					reasoningTargetKind = geminiResponsesCarrierText
				}
				return true
			}
			if t := p.Get("text"); t.Exists() && t.String() != "" {
				messageSignature := ""
				if signature != "" {
					if reasoningText.Len() > 0 && reasoningEncrypted == "" {
						reasoningEncrypted = signature
						reasoningDirection = geminiResponsesCarrierNext
						reasoningTargetKind = geminiResponsesCarrierText
					} else {
						messageSignature = signature
					}
				}
				flushReasoningOutput()
				if len(currentMessageSignatures) > 0 && (messageSignature == "" || currentMessageSignatures[len(currentMessageSignatures)-1] != messageSignature) {
					flushMessageOutput()
					currentMsgRuneOffset = 0
				}
				partText := t.String()
				partMappings = append(partMappings, GeminiPartMapping{
					PartIndex:      partIdx,
					MessageIndex:   len(messageOutputs),
					StartRuneInMsg: currentMsgRuneOffset,
					PartText:       partText,
				})
				currentMsgRuneOffset += int64(utf8.RuneCountInString(partText))
				currentMessageText.WriteString(partText)
				if messageSignature != "" && (len(currentMessageSignatures) == 0 || currentMessageSignatures[len(currentMessageSignatures)-1] != messageSignature) {
					currentMessageSignatures = append(currentMessageSignatures, messageSignature)
				}
				return true
			}
			if fc := p.Get("functionCall"); fc.Exists() {
				if reasoningText.Len() > 0 && reasoningEncrypted == "" && signature != "" {
					reasoningEncrypted = signature
					reasoningDirection = geminiResponsesCarrierNext
					reasoningTargetKind = geminiResponsesCarrierFunction
					signature = ""
				}
				flushReasoningOutput()
				flushMessageOutput()
				currentMsgRuneOffset = 0

				explicitIndex := -1
				if p.Get("partIndex").Exists() {
					explicitIndex = int(p.Get("partIndex").Int())
				} else if p.Get("index").Exists() {
					explicitIndex = int(p.Get("index").Int())
				}
				evidence := geminiRecordFunctionEvidence(evidenceState, fc, explicitIndex, ojson.ValidBytes(rawJSON))
				if evidence.ApplyPatch && evidence.Err != nil {
					toolInputError = evidence.Err
					return false
				}
				if evidence.RawName == "" {
					return true
				}
				rawName := fc.Get("name").String()
				if evidence.ApplyPatch {
					rawName = evidence.RawName
				}
				identity, hasIdentity := toolIdentityMap[rawName]
				if !hasIdentity {
					restored := util.RestoreSanitizedToolName(sanitizedNameMap, rawName)
					identity = util.ResponsesToolIdentity{Name: restored}
				}
				name := identity.Name
				namespace := identity.Namespace
				isCustom := identity.Custom
				if identity.ApplyPatch && evidence.PatchCall != nil {
					_, _, errFinishArguments := evidence.PatchCall.FinishArguments(fc.Get("args").Raw)
					if errFinishArguments != nil {
						toolInputError = errFinishArguments
						return false
					}
					return true
				}

				args := fc.Get("args")
				argsStr := ""
				if args.Exists() {
					argsStr = args.Raw
				}
				callID := fmt.Sprintf("call_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&funcCallIDCounter, 1))
				if (identity.ApplyPatch || identity.ToolSearch) && evidence.UpstreamID != "" {
					callID = evidence.UpstreamID
				}
				var itemJSON []byte
				if identity.ToolSearch {
					itemJSON = buildOpenAIResponsesToolSearchCallItem(openAIResponsesToolSearchCallItemID(callID), callID, status, argsStr)
				} else if isCustom {
					inputStr := util.UnwrapResponsesCustomToolInput(argsStr)
					if identity.ApplyPatch {
						var patchCall translatorcommon.ApplyPatchCallState
						_, input, errFinishArguments := patchCall.FinishArguments(argsStr)
						if !ojson.ValidBytes(rawJSON) {
							errFinishArguments = fmt.Errorf("invalid Gemini apply_patch response JSON")
						}
						if errFinishArguments != nil {
							toolInputError = errFinishArguments
							return false
						}
						inputStr = input
						evidence.PatchCall = &patchCall
					}
					itemDoc := ojson.NewDocString(`{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}`)
					_ = itemDoc.Set("id", fmt.Sprintf("ctc_%s", callID))
					_ = itemDoc.Set("call_id", callID)
					_ = itemDoc.Set("input", inputStr)
					translatorcommon.SetResponsesToolCallIdentityDoc(itemDoc, name, namespace, "")
					itemJSON = itemDoc.Bytes()
				} else {
					itemDoc := ojson.NewDocString(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
					_ = itemDoc.Set("id", fmt.Sprintf("fc_%s", callID))
					_ = itemDoc.Set("call_id", callID)
					_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(itemDoc, "arguments", argsStr)
					translatorcommon.SetResponsesToolCallIdentityDoc(itemDoc, name, namespace, "")
					itemJSON = itemDoc.Bytes()
				}
				functionIndex := len(functionOutputs)
				functionOutputs = append(functionOutputs, nonStreamFunctionOutput{item: itemJSON, signature: signature})
				outputOrder = append(outputOrder, nonStreamOutputOrder{kind: "function", index: functionIndex})
				return true
			}
			if signature != "" {
				if reasoningText.Len() > 0 {
					switch {
					case reasoningEncrypted == "":
						reasoningEncrypted = signature
						reasoningDirection = geminiResponsesCarrierStandalone
						reasoningTargetKind = geminiResponsesCarrierText
					case reasoningEncrypted != signature:
						flushReasoningOutput()
						detachedIndex := len(detachedReasoningOutputs)
						detachedReasoningOutputs = append(detachedReasoningOutputs, nonStreamDetachedOutput{signature: signature, direction: geminiResponsesCarrierPrevious, targetKind: geminiResponsesCarrierText})
						outputOrder = append(outputOrder, nonStreamOutputOrder{kind: "detached", index: detachedIndex})
					}
				} else if currentMessageText.Len() > 0 {
					if len(currentMessageSignatures) == 0 {
						currentMessageSignatures = append(currentMessageSignatures, signature)
					} else if currentMessageSignatures[len(currentMessageSignatures)-1] != signature {
						flushMessageOutput()
						currentMsgRuneOffset = 0
						detachedIndex := len(detachedReasoningOutputs)
						detachedReasoningOutputs = append(detachedReasoningOutputs, nonStreamDetachedOutput{signature: signature, direction: geminiResponsesCarrierPrevious, targetKind: geminiResponsesCarrierText})
						outputOrder = append(outputOrder, nonStreamOutputOrder{kind: "detached", index: detachedIndex})
					}
				} else if len(functionOutputs) > 0 {
					detachedIndex := len(detachedReasoningOutputs)
					detachedReasoningOutputs = append(detachedReasoningOutputs, nonStreamDetachedOutput{signature: signature, direction: geminiResponsesCarrierPrevious, targetKind: geminiResponsesCarrierFunction})
					outputOrder = append(outputOrder, nonStreamOutputOrder{kind: "detached", index: detachedIndex})
				} else {
					detachedIndex := len(detachedReasoningOutputs)
					detachedReasoningOutputs = append(detachedReasoningOutputs, nonStreamDetachedOutput{signature: signature, direction: geminiResponsesCarrierNext, targetKind: geminiResponsesCarrierAny})
					outputOrder = append(outputOrder, nonStreamOutputOrder{kind: "detached", index: detachedIndex})
				}
			}
			return true
		})
	}

	if toolInputError == nil {
		toolInputError = geminiPendingIdentityError(evidenceState)
	}
	if toolInputError != nil {
		if param != nil {
			state := &translatorcommon.ApplyPatchErrorState{}
			state.SetToolInputError(toolInputError)
			*param = state
		}
		return nil
	}
	activeMessageIndex := -1
	if currentMessageText.Len() > 0 {
		activeMessageIndex = len(messageOutputs)
	}
	flushReasoningOutput()
	flushMessageOutput()

	// Web search handling from groundingMetadata
	groundingMetadata := ExtractGroundingMetadata(root)
	hasGrounding := HasValidWebGrounding(groundingMetadata)
	var wsItem []byte
	var messageCitations map[int][][]byte
	if hasGrounding {
		queries := ExtractGroundingQueries(groundingMetadata)
		query := ""
		if len(queries) > 0 {
			query = queries[0]
		}
		if query == "" && len(reqJSON) > 0 {
			query = ExtractResponsesWebSearchQuery(unwrapRequestRoot(util.ParseGJSONBytesNoCopy(reqJSON)))
		}
		sources := ExtractGroundingSources(groundingMetadata)
		wsID := fmt.Sprintf("ws_%s", strings.TrimPrefix(id, "resp_"))
		wsItem = BuildResponsesWebSearchCallItem(wsID, query, queries, sources)

		messageTexts := make([]string, len(messageOutputs))
		for i, mo := range messageOutputs {
			messageTexts[i] = mo.text
		}
		messageCitations = BuildResponsesURLCitationsForMessages(groundingMetadata, partMappings, messageTexts)
	}

	wsAppended := false
	for _, outputItem := range outputOrder {
		switch outputItem.kind {
		case "detached":
			if outputItem.index < 0 || outputItem.index >= len(detachedReasoningOutputs) {
				continue
			}
			detached := detachedReasoningOutputs[outputItem.index]
			if !reasoningOutputSignatures[detached.signature] {
				appendDetachedOutput(detached.signature, detached.direction, detached.targetKind)
			}
		case "reasoning":
			if outputItem.index < 0 || outputItem.index >= len(reasoningOutputs) {
				continue
			}
			reasoningOutput := reasoningOutputs[outputItem.index]
			rid := strings.TrimPrefix(id, "resp_")
			reasoningID := fmt.Sprintf("rs_%s", rid)
			if len(reasoningOutputs) > 1 {
				reasoningID = fmt.Sprintf("rs_%s_%d", rid, outputItem.index)
			}
			itemDoc := ojson.NewDocString(`{"id":"","type":"reasoning","encrypted_content":""}`)
			_ = itemDoc.Set("id", reasoningID)
			encryptedContent := reasoningOutput.signature
			if encryptedContent != "" && reasoningOutput.direction != "" {
				encryptedContent = encodeGeminiResponsesCarrier(encryptedContent, reasoningOutput.direction, reasoningOutput.targetKind)
			}
			_ = itemDoc.Set("encrypted_content", encryptedContent)
			if reasoningOutput.text != "" {
				summaryJSON := []byte(`{"type":"summary_text","text":""}`)
				summaryJSON, _ = ojson.SetBytes(summaryJSON, "text", reasoningOutput.text)
				_ = itemDoc.SetRawBytes("summary", translatorcommon.JoinRawArray([][]byte{summaryJSON}))
			}
			appendOutput(itemDoc.Bytes())
		case "message":
			if hasGrounding && !wsAppended {
				appendOutput(wsItem)
				wsAppended = true
			}
			if outputItem.index < 0 || outputItem.index >= len(messageOutputs) {
				continue
			}
			messageOutput := messageOutputs[outputItem.index]
			for _, signature := range messageOutput.signatures {
				if !reasoningOutputSignatures[signature] {
					appendDetachedOutput(signature, geminiResponsesCarrierNext, geminiResponsesCarrierText)
				}
			}
			itemDoc := ojson.NewDocString(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
			_ = itemDoc.Set("id", fmt.Sprintf("msg_%s_%d", strings.TrimPrefix(id, "resp_"), outputItem.index))
			_ = itemDoc.Set("content.0.text", messageOutput.text)
			if outputItem.index == activeMessageIndex {
				_ = itemDoc.Set("status", status)
			}
			if c := messageCitations[outputItem.index]; len(c) > 0 {
				_ = itemDoc.SetRawBytes("content.0.annotations", translatorcommon.JoinRawArray(c))
			}
			appendOutput(itemDoc.Bytes())
		case "function":
			if outputItem.index < 0 || outputItem.index >= len(functionOutputs) {
				continue
			}
			functionOutput := functionOutputs[outputItem.index]
			appendDetachedOutput(functionOutput.signature, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
			appendOutput(functionOutput.item)
		}
	}

	if hasGrounding && !wsAppended {
		appendOutput(wsItem)
		wsAppended = true
	}

	if len(outputs) > 0 {
		_ = respDoc.SetRawBytes("output", translatorcommon.JoinRawArray(outputs))
	}

	if hasGrounding {
		_ = respDoc.Set("tool_usage.web_search.num_requests", 1)
	}

	var usage geminiResponsesUsage
	if usage.Merge(root) {
		_ = respDoc.SetRawBytes("usage", usage.JSON())
	}

	return respDoc.Bytes()
}

// Full snapshots are evidence, never source prefixes. Stable IDs and explicit
// part indexes identify repeated snapshots without merging distinct unkeyed calls.
func geminiRecordFunctionEvidence(st *geminiToResponsesState, fc ojson.Result, partIndex int, validJSON bool) *geminiFunctionCallEvidence {
	if st.FunctionEvidence == nil {
		st.FunctionEvidence = make(map[string]*geminiFunctionCallEvidence)
	}
	var keys []string
	if partIndex >= 0 {
		keys = append(keys, fmt.Sprintf("part:%d", partIndex))
	}
	if id := fc.Get("id").String(); id != "" {
		keys = append(keys, "id:"+id)
	}
	// Never guess which later named call owns an unkeyed nameless snapshot.
	if len(keys) == 0 && fc.Get("name").String() == "" {
		keys = append(keys, fmt.Sprintf("unknown:%d", len(st.FunctionEvidence)))
	}
	var evidence *geminiFunctionCallEvidence
	conflict := false
	patchRelated := st.ToolIdentityMap[fc.Get("name").String()].ApplyPatch
	for _, key := range keys {
		if prior := st.FunctionEvidence[key]; prior != nil {
			patchRelated = patchRelated || prior.ApplyPatch
			if evidence == nil {
				evidence = prior
			} else if evidence != prior {
				conflict = true
			}
		}
	}
	if conflict {
		errIdentity := fmt.Errorf("conflicting apply_patch call indexes")
		if patchRelated {
			// Reject before rebinding either established call's aliases or provenance.
			return &geminiFunctionCallEvidence{ApplyPatch: true, Err: errIdentity}
		}
		// Ordinary-only cross-key reuse keeps the legacy first-match behavior.
		evidence.Err = errIdentity
	}
	if evidence == nil {
		evidence = &geminiFunctionCallEvidence{}
	}
	recordError := func(err error) {
		if evidence.Err == nil {
			evidence.Err = err
		}
	}
	if partIndex >= 0 {
		if evidence.HasPartIndex && evidence.PartIndex != partIndex {
			recordError(fmt.Errorf("conflicting apply_patch part index"))
		} else {
			evidence.PartIndex = partIndex
			evidence.HasPartIndex = true
		}
	}
	if st.ToolIdentityMap[fc.Get("name").String()].ApplyPatch {
		evidence.ApplyPatch = true
	}
	for _, key := range keys {
		st.FunctionEvidence[key] = evidence
	}
	if name := fc.Get("name").String(); name != "" {
		if evidence.RawName != "" && evidence.RawName != name {
			recordError(fmt.Errorf("conflicting apply_patch call name"))
		} else {
			evidence.RawName = strings.Clone(name)
		}
	}
	if id := fc.Get("id").String(); id != "" {
		if evidence.UpstreamID != "" && evidence.UpstreamID != id {
			recordError(fmt.Errorf("conflicting apply_patch call ID"))
		} else {
			evidence.UpstreamID = strings.Clone(id)
		}
	}
	var snapshot translatorcommon.ApplyPatchCallState
	_, input, errFinishArguments := snapshot.FinishArguments(fc.Get("args").Raw)
	if !validJSON {
		recordError(fmt.Errorf("invalid Gemini apply_patch response JSON"))
	}
	if errFinishArguments != nil {
		recordError(errFinishArguments)
	} else {
		if evidence.HasInput && evidence.Input != input {
			recordError(fmt.Errorf("conflicting apply_patch complete snapshots"))
		}
		evidence.HasInput = true
		evidence.Input = strings.Clone(input)
	}
	return evidence
}

func geminiPendingIdentityError(st *geminiToResponsesState) error {
	patchEnabled := false
	for _, identity := range st.ToolIdentityMap {
		if identity.ApplyPatch {
			patchEnabled = true
			break
		}
	}
	if !patchEnabled {
		return nil
	}
	for _, evidence := range st.FunctionEvidence {
		if evidence.RawName == "" {
			if evidence.Err != nil {
				return evidence.Err
			}
			return fmt.Errorf("unresolved Gemini apply_patch call identity")
		}
	}
	return nil
}

// FinalizeToolInput rejects a patch-enabled stream lacking its source terminator.
func (st *geminiToResponsesState) FinalizeToolInput() [][]byte {
	if st.ToolInputError() != nil || st.Completed {
		return nil
	}
	// A validated source finish remains valid while Responses waits for usage.
	if st.FinishReason != "" {
		if errIdentity := geminiPendingIdentityError(st); errIdentity == nil {
			return nil
		} else {
			st.SetToolInputError(errIdentity)
		}
	}
	enabled := false
	for _, identity := range st.ToolIdentityMap {
		if identity.ApplyPatch {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil
	}
	if st.ToolInputError() == nil {
		st.SetToolInputError(fmt.Errorf("upstream apply_patch stream ended before protocol completion"))
	}
	st.Completed = true
	st.Seq++
	return [][]byte{emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, st.Seq))}
}
