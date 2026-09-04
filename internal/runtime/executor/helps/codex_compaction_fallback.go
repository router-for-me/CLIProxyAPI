package helps

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Codex "remote compaction v2" sends a normal Responses request whose input
// ends with a {"type":"compaction_trigger"} item and expects exactly one
// output item of type "compaction" back. Only OpenAI/Codex upstreams can
// produce the opaque encrypted_content blob. For every other upstream this
// file synthesizes the compaction: it asks the upstream model for a handoff
// summary, wraps the summary into a compaction item with a proxy-owned
// marker, and on replay turns that item back into a plain user message so
// the model keeps the conversation state instead of losing it.

const (
	codexCompactionTriggerType = "compaction_trigger"
	codexCompactionItemType    = "compaction"
	// syntheticCompactionPrefix marks encrypted_content values produced by this
	// proxy. Native OpenAI blobs start with "gAAAA" and never match it.
	syntheticCompactionPrefix = "cpa-compaction-v1:"

	syntheticCompactionPrompt = "Summarize the transcript inside <summary></summary> tags. Include relevant information in the summary such that this conversation will be continued by a new context window without needing to redo work or be reprovided with relevant constraints or context. Be sure to preserve: (1) any difficulties or problems that came up, and how they were handled or resolved; (2) any possibilities, options, or approaches that were considered but ruled out, and why; (3) the user's goal and explicit constraints, quoted verbatim where possible; (4) decisions made and their rationale; (5) what has been completed, with file paths, identifiers, commands, results and error text kept verbatim; (6) the current state of any tool interaction and anything still pending from the user; (7) the exact next steps. Do not continue the task; write only the summary."

	syntheticCompactionReplayPrefix = "The conversation so far was compacted. The following summary, written by the previous model window, " +
		"is the authoritative state of the task. Continue from it without repeating completed work:\n\n"
)

type compactionExecutor interface {
	Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
}

// IsCodexCompactionTriggerRequest reports whether a Responses payload asks for
// remote compaction v2 (its input carries a compaction_trigger item).
func IsCodexCompactionTriggerRequest(payload []byte) bool {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) == codexCompactionTriggerType {
			return true
		}
	}
	return false
}

// SyntheticCompactionSupported reports whether the fallback applies: the
// request is a compaction trigger coming from the Responses API.
func SyntheticCompactionSupported(payload []byte, opts cliproxyexecutor.Options) bool {
	if opts.Alt == "responses/compact" {
		return false
	}
	if opts.SourceFormat != sdktranslator.FormatOpenAIResponse {
		return false
	}
	return IsCodexCompactionTriggerRequest(payload)
}

// ExecuteSyntheticCompaction runs the summary turn and returns a non-streaming
// Responses payload whose only output item is a compaction item.
func ExecuteSyntheticCompaction(ctx context.Context, exec compactionExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	responseJSON, headers, err := runSyntheticCompaction(ctx, exec, auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: responseJSON, Headers: headers}, nil
}

// ExecuteSyntheticCompactionStream runs the summary turn and replays the
// result as a minimal Responses SSE stream.
func ExecuteSyntheticCompactionStream(ctx context.Context, exec compactionExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	responseJSON, headers, err := runSyntheticCompaction(ctx, exec, auth, req, opts)
	if err != nil {
		return nil, err
	}
	chunks := SyntheticCompactionStreamChunks(responseJSON)
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(out)
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

func runSyntheticCompaction(ctx context.Context, exec compactionExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, map[string][]string, error) {
	if exec == nil {
		return nil, nil, errors.New("synthetic compaction: executor is nil")
	}
	summaryPayload := BuildSyntheticCompactionSummaryPayload(req.Payload)
	summaryReq := req
	summaryReq.Payload = summaryPayload
	summaryOpts := opts
	summaryOpts.Stream = false
	summaryOpts.Alt = ""
	summaryOpts.OriginalRequest = summaryPayload
	summaryOpts.ResponseFormat = sdktranslator.FormatOpenAIResponse
	summaryOpts.WebSocketResponseObserver = nil

	resp, err := exec.Execute(ctx, auth, summaryReq, summaryOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("synthetic compaction: summary request failed: %w", err)
	}
	summary := extractSummaryTag(strings.TrimSpace(responsesOutputText(resp.Payload)))
	if summary == "" {
		return nil, nil, errors.New("synthetic compaction: upstream returned an empty summary")
	}
	model := strings.TrimSpace(gjson.GetBytes(req.Payload, "model").String())
	if model == "" {
		model = req.Model
	}
	item := SyntheticCompactionItem(summary)
	responseJSON := syntheticCompactionResponse(model, item, gjson.GetBytes(resp.Payload, "usage"))
	log.Infof("synthetic compaction: produced compaction item model=%s summary_chars=%d", model, len(summary))
	return responseJSON, resp.Headers, nil
}

// BuildSyntheticCompactionSummaryPayload turns a compaction trigger request
// into a plain, tool-free summary request for the same upstream model.
func BuildSyntheticCompactionSummaryPayload(payload []byte) []byte {
	out := removeInputItemsByType(payload, codexCompactionTriggerType)
	for _, field := range []string{"tools", "tool_choice", "parallel_tool_calls", "include", "stream", "text.format", "max_output_tokens"} {
		out, _ = sjson.DeleteBytes(out, field)
	}
	message := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`)
	message, _ = sjson.SetBytes(message, "content.0.text", syntheticCompactionPrompt)
	out, _ = sjson.SetRawBytes(out, "input.-1", message)
	out, _ = sjson.SetBytes(out, "stream", false)
	return out
}

// SyntheticCompactionItem builds the compaction output item carrying the
// summary under the proxy marker.
func SyntheticCompactionItem(summary string) []byte {
	item := []byte(`{"type":"compaction","id":"","encrypted_content":""}`)
	item, _ = sjson.SetBytes(item, "id", "cmp_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	item, _ = sjson.SetBytes(item, "encrypted_content", syntheticCompactionPrefix+base64.RawURLEncoding.EncodeToString([]byte(summary)))
	return item
}

// DecodeSyntheticCompaction returns the summary carried by a proxy-produced
// compaction item, or false for foreign (natively encrypted) items.
func DecodeSyntheticCompaction(encryptedContent string) (string, bool) {
	if !strings.HasPrefix(encryptedContent, syntheticCompactionPrefix) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encryptedContent, syntheticCompactionPrefix))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// InputHasSyntheticCompaction reports whether a Responses input array carries
// a proxy-produced compaction item.
func InputHasSyntheticCompaction(inputRaw []byte) bool {
	input := gjson.ParseBytes(inputRaw)
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != codexCompactionItemType {
			continue
		}
		if _, ok := DecodeSyntheticCompaction(item.Get("encrypted_content").String()); ok {
			return true
		}
	}
	return false
}

// RewriteSyntheticCompactionInput prepares a Responses payload for a
// non-Codex upstream: proxy-produced compaction items become user messages
// carrying the summary, foreign compaction items and stray triggers are
// dropped with a warning because no other upstream can read them.
func RewriteSyntheticCompactionInput(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}
	items := make([][]byte, 0, len(input.Array()))
	changed := false
	for _, item := range input.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case codexCompactionTriggerType:
			changed = true
			continue
		case codexCompactionItemType:
			changed = true
			summary, ok := DecodeSyntheticCompaction(item.Get("encrypted_content").String())
			if !ok {
				log.Warnf("synthetic compaction: dropping compaction item %s encrypted by another provider; earlier context is not recoverable for this upstream", item.Get("id").String())
				continue
			}
			message := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`)
			message, _ = sjson.SetBytes(message, "content.0.text", syntheticCompactionReplayPrefix+summary)
			items = append(items, message)
			continue
		}
		items = append(items, []byte(item.Raw))
	}
	if !changed {
		return payload
	}
	out, err := sjson.SetRawBytes(payload, "input", translatorcommon.JoinRawArray(items))
	if err != nil {
		return payload
	}
	return out
}

// SyntheticCompactionStreamChunks renders a completed Responses payload as
// the minimal SSE sequence Codex expects for a compaction turn.
func SyntheticCompactionStreamChunks(responseJSON []byte) [][]byte {
	seq := 0
	next := func() int { seq++; return seq }
	inProgress, _ := sjson.SetBytes(responseJSON, "status", "in_progress")
	inProgress, _ = sjson.SetRawBytes(inProgress, "output", []byte(`[]`))
	inProgress, _ = sjson.DeleteBytes(inProgress, "usage")
	item := gjson.GetBytes(responseJSON, "output.0").Raw

	event := func(name string, body string) []byte {
		payload := []byte(body)
		payload, _ = sjson.SetBytes(payload, "sequence_number", next())
		return translatorcommon.SSEEventData(name, payload)
	}
	created, _ := sjson.SetRawBytes([]byte(`{"type":"response.created","sequence_number":0,"response":{}}`), "response", inProgress)
	progress, _ := sjson.SetRawBytes([]byte(`{"type":"response.in_progress","sequence_number":0,"response":{}}`), "response", inProgress)
	added, _ := sjson.SetRawBytes([]byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{}}`), "item", []byte(item))
	done, _ := sjson.SetRawBytes([]byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{}}`), "item", []byte(item))
	completed, _ := sjson.SetRawBytes([]byte(`{"type":"response.completed","sequence_number":0,"response":{}}`), "response", responseJSON)
	return [][]byte{
		event("response.created", string(created)),
		event("response.in_progress", string(progress)),
		event("response.output_item.added", string(added)),
		event("response.output_item.done", string(done)),
		event("response.completed", string(completed)),
	}
}

func syntheticCompactionResponse(model string, item []byte, usage gjson.Result) []byte {
	out := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"model":"","output":[]}`)
	out, _ = sjson.SetBytes(out, "id", "resp_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	out, _ = sjson.SetBytes(out, "created_at", time.Now().Unix())
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetRawBytes(out, "output.-1", item)
	if usage.Exists() && usage.IsObject() {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(usage.Raw))
	} else {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(`{"input_tokens":0,"output_tokens":0,"total_tokens":0}`))
	}
	return EnsureResponsesUsageDetails(out)
}

func responsesOutputText(payload []byte) string {
	var text strings.Builder
	for _, item := range gjson.GetBytes(payload, "output").Array() {
		if strings.TrimSpace(item.Get("type").String()) != "message" {
			continue
		}
		for _, part := range item.Get("content").Array() {
			if strings.TrimSpace(part.Get("type").String()) == "output_text" {
				text.WriteString(part.Get("text").String())
			}
		}
	}
	return text.String()
}

func removeInputItemsByType(payload []byte, itemType string) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}
	items := make([][]byte, 0, len(input.Array()))
	changed := false
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) == itemType {
			changed = true
			continue
		}
		items = append(items, []byte(item.Raw))
	}
	if !changed {
		return payload
	}
	out, err := sjson.SetRawBytes(payload, "input", translatorcommon.JoinRawArray(items))
	if err != nil {
		return payload
	}
	return out
}

// extractSummaryTag returns the text inside <summary></summary> when the
// model followed the instruction, and the whole text otherwise.
func extractSummaryTag(text string) string {
	start := strings.Index(text, "<summary>")
	end := strings.LastIndex(text, "</summary>")
	if start >= 0 && end > start {
		return strings.TrimSpace(text[start+len("<summary>") : end])
	}
	return text
}
