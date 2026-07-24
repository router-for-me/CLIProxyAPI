package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type geminiDetachedReasoningItem struct {
	Index     int
	ID        string
	Signature string
}

type geminiCompletedMessageItem struct {
	ID   string
	Text string
}

type geminiCompletedReasoningItem struct {
	ID        string
	Signature string
	Text      string
}

type geminiToResponsesState struct {
	Seq        int
	ResponseID string
	CreatedAt  int64
	Started    bool

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

	// function call aggregation (keyed by output_index)
	NextIndex        int
	FuncArgsBuf      map[int]*strings.Builder
	FuncNames        map[int]string
	FuncCallIDs      map[int]string
	FuncDone         map[int]bool
	SanitizedNameMap map[string]string
}

// responseIDCounter provides a process-wide unique counter for synthesized response identifiers.
var responseIDCounter uint64

// funcCallIDCounter provides a process-wide unique counter for function call identifiers.
var funcCallIDCounter uint64

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && gjson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && gjson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

func unwrapRequestRoot(root gjson.Result) gjson.Result {
	req := root.Get("request")
	if !req.Exists() {
		return root
	}
	if req.Get("model").Exists() || req.Get("input").Exists() || req.Get("instructions").Exists() {
		return req
	}
	return root
}

func unwrapGeminiResponseRoot(root gjson.Result) gjson.Result {
	resp := root.Get("response")
	if !resp.Exists() {
		return root
	}
	// Vertex-style Gemini responses wrap the actual payload in a "response" object.
	if resp.Get("candidates").Exists() || resp.Get("responseId").Exists() || resp.Get("usageMetadata").Exists() {
		return resp
	}
	return root
}

func emitEvent(event string, payload []byte) []byte {
	return translatorcommon.SSEEventData(event, payload)
}

// ConvertGeminiResponseToOpenAIResponses converts Gemini SSE chunks into OpenAI Responses SSE events.
func ConvertGeminiResponseToOpenAIResponses(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &geminiToResponsesState{
			FuncArgsBuf:             make(map[int]*strings.Builder),
			FuncNames:               make(map[int]string),
			FuncCallIDs:             make(map[int]string),
			FuncDone:                make(map[int]bool),
			DetachedReasoning:       make(map[int]geminiDetachedReasoningItem),
			CompletedMessages:       make(map[int]geminiCompletedMessageItem),
			CompletedReasoning:      make(map[int]geminiCompletedReasoningItem),
			SeenReasoningSignatures: make(map[string]bool),
			SanitizedNameMap:        util.SanitizedToolNameMap(originalRequestRawJSON),
		}
	}
	st := (*param).(*geminiToResponsesState)
	if st.FuncArgsBuf == nil {
		st.FuncArgsBuf = make(map[int]*strings.Builder)
	}
	if st.FuncNames == nil {
		st.FuncNames = make(map[int]string)
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
	if st.SanitizedNameMap == nil {
		st.SanitizedNameMap = util.SanitizedToolNameMap(originalRequestRawJSON)
	}

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	rawJSON = bytes.TrimSpace(rawJSON)
	if len(rawJSON) == 0 {
		return [][]byte{}
	}
	doneOnly := bytes.Equal(rawJSON, []byte("[DONE]"))
	if doneOnly {
		rawJSON = []byte(`{}`)
	}

	root := gjson.ParseBytes(rawJSON)
	if !root.Exists() {
		return [][]byte{}
	}
	root = unwrapGeminiResponseRoot(root)

	var out [][]byte
	nextSeq := func() int { st.Seq++; return st.Seq }

	reasoningEncryptedContent := func() string {
		if st.ReasoningEnc == "" || st.ReasoningDirection == "" {
			return st.ReasoningEnc
		}
		return encodeGeminiResponsesCarrier(st.ReasoningEnc, st.ReasoningDirection, st.ReasoningTargetKind)
	}
	openReasoning := func() {
		if st.ReasoningOpened || st.ReasoningClosed || (st.ReasoningBuf.Len() == 0 && st.ReasoningEnc == "") {
			return
		}
		st.ReasoningOpened = true
		st.ReasoningIndex = st.NextIndex
		st.NextIndex++
		st.ReasoningItemID = fmt.Sprintf("rs_%s_%d", st.ResponseID, st.ReasoningIndex)
		item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","encrypted_content":"","summary":[]}}`)
		item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
		item, _ = sjson.SetBytes(item, "output_index", st.ReasoningIndex)
		item, _ = sjson.SetBytes(item, "item.id", st.ReasoningItemID)
		item, _ = sjson.SetBytes(item, "item.encrypted_content", reasoningEncryptedContent())
		out = append(out, emitEvent("response.output_item.added", item))
		partAdded := []byte(`{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
		partAdded, _ = sjson.SetBytes(partAdded, "sequence_number", nextSeq())
		partAdded, _ = sjson.SetBytes(partAdded, "item_id", st.ReasoningItemID)
		partAdded, _ = sjson.SetBytes(partAdded, "output_index", st.ReasoningIndex)
		out = append(out, emitEvent("response.reasoning_summary_part.added", partAdded))
		for _, delta := range st.ReasoningPendingDeltas {
			msg := []byte(`{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`)
			msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
			msg, _ = sjson.SetBytes(msg, "item_id", st.ReasoningItemID)
			msg, _ = sjson.SetBytes(msg, "output_index", st.ReasoningIndex)
			msg, _ = sjson.SetBytes(msg, "delta", delta)
			out = append(out, emitEvent("response.reasoning_summary_text.delta", msg))
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
		textDone := []byte(`{"type":"response.reasoning_summary_text.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"text":""}`)
		textDone, _ = sjson.SetBytes(textDone, "sequence_number", nextSeq())
		textDone, _ = sjson.SetBytes(textDone, "item_id", st.ReasoningItemID)
		textDone, _ = sjson.SetBytes(textDone, "output_index", st.ReasoningIndex)
		textDone, _ = sjson.SetBytes(textDone, "text", full)
		out = append(out, emitEvent("response.reasoning_summary_text.done", textDone))

		partDone := []byte(`{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
		partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
		partDone, _ = sjson.SetBytes(partDone, "item_id", st.ReasoningItemID)
		partDone, _ = sjson.SetBytes(partDone, "output_index", st.ReasoningIndex)
		partDone, _ = sjson.SetBytes(partDone, "part.text", full)
		out = append(out, emitEvent("response.reasoning_summary_part.done", partDone))

		itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":""}]}}`)
		itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
		itemDone, _ = sjson.SetBytes(itemDone, "item.id", st.ReasoningItemID)
		itemDone, _ = sjson.SetBytes(itemDone, "output_index", st.ReasoningIndex)
		itemDone, _ = sjson.SetBytes(itemDone, "item.encrypted_content", reasoningEncryptedContent())
		itemDone, _ = sjson.SetBytes(itemDone, "item.summary.0.text", full)
		out = append(out, emitEvent("response.output_item.done", itemDone))

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

	// Helper to finalize the assistant message in correct order.
	// It emits response.output_text.done, response.content_part.done,
	// and response.output_item.done exactly once.
	finalizeMessage := func() {
		if !st.MsgOpened || st.MsgClosed {
			return
		}
		fullText := st.ItemTextBuf.String()
		done := []byte(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
		done, _ = sjson.SetBytes(done, "item_id", st.CurrentMsgID)
		done, _ = sjson.SetBytes(done, "output_index", st.MsgIndex)
		done, _ = sjson.SetBytes(done, "text", fullText)
		out = append(out, emitEvent("response.output_text.done", done))
		partDone := []byte(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
		partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
		partDone, _ = sjson.SetBytes(partDone, "item_id", st.CurrentMsgID)
		partDone, _ = sjson.SetBytes(partDone, "output_index", st.MsgIndex)
		partDone, _ = sjson.SetBytes(partDone, "part.text", fullText)
		out = append(out, emitEvent("response.content_part.done", partDone))
		final := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","text":""}],"role":"assistant"}}`)
		final, _ = sjson.SetBytes(final, "sequence_number", nextSeq())
		final, _ = sjson.SetBytes(final, "output_index", st.MsgIndex)
		final, _ = sjson.SetBytes(final, "item.id", st.CurrentMsgID)
		final, _ = sjson.SetBytes(final, "item.content.0.text", fullText)
		out = append(out, emitEvent("response.output_item.done", final))

		st.CompletedMessages[st.MsgIndex] = geminiCompletedMessageItem{ID: st.CurrentMsgID, Text: fullText}
		st.MsgClosed = true
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

		added := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","encrypted_content":"","summary":[]}}`)
		added, _ = sjson.SetBytes(added, "sequence_number", nextSeq())
		added, _ = sjson.SetBytes(added, "output_index", idx)
		added, _ = sjson.SetBytes(added, "item.id", itemID)
		added, _ = sjson.SetBytes(added, "item.encrypted_content", carrierSignature)
		out = append(out, emitEvent("response.output_item.added", added))

		done := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","encrypted_content":"","summary":[]}}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
		done, _ = sjson.SetBytes(done, "output_index", idx)
		done, _ = sjson.SetBytes(done, "item.id", itemID)
		done, _ = sjson.SetBytes(done, "item.encrypted_content", carrierSignature)
		out = append(out, emitEvent("response.output_item.done", done))

		st.DetachedReasoning[idx] = geminiDetachedReasoningItem{Index: idx, ID: itemID, Signature: carrierSignature}
		st.SeenReasoningSignatures[signature] = true
	}
	emitTrailingDetachedReasoning := func(signature string) {
		switch st.LastSemanticKind {
		case geminiResponsesCarrierText:
			emitDetachedReasoning(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		case geminiResponsesCarrierFunction:
			emitDetachedReasoning(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierFunction)
		default:
			emitDetachedReasoning(signature, geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)
		}
	}

	if doneOnly {
		if st.Started {
			openReasoning()
		}
		return out
	}

	// Initialize per-response fields and emit created/in_progress once
	if !st.Started {
		st.ResponseID = root.Get("responseId").String()
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

		created := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
		created, _ = sjson.SetBytes(created, "sequence_number", nextSeq())
		created, _ = sjson.SetBytes(created, "response.id", st.ResponseID)
		created, _ = sjson.SetBytes(created, "response.created_at", st.CreatedAt)
		out = append(out, emitEvent("response.created", created))

		inprog := []byte(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress"}}`)
		inprog, _ = sjson.SetBytes(inprog, "sequence_number", nextSeq())
		inprog, _ = sjson.SetBytes(inprog, "response.id", st.ResponseID)
		inprog, _ = sjson.SetBytes(inprog, "response.created_at", st.CreatedAt)
		out = append(out, emitEvent("response.in_progress", inprog))

		st.Started = true
		st.NextIndex = 0
	}

	// Handle parts (text/thought/functionCall)
	if parts := root.Get("candidates.0.content.parts"); parts.Exists() && parts.IsArray() {
		parts.ForEach(func(_, part gjson.Result) bool {
			signature := strings.TrimSpace(part.Get("thoughtSignature").String())
			if signature == "" {
				signature = strings.TrimSpace(part.Get("thought_signature").String())
			}
			functionCall := part.Get("functionCall")
			text := part.Get("text")
			isThought := part.Get("thought").Bool()
			if functionCall.Exists() && st.PendingReasoningSignature != "" {
				emitTrailingDetachedReasoning(st.PendingReasoningSignature)
				st.PendingReasoningSignature = ""
			}
			reasoningActive := (st.ReasoningOpened && !st.ReasoningClosed) || (!st.ReasoningOpened && (st.ReasoningBuf.Len() > 0 || st.ReasoningEnc != ""))
			if signature != "" && !isThought {
				if reasoningActive {
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
						if st.MsgOpened || len(st.FuncDone) > 0 {
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
						msg := []byte(`{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`)
						msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
						msg, _ = sjson.SetBytes(msg, "item_id", st.ReasoningItemID)
						msg, _ = sjson.SetBytes(msg, "output_index", st.ReasoningIndex)
						msg, _ = sjson.SetBytes(msg, "delta", t.String())
						out = append(out, emitEvent("response.reasoning_summary_text.delta", msg))
					} else {
						st.ReasoningPendingDeltas = append(st.ReasoningPendingDeltas, t.String())
					}
				}
				if !st.ReasoningOpened && st.ReasoningEnc != "" {
					openReasoning()
				}
				return true
			}

			// Assistant visible text
			if t := part.Get("text"); t.Exists() && t.String() != "" {
				if signature == "" && st.PendingReasoningSignature != "" && st.MsgOpened && !st.MsgClosed {
					emitTrailingDetachedReasoning(st.PendingReasoningSignature)
					st.PendingReasoningSignature = ""
				}
				// Responses output items are sequential: finish reasoning before
				// opening the visible message. A signature that arrives later is
				// emitted as an explicit trailing carrier and recombined on replay.
				finalizeReasoning()
				if st.MsgClosed {
					st.MsgOpened = false
					st.MsgClosed = false
					st.ItemTextBuf.Reset()
				}
				if !st.MsgOpened {
					st.MsgOpened = true
					st.MsgIndex = st.NextIndex
					st.NextIndex++
					st.CurrentMsgID = fmt.Sprintf("msg_%s_%d", st.ResponseID, st.MsgIndex)
					item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`)
					item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
					item, _ = sjson.SetBytes(item, "output_index", st.MsgIndex)
					item, _ = sjson.SetBytes(item, "item.id", st.CurrentMsgID)
					out = append(out, emitEvent("response.output_item.added", item))
					partAdded := []byte(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
					partAdded, _ = sjson.SetBytes(partAdded, "sequence_number", nextSeq())
					partAdded, _ = sjson.SetBytes(partAdded, "item_id", st.CurrentMsgID)
					partAdded, _ = sjson.SetBytes(partAdded, "output_index", st.MsgIndex)
					out = append(out, emitEvent("response.content_part.added", partAdded))
					st.ItemTextBuf.Reset()
				}
				st.LastSemanticKind = geminiResponsesCarrierText
				st.ItemTextBuf.WriteString(t.String())
				msg := []byte(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
				msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
				msg, _ = sjson.SetBytes(msg, "item_id", st.CurrentMsgID)
				msg, _ = sjson.SetBytes(msg, "output_index", st.MsgIndex)
				msg, _ = sjson.SetBytes(msg, "delta", t.String())
				out = append(out, emitEvent("response.output_text.delta", msg))
				return true
			}

			// Function call
			if fc := part.Get("functionCall"); fc.Exists() {
				// Before emitting function-call outputs, finalize reasoning and the message (if open).
				// Responses streaming requires message done events before the next output_item.added.
				finalizeReasoning()
				finalizeMessage()
				st.LastSemanticKind = geminiResponsesCarrierFunction
				name := util.RestoreSanitizedToolName(st.SanitizedNameMap, fc.Get("name").String())
				idx := st.NextIndex
				st.NextIndex++
				// Ensure buffers
				if st.FuncArgsBuf[idx] == nil {
					st.FuncArgsBuf[idx] = &strings.Builder{}
				}
				if st.FuncCallIDs[idx] == "" {
					st.FuncCallIDs[idx] = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&funcCallIDCounter, 1))
				}
				st.FuncNames[idx] = name

				argsJSON := "{}"
				if args := fc.Get("args"); args.Exists() {
					argsJSON = args.Raw
				}
				if st.FuncArgsBuf[idx].Len() == 0 && argsJSON != "" {
					st.FuncArgsBuf[idx].WriteString(argsJSON)
				}

				// Emit item.added for function call
				item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`)
				item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
				item, _ = sjson.SetBytes(item, "output_index", idx)
				item, _ = sjson.SetBytes(item, "item.id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
				item, _ = sjson.SetBytes(item, "item.call_id", st.FuncCallIDs[idx])
				item, _ = sjson.SetBytes(item, "item.name", name)
				out = append(out, emitEvent("response.output_item.added", item))

				// Emit arguments delta (full args in one chunk).
				// When Gemini omits args, emit "{}" to keep Responses streaming event order consistent.
				if argsJSON != "" {
					ad := []byte(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
					ad, _ = sjson.SetBytes(ad, "sequence_number", nextSeq())
					ad, _ = sjson.SetBytes(ad, "item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
					ad, _ = sjson.SetBytes(ad, "output_index", idx)
					ad, _ = sjson.SetBytes(ad, "delta", argsJSON)
					out = append(out, emitEvent("response.function_call_arguments.delta", ad))
				}

				// Gemini emits the full function call payload at once, so we can finalize it immediately.
				if !st.FuncDone[idx] {
					fcDone := []byte(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
					fcDone, _ = sjson.SetBytes(fcDone, "sequence_number", nextSeq())
					fcDone, _ = sjson.SetBytes(fcDone, "item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
					fcDone, _ = sjson.SetBytes(fcDone, "output_index", idx)
					fcDone, _ = sjson.SetBytes(fcDone, "arguments", argsJSON)
					out = append(out, emitEvent("response.function_call_arguments.done", fcDone))

					itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
					itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
					itemDone, _ = sjson.SetBytes(itemDone, "output_index", idx)
					itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
					itemDone, _ = sjson.SetBytes(itemDone, "item.arguments", argsJSON)
					itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", st.FuncCallIDs[idx])
					itemDone, _ = sjson.SetBytes(itemDone, "item.name", st.FuncNames[idx])
					out = append(out, emitEvent("response.output_item.done", itemDone))

					st.FuncDone[idx] = true
				}

				return true
			}

			return true
		})
	}

	// Finalization on finishReason
	if fr := root.Get("candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
		if st.PendingReasoningSignature != "" {
			emitTrailingDetachedReasoning(st.PendingReasoningSignature)
			st.PendingReasoningSignature = ""
		}
		// Finalize reasoning first to keep ordering tight with last delta
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
				args := "{}"
				if b := st.FuncArgsBuf[idx]; b != nil && b.Len() > 0 {
					args = b.String()
				}
				fcDone := []byte(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
				fcDone, _ = sjson.SetBytes(fcDone, "sequence_number", nextSeq())
				fcDone, _ = sjson.SetBytes(fcDone, "item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
				fcDone, _ = sjson.SetBytes(fcDone, "output_index", idx)
				fcDone, _ = sjson.SetBytes(fcDone, "arguments", args)
				out = append(out, emitEvent("response.function_call_arguments.done", fcDone))

				itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
				itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
				itemDone, _ = sjson.SetBytes(itemDone, "output_index", idx)
				itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
				itemDone, _ = sjson.SetBytes(itemDone, "item.arguments", args)
				itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", st.FuncCallIDs[idx])
				itemDone, _ = sjson.SetBytes(itemDone, "item.name", st.FuncNames[idx])
				out = append(out, emitEvent("response.output_item.done", itemDone))

				st.FuncDone[idx] = true
			}
		}

		// Reasoning already finalized above if present

		// Build response.completed with aggregated outputs and request echo fields
		completed := []byte(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null}}`)
		completed, _ = sjson.SetBytes(completed, "sequence_number", nextSeq())
		completed, _ = sjson.SetBytes(completed, "response.id", st.ResponseID)
		completed, _ = sjson.SetBytes(completed, "response.created_at", st.CreatedAt)

		if reqJSON := pickRequestJSON(originalRequestRawJSON, requestRawJSON); len(reqJSON) > 0 {
			req := unwrapRequestRoot(gjson.ParseBytes(reqJSON))
			if v := req.Get("instructions"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.instructions", v.String())
			}
			if v := req.Get("max_output_tokens"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.max_output_tokens", v.Int())
			}
			if v := req.Get("max_tool_calls"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.max_tool_calls", v.Int())
			}
			if v := req.Get("model"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.model", v.String())
			}
			if v := req.Get("parallel_tool_calls"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.parallel_tool_calls", v.Bool())
			}
			if v := req.Get("previous_response_id"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.previous_response_id", v.String())
			}
			if v := req.Get("prompt_cache_key"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.prompt_cache_key", v.String())
			}
			if v := req.Get("reasoning"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.reasoning", v.Value())
			}
			if v := req.Get("safety_identifier"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.safety_identifier", v.String())
			}
			if v := req.Get("service_tier"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.service_tier", v.String())
			}
			if v := req.Get("store"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.store", v.Bool())
			}
			if v := req.Get("temperature"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.temperature", v.Float())
			}
			if v := req.Get("text"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.text", v.Value())
			}
			if v := req.Get("tool_choice"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.tool_choice", v.Value())
			}
			if v := req.Get("tools"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.tools", v.Value())
			}
			if v := req.Get("top_logprobs"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.top_logprobs", v.Int())
			}
			if v := req.Get("top_p"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.top_p", v.Float())
			}
			if v := req.Get("truncation"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.truncation", v.String())
			}
			if v := req.Get("user"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.user", v.Value())
			}
			if v := req.Get("metadata"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.metadata", v.Value())
			}
		}

		// Compose outputs in output_index order.
		outputsWrapper := []byte(`{"arr":[]}`)
		for idx := 0; idx < st.NextIndex; idx++ {
			if completedReasoning, ok := st.CompletedReasoning[idx]; ok {
				item := []byte(`{"id":"","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":""}]}`)
				item, _ = sjson.SetBytes(item, "id", completedReasoning.ID)
				item, _ = sjson.SetBytes(item, "encrypted_content", completedReasoning.Signature)
				item, _ = sjson.SetBytes(item, "summary.0.text", completedReasoning.Text)
				outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
				continue
			}
			if completedMessage, ok := st.CompletedMessages[idx]; ok {
				item := []byte(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
				item, _ = sjson.SetBytes(item, "id", completedMessage.ID)
				item, _ = sjson.SetBytes(item, "content.0.text", completedMessage.Text)
				outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
				continue
			}
			if detached, ok := st.DetachedReasoning[idx]; ok {
				item := []byte(`{"id":"","type":"reasoning","encrypted_content":"","summary":[]}`)
				item, _ = sjson.SetBytes(item, "id", detached.ID)
				item, _ = sjson.SetBytes(item, "encrypted_content", detached.Signature)
				outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
				continue
			}

			if callID, ok := st.FuncCallIDs[idx]; ok && callID != "" {
				args := "{}"
				if b := st.FuncArgsBuf[idx]; b != nil && b.Len() > 0 {
					args = b.String()
				}
				item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
				item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("fc_%s", callID))
				item, _ = sjson.SetBytes(item, "arguments", args)
				item, _ = sjson.SetBytes(item, "call_id", callID)
				item, _ = sjson.SetBytes(item, "name", st.FuncNames[idx])
				outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
			}
		}
		if gjson.GetBytes(outputsWrapper, "arr.#").Int() > 0 {
			completed, _ = sjson.SetRawBytes(completed, "response.output", []byte(gjson.GetBytes(outputsWrapper, "arr").Raw))
		}

		// usage mapping
		if um := root.Get("usageMetadata"); um.Exists() {
			// input tokens = prompt only (thoughts go to output)
			input := um.Get("promptTokenCount").Int()
			completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens", input)
			// cached token details: align with OpenAI "cached_tokens" semantics.
			completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens_details.cached_tokens", um.Get("cachedContentTokenCount").Int())
			// output tokens
			if v := um.Get("candidatesTokenCount"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens", v.Int())
			} else {
				completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens", 0)
			}
			if v := um.Get("thoughtsTokenCount"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens_details.reasoning_tokens", v.Int())
			} else {
				completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens_details.reasoning_tokens", 0)
			}
			if v := um.Get("totalTokenCount"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.usage.total_tokens", v.Int())
			} else {
				completed, _ = sjson.SetBytes(completed, "response.usage.total_tokens", 0)
			}
		}

		out = append(out, emitEvent("response.completed", completed))
	}

	return out
}

// ConvertGeminiResponseToOpenAIResponsesNonStream aggregates Gemini response JSON into a single OpenAI Responses JSON object.
func ConvertGeminiResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	root := gjson.ParseBytes(rawJSON)
	root = unwrapGeminiResponseRoot(root)
	sanitizedNameMap := util.SanitizedToolNameMap(originalRequestRawJSON)

	// Base response scaffold
	resp := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null}`)

	// id: prefer provider responseId, otherwise synthesize
	id := root.Get("responseId").String()
	if id == "" {
		id = fmt.Sprintf("resp_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&responseIDCounter, 1))
	}
	// Normalize to response-style id (prefix resp_ if missing)
	if !strings.HasPrefix(id, "resp_") {
		id = fmt.Sprintf("resp_%s", id)
	}
	resp, _ = sjson.SetBytes(resp, "id", id)

	// created_at: map from createTime if available
	createdAt := time.Now().Unix()
	if v := root.Get("createTime"); v.Exists() {
		if t, errParseCreateTime := time.Parse(time.RFC3339Nano, v.String()); errParseCreateTime == nil {
			createdAt = t.Unix()
		}
	}
	resp, _ = sjson.SetBytes(resp, "created_at", createdAt)

	// Echo request fields when present; fallback model from response modelVersion
	if reqJSON := pickRequestJSON(originalRequestRawJSON, requestRawJSON); len(reqJSON) > 0 {
		req := unwrapRequestRoot(gjson.ParseBytes(reqJSON))
		if v := req.Get("instructions"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "instructions", v.String())
		}
		if v := req.Get("max_output_tokens"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "max_output_tokens", v.Int())
		}
		if v := req.Get("max_tool_calls"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "max_tool_calls", v.Int())
		}
		if v := req.Get("model"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "model", v.String())
		} else if v = root.Get("modelVersion"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "model", v.String())
		}
		if v := req.Get("parallel_tool_calls"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "parallel_tool_calls", v.Bool())
		}
		if v := req.Get("previous_response_id"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "previous_response_id", v.String())
		}
		if v := req.Get("prompt_cache_key"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "prompt_cache_key", v.String())
		}
		if v := req.Get("reasoning"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "reasoning", v.Value())
		}
		if v := req.Get("safety_identifier"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "safety_identifier", v.String())
		}
		if v := req.Get("service_tier"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "service_tier", v.String())
		}
		if v := req.Get("store"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "store", v.Bool())
		}
		if v := req.Get("temperature"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "temperature", v.Float())
		}
		if v := req.Get("text"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "text", v.Value())
		}
		if v := req.Get("tool_choice"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "tool_choice", v.Value())
		}
		if v := req.Get("tools"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "tools", v.Value())
		}
		if v := req.Get("top_logprobs"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "top_logprobs", v.Int())
		}
		if v := req.Get("top_p"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "top_p", v.Float())
		}
		if v := req.Get("truncation"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "truncation", v.String())
		}
		if v := req.Get("user"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "user", v.Value())
		}
		if v := req.Get("metadata"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "metadata", v.Value())
		}
	} else if v := root.Get("modelVersion"); v.Exists() {
		resp, _ = sjson.SetBytes(resp, "model", v.String())
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
	flushMessageOutput := func() {
		if currentMessageText.Len() == 0 {
			return
		}
		messageIndex := len(messageOutputs)
		messageOutputs = append(messageOutputs, nonStreamMessageOutput{text: currentMessageText.String(), signatures: append([]string(nil), currentMessageSignatures...)})
		outputOrder = append(outputOrder, nonStreamOutputOrder{kind: "message", index: messageIndex})
		currentMessageText.Reset()
		currentMessageSignatures = nil
	}

	haveOutput := false
	ensureOutput := func() {
		if haveOutput {
			return
		}
		resp, _ = sjson.SetRawBytes(resp, "output", []byte("[]"))
		haveOutput = true
	}
	appendOutput := func(itemJSON []byte) {
		ensureOutput()
		resp, _ = sjson.SetRawBytes(resp, "output.-1", itemJSON)
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
		itemJSON := []byte(`{"id":"","type":"reasoning","encrypted_content":"","summary":[]}`)
		itemJSON, _ = sjson.SetBytes(itemJSON, "id", fmt.Sprintf("rs_%s_detached_%s_%d", strings.TrimPrefix(id, "resp_"), placement, detachedOutputIndex))
		itemJSON, _ = sjson.SetBytes(itemJSON, "encrypted_content", encodeGeminiResponsesCarrier(signature, direction, targetKind))
		detachedOutputIndex++
		appendOutput(itemJSON)
	}

	if parts := root.Get("candidates.0.content.parts"); parts.Exists() && parts.IsArray() {
		parts.ForEach(func(_, p gjson.Result) bool {
			signature := strings.TrimSpace(p.Get("thoughtSignature").String())
			if signature == "" {
				signature = strings.TrimSpace(p.Get("thought_signature").String())
			}
			if p.Get("thought").Bool() {
				flushMessageOutput()
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
				}
				currentMessageText.WriteString(t.String())
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
				name := util.RestoreSanitizedToolName(sanitizedNameMap, fc.Get("name").String())
				args := fc.Get("args")
				callID := fmt.Sprintf("call_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&funcCallIDCounter, 1))
				itemJSON := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
				itemJSON, _ = sjson.SetBytes(itemJSON, "id", fmt.Sprintf("fc_%s", callID))
				itemJSON, _ = sjson.SetBytes(itemJSON, "call_id", callID)
				itemJSON, _ = sjson.SetBytes(itemJSON, "name", name)
				argsStr := ""
				if args.Exists() {
					argsStr = args.Raw
				}
				itemJSON, _ = sjson.SetBytes(itemJSON, "arguments", argsStr)
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

	flushReasoningOutput()
	flushMessageOutput()

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
			itemJSON := []byte(`{"id":"","type":"reasoning","encrypted_content":""}`)
			itemJSON, _ = sjson.SetBytes(itemJSON, "id", reasoningID)
			encryptedContent := reasoningOutput.signature
			if encryptedContent != "" && reasoningOutput.direction != "" {
				encryptedContent = encodeGeminiResponsesCarrier(encryptedContent, reasoningOutput.direction, reasoningOutput.targetKind)
			}
			itemJSON, _ = sjson.SetBytes(itemJSON, "encrypted_content", encryptedContent)
			if reasoningOutput.text != "" {
				summaryJSON := []byte(`{"type":"summary_text","text":""}`)
				summaryJSON, _ = sjson.SetBytes(summaryJSON, "text", reasoningOutput.text)
				itemJSON, _ = sjson.SetRawBytes(itemJSON, "summary", []byte(`[]`))
				itemJSON, _ = sjson.SetRawBytes(itemJSON, "summary.-1", summaryJSON)
			}
			appendOutput(itemJSON)
		case "message":
			if outputItem.index < 0 || outputItem.index >= len(messageOutputs) {
				continue
			}
			messageOutput := messageOutputs[outputItem.index]
			for _, signature := range messageOutput.signatures {
				if !reasoningOutputSignatures[signature] {
					appendDetachedOutput(signature, geminiResponsesCarrierNext, geminiResponsesCarrierText)
				}
			}
			itemJSON := []byte(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
			itemJSON, _ = sjson.SetBytes(itemJSON, "id", fmt.Sprintf("msg_%s_%d", strings.TrimPrefix(id, "resp_"), outputItem.index))
			itemJSON, _ = sjson.SetBytes(itemJSON, "content.0.text", messageOutput.text)
			appendOutput(itemJSON)
		case "function":
			if outputItem.index < 0 || outputItem.index >= len(functionOutputs) {
				continue
			}
			functionOutput := functionOutputs[outputItem.index]
			appendDetachedOutput(functionOutput.signature, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
			appendOutput(functionOutput.item)
		}
	}

	// usage mapping
	if um := root.Get("usageMetadata"); um.Exists() {
		// input tokens = prompt only (thoughts go to output)
		input := um.Get("promptTokenCount").Int()
		resp, _ = sjson.SetBytes(resp, "usage.input_tokens", input)
		// cached token details: align with OpenAI "cached_tokens" semantics.
		resp, _ = sjson.SetBytes(resp, "usage.input_tokens_details.cached_tokens", um.Get("cachedContentTokenCount").Int())
		// output tokens
		if v := um.Get("candidatesTokenCount"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "usage.output_tokens", v.Int())
		}
		if v := um.Get("thoughtsTokenCount"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "usage.output_tokens_details.reasoning_tokens", v.Int())
		}
		if v := um.Get("totalTokenCount"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "usage.total_tokens", v.Int())
		}
	}

	return resp
}
