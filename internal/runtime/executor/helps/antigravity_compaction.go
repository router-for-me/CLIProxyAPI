package helps

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	antigravityCompactionCapsulePrefix = "cpa-ag-compact-v1:"
	fixedCompactionKeySecret           = "CLIProxyAPI"
)

type antigravityCompactionCapsuleData struct {
	Summary   string `json:"summary"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"created_at"`
}

// RecognizedAntigravityCompactionCapsule reports whether encrypted content uses the CPA capsule prefix.
func RecognizedAntigravityCompactionCapsule(encryptedContent string) bool {
	return strings.HasPrefix(strings.TrimSpace(encryptedContent), antigravityCompactionCapsulePrefix)
}

// HasResponsesCompactionTrigger checks whether input contains a compaction_trigger item.
func HasResponsesCompactionTrigger(payload []byte) bool {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == "compaction_trigger" {
			return true
		}
	}
	return false
}

// HasResponsesCompactionItem checks whether input contains a compaction item.
func HasResponsesCompactionItem(payload []byte) bool {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == "compaction" {
			return true
		}
	}
	return false
}

// HasResponsesCompactionTriggerRequest reports whether req.Payload or opts.OriginalRequest contains a compaction_trigger item.
func HasResponsesCompactionTriggerRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	return HasResponsesCompactionTrigger(req.Payload) || HasResponsesCompactionTrigger(opts.OriginalRequest)
}

// ResponsesCompactionSourcePayload selects the source payload containing the compaction trigger or request history.
func ResponsesCompactionSourcePayload(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	payload := req.Payload
	if !HasResponsesCompactionTrigger(payload) && HasResponsesCompactionTrigger(opts.OriginalRequest) {
		payload = opts.OriginalRequest
	}
	if len(payload) == 0 && len(opts.OriginalRequest) > 0 {
		payload = opts.OriginalRequest
	}
	return payload
}

// DropForeignResponsesCompactionItems removes compaction items whose encrypted_content does not use the CPA capsule prefix.
func DropForeignResponsesCompactionItems(payload []byte, provider string) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}
	dropped := 0
	items := make([]string, 0, len(input.Array()))
	for _, item := range input.Array() {
		if item.Get("type").String() == "compaction" && !RecognizedAntigravityCompactionCapsule(item.Get("encrypted_content").String()) {
			dropped++
			continue
		}
		items = append(items, item.Raw)
	}
	if dropped == 0 {
		return payload
	}
	log.Warnf("%s compaction: dropped %d non-CPA compaction item(s)", provider, dropped)
	out, _ := sjson.SetRawBytes(payload, "input", []byte("["+strings.Join(items, ",")+"]"))
	return out
}

// ExpandResponsesCompactionCapsules drops foreign compaction items and expands recognized CPA compaction capsules in req and opts.
func ExpandResponsesCompactionCapsules(req *cliproxyexecutor.Request, opts *cliproxyexecutor.Options, provider string) error {
	sameOriginal := req != nil && opts != nil && len(opts.OriginalRequest) > 0 && bytes.Equal(req.Payload, opts.OriginalRequest)
	if req != nil {
		req.Payload = DropForeignResponsesCompactionItems(req.Payload, provider)
		if HasResponsesCompactionItem(req.Payload) {
			expanded, errExpand := ExpandAntigravityCompactionCapsules(req.Payload)
			if errExpand != nil {
				return errExpand
			}
			req.Payload = expanded
		}
	}
	if opts != nil && len(opts.OriginalRequest) > 0 {
		if sameOriginal {
			opts.OriginalRequest = req.Payload
			return nil
		}
		opts.OriginalRequest = DropForeignResponsesCompactionItems(opts.OriginalRequest, provider)
		if HasResponsesCompactionItem(opts.OriginalRequest) {
			expanded, errExpand := ExpandAntigravityCompactionCapsules(opts.OriginalRequest)
			if errExpand != nil {
				return errExpand
			}
			opts.OriginalRequest = expanded
		}
	}
	return nil
}

// PrepareAntigravityCompactionSummaryPayload prepares a payload for non-stream Antigravity summary generation.
func PrepareAntigravityCompactionSummaryPayload(payload []byte, modelName string) []byte {
	out := payload
	summaryPrompt := `{"type":"message","role":"user","content":[{"type":"input_text","text":"Please provide a concise and comprehensive summary of the preceding conversation and task progress so far, including user goals, key findings, actions taken, and current status, so that work can continue smoothly."}]}`

	input := gjson.GetBytes(out, "input")
	if input.IsArray() {
		var filtered []string
		for _, item := range input.Array() {
			itemType := item.Get("type").String()
			if itemType == "compaction_trigger" {
				continue
			}
			filtered = append(filtered, item.Raw)
		}
		filtered = append(filtered, summaryPrompt)
		newInput := "[" + strings.Join(filtered, ",") + "]"
		out, _ = sjson.SetRawBytes(out, "input", []byte(newInput))
	} else if input.Type == gjson.String && input.String() != "" {
		userMsg := []byte(`{"type":"message","role":"user","content":[{"type":"input_text"}]}`)
		userMsg, _ = sjson.SetBytes(userMsg, "content.0.text", input.String())
		newInput := "[" + string(userMsg) + "," + summaryPrompt + "]"
		out, _ = sjson.SetRawBytes(out, "input", []byte(newInput))
	} else {
		newInput := "[" + summaryPrompt + "]"
		out, _ = sjson.SetRawBytes(out, "input", []byte(newInput))
	}

	// Remove fields not needed or incompatible with summary generation
	for _, field := range []string{
		"stream",
		"tools",
		"tool_choice",
		"previous_response_id",
		"parallel_tool_calls",
		"additional_tools",
		"truncation",
		"metadata",
	} {
		out, _ = sjson.DeleteBytes(out, field)
	}

	out, _ = sjson.SetBytes(out, "stream", false)
	return out
}

// deriveAntigravityCompactionKey derives an AES-256 key from the fixed CLIProxyAPI secret.
func deriveAntigravityCompactionKey() []byte {
	h := sha256.Sum256([]byte(fixedCompactionKeySecret))
	return h[:]
}

// SealAntigravityCompaction encrypts summary data into an opaque capsule using AES-GCM.
func SealAntigravityCompaction(summary, modelName string) (string, error) {
	data := antigravityCompactionCapsuleData{
		Summary:   summary,
		Model:     modelName,
		CreatedAt: time.Now().Unix(),
	}
	plaintext, errMarshal := json.Marshal(data)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal compaction capsule: %w", errMarshal)
	}

	key := deriveAntigravityCompactionKey()
	block, errCipher := aes.NewCipher(key)
	if errCipher != nil {
		return "", fmt.Errorf("create cipher: %w", errCipher)
	}

	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		return "", fmt.Errorf("create gcm: %w", errGCM)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, errRead := io.ReadFull(rand.Reader, nonce); errRead != nil {
		return "", fmt.Errorf("generate nonce: %w", errRead)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return antigravityCompactionCapsulePrefix + base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// UnsealAntigravityCompaction decrypts and validates an opaque capsule.
func UnsealAntigravityCompaction(encryptedContent string) (string, error) {
	if !strings.HasPrefix(encryptedContent, antigravityCompactionCapsulePrefix) {
		return "", fmt.Errorf("unrecognized compaction capsule format")
	}

	raw := strings.TrimPrefix(encryptedContent, antigravityCompactionCapsulePrefix)
	ciphertext, errDecode := base64.RawURLEncoding.DecodeString(raw)
	if errDecode != nil {
		return "", fmt.Errorf("decode compaction capsule: %w", errDecode)
	}

	key := deriveAntigravityCompactionKey()
	block, errCipher := aes.NewCipher(key)
	if errCipher != nil {
		return "", fmt.Errorf("create cipher: %w", errCipher)
	}

	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		return "", fmt.Errorf("create gcm: %w", errGCM)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize+gcm.Overhead() {
		return "", fmt.Errorf("compaction capsule ciphertext too short")
	}

	nonce, encrypted := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, errOpen := gcm.Open(nil, nonce, encrypted, nil)
	if errOpen != nil {
		return "", fmt.Errorf("invalid or corrupted compaction capsule: %w", errOpen)
	}

	var data antigravityCompactionCapsuleData
	if errUnmarshal := json.Unmarshal(plaintext, &data); errUnmarshal != nil {
		return "", fmt.Errorf("unmarshal compaction capsule: %w", errUnmarshal)
	}
	return data.Summary, nil
}

// ExpandAntigravityCompactionCapsules expands compaction items in input into developer context.
func ExpandAntigravityCompactionCapsules(payload []byte) ([]byte, error) {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload, nil
	}

	var expandedItems []string
	changed := false
	for _, item := range input.Array() {
		if item.Get("type").String() == "compaction" {
			encryptedContent := item.Get("encrypted_content").String()
			summary, errUnseal := UnsealAntigravityCompaction(encryptedContent)
			if errUnseal != nil {
				return nil, fmt.Errorf("invalid compaction capsule: %w", errUnseal)
			}
			devMsg := []byte(`{"type":"message","role":"developer","content":[{"type":"input_text"}]}`)
			devMsg, _ = sjson.SetBytes(devMsg, "content.0.text", "Context summary from previous turns:\n"+summary)
			expandedItems = append(expandedItems, string(devMsg))
			changed = true
			continue
		}
		expandedItems = append(expandedItems, item.Raw)
	}

	if !changed {
		return payload, nil
	}

	newInput := "[" + strings.Join(expandedItems, ",") + "]"
	return sjson.SetRawBytes(payload, "input", []byte(newInput))
}

// ExtractAntigravitySummaryText extracts summary text from Antigravity or Responses response.
func ExtractAntigravitySummaryText(respPayload []byte) (string, error) {
	// 1. OpenAI Responses format: iterate through output array and skip reasoning
	output := gjson.GetBytes(respPayload, "output")
	if output.IsArray() {
		var textParts []string
		for _, item := range output.Array() {
			if item.Get("type").String() == "message" {
				content := item.Get("content")
				if content.IsArray() {
					for _, part := range content.Array() {
						partType := part.Get("type").String()
						if partType == "output_text" || partType == "text" {
							if t := part.Get("text").String(); t != "" {
								textParts = append(textParts, t)
							}
						}
					}
				} else if content.Type == gjson.String && content.String() != "" {
					textParts = append(textParts, content.String())
				}
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n"), nil
		}
	}

	// 2. Antigravity Gemini format: response.candidates.0.content.parts or candidates.0.content.parts
	candidates := gjson.GetBytes(respPayload, "response.candidates")
	if !candidates.Exists() {
		candidates = gjson.GetBytes(respPayload, "candidates")
	}
	if candidates.IsArray() && len(candidates.Array()) > 0 {
		var textParts []string
		parts := candidates.Array()[0].Get("content.parts")
		if parts.IsArray() {
			for _, part := range parts.Array() {
				if part.Get("thought").Bool() {
					continue
				}
				if t := part.Get("text").String(); t != "" {
					textParts = append(textParts, t)
				}
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n"), nil
		}
	}

	// 3. Claude format: content array
	content := gjson.GetBytes(respPayload, "content")
	if content.IsArray() {
		var textParts []string
		for _, block := range content.Array() {
			if block.Get("type").String() == "text" {
				if t := block.Get("text").String(); t != "" {
					textParts = append(textParts, t)
				}
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n"), nil
		}
	}

	// 4. OpenAI Chat format: choices.0.message.content
	if text := gjson.GetBytes(respPayload, "choices.0.message.content").String(); text != "" {
		return text, nil
	}

	return "", fmt.Errorf("no summary text found in upstream response")
}

// BuildAntigravityCompactionStreamChunks creates SSE frames for compaction stream response.
func BuildAntigravityCompactionStreamChunks(modelName, capsule string, inputTokens, outputTokens, totalTokens int) [][]byte {
	now := time.Now().Unix()
	responseID := fmt.Sprintf("resp_ag_compact_%d", time.Now().UnixNano())
	itemID := fmt.Sprintf("cmp_ag_compact_%d", time.Now().UnixNano())

	itemInProgress := []byte(`{"type":"compaction","status":"in_progress"}`)
	itemInProgress, _ = sjson.SetBytes(itemInProgress, "id", itemID)
	itemInProgress, _ = sjson.SetBytes(itemInProgress, "encrypted_content", capsule)

	itemCompleted := []byte(`{"type":"compaction","status":"completed"}`)
	itemCompleted, _ = sjson.SetBytes(itemCompleted, "id", itemID)
	itemCompleted, _ = sjson.SetBytes(itemCompleted, "encrypted_content", capsule)

	usageNode := []byte(`{"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`)
	usageNode, _ = sjson.SetBytes(usageNode, "input_tokens", inputTokens)
	usageNode, _ = sjson.SetBytes(usageNode, "output_tokens", outputTokens)
	usageNode, _ = sjson.SetBytes(usageNode, "total_tokens", totalTokens)

	createdResponse := []byte(`{"object":"response","status":"in_progress","background":false,"error":null,"output":[]}`)
	createdResponse, _ = sjson.SetBytes(createdResponse, "id", responseID)
	createdResponse, _ = sjson.SetBytes(createdResponse, "created_at", now)
	createdResponse, _ = sjson.SetBytes(createdResponse, "model", modelName)

	inProgressResponse := createdResponse

	completedResponse := []byte(`{"object":"response","status":"completed","background":false,"error":null}`)
	completedResponse, _ = sjson.SetBytes(completedResponse, "id", responseID)
	completedResponse, _ = sjson.SetBytes(completedResponse, "created_at", now)
	completedResponse, _ = sjson.SetBytes(completedResponse, "completed_at", now)
	completedResponse, _ = sjson.SetBytes(completedResponse, "model", modelName)
	completedResponse, _ = sjson.SetRawBytes(completedResponse, "output", []byte("["+string(itemCompleted)+"]"))
	completedResponse, _ = sjson.SetRawBytes(completedResponse, "usage", usageNode)

	createdPayload := []byte(`{"type":"response.created","sequence_number":0}`)
	createdPayload, _ = sjson.SetRawBytes(createdPayload, "response", createdResponse)

	inProgressPayload := []byte(`{"type":"response.in_progress","sequence_number":1}`)
	inProgressPayload, _ = sjson.SetRawBytes(inProgressPayload, "response", inProgressResponse)

	addedPayload := []byte(`{"type":"response.output_item.added","sequence_number":2,"output_index":0}`)
	addedPayload, _ = sjson.SetRawBytes(addedPayload, "item", itemInProgress)

	donePayload := []byte(`{"type":"response.output_item.done","sequence_number":3,"output_index":0}`)
	donePayload, _ = sjson.SetRawBytes(donePayload, "item", itemCompleted)

	completedPayload := []byte(`{"type":"response.completed","sequence_number":4}`)
	completedPayload, _ = sjson.SetRawBytes(completedPayload, "response", completedResponse)

	frames := [][]byte{
		buildSSEFrame("response.created", createdPayload),
		buildSSEFrame("response.in_progress", inProgressPayload),
		buildSSEFrame("response.output_item.added", addedPayload),
		buildSSEFrame("response.output_item.done", donePayload),
		buildSSEFrame("response.completed", completedPayload),
	}
	return frames
}

// BuildAntigravityCompactionResponse creates JSON response for non-stream response.compaction.
func BuildAntigravityCompactionResponse(modelName, capsule string, inputTokens, outputTokens, totalTokens int) []byte {
	now := time.Now().Unix()
	responseID := fmt.Sprintf("resp_ag_compact_%d", time.Now().UnixNano())
	itemID := fmt.Sprintf("cmp_ag_compact_%d", time.Now().UnixNano())

	itemJSON := []byte(`{"type":"compaction","status":"completed"}`)
	itemJSON, _ = sjson.SetBytes(itemJSON, "id", itemID)
	itemJSON, _ = sjson.SetBytes(itemJSON, "encrypted_content", capsule)

	usageJSON := []byte(`{"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`)
	usageJSON, _ = sjson.SetBytes(usageJSON, "input_tokens", inputTokens)
	usageJSON, _ = sjson.SetBytes(usageJSON, "output_tokens", outputTokens)
	usageJSON, _ = sjson.SetBytes(usageJSON, "total_tokens", totalTokens)

	respJSON := []byte(`{"object":"response.compaction","status":"completed"}`)
	respJSON, _ = sjson.SetBytes(respJSON, "id", responseID)
	respJSON, _ = sjson.SetBytes(respJSON, "created_at", now)
	respJSON, _ = sjson.SetBytes(respJSON, "model", modelName)
	respJSON, _ = sjson.SetRawBytes(respJSON, "output", []byte("["+string(itemJSON)+"]"))
	respJSON, _ = sjson.SetRawBytes(respJSON, "usage", usageJSON)

	return respJSON
}

// ExtractResponsesCompactionUsage extracts usage counters from an OpenAI Responses summary payload.
func ExtractResponsesCompactionUsage(payload []byte) (inputTokens, outputTokens, totalTokens, cachedTokens, reasoningTokens int) {
	if detail, ok := ParseCodexUsage(payload); ok && (detail.TotalTokens > 0 || detail.InputTokens > 0 || detail.OutputTokens > 0) {
		return int(detail.InputTokens), int(detail.OutputTokens), int(detail.TotalTokens), int(detail.CachedTokens), int(detail.ReasoningTokens)
	}
	parsed := ParseOpenAIUsage(payload)
	return int(parsed.InputTokens), int(parsed.OutputTokens), int(parsed.TotalTokens), int(parsed.CachedTokens), int(parsed.ReasoningTokens)
}

// PatchResponsesCompactionStreamUsage updates usage counters on a compaction SSE chunk.
func PatchResponsesCompactionStreamUsage(chunk []byte, inputTokens, outputTokens, totalTokens, cachedTokens, reasoningTokens int) []byte {
	const dataPrefix = "data: "
	idx := bytes.Index(chunk, []byte(dataPrefix))
	if idx < 0 {
		return chunk
	}
	data := bytes.TrimSpace(chunk[idx+len(dataPrefix):])
	path := ""
	switch {
	case gjson.GetBytes(data, "response.usage").Exists():
		path = "response.usage"
	case gjson.GetBytes(data, "usage").Exists():
		path = "usage"
	default:
		return chunk
	}
	data, _ = sjson.SetBytes(data, path+".input_tokens", inputTokens)
	data, _ = sjson.SetBytes(data, path+".output_tokens", outputTokens)
	data, _ = sjson.SetBytes(data, path+".total_tokens", totalTokens)
	data, _ = sjson.SetBytes(data, path+".input_tokens_details.cached_tokens", cachedTokens)
	data, _ = sjson.SetBytes(data, path+".output_tokens_details.reasoning_tokens", reasoningTokens)
	var buf bytes.Buffer
	buf.Write(chunk[:idx])
	buf.WriteString(dataPrefix)
	buf.Write(data)
	buf.WriteString("\n\n")
	return buf.Bytes()
}

// ValidateResponsesCompactionSummaryStatus ensures the upstream summary response finished with status "completed".
func ValidateResponsesCompactionSummaryStatus(summaryPayload []byte) error {
	status := strings.ToLower(strings.TrimSpace(gjson.GetBytes(summaryPayload, "status").String()))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(gjson.GetBytes(summaryPayload, "response.status").String()))
	}
	if status != "completed" {
		if status == "" {
			status = "missing"
		}
		return fmt.Errorf("compaction summary response status is %q, want \"completed\"", status)
	}
	if eventType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(summaryPayload, "type").String())); eventType == "response.incomplete" || eventType == "response.failed" {
		return fmt.Errorf("compaction summary response terminal event is %q", eventType)
	}
	for _, path := range []string{"incomplete_details", "response.incomplete_details", "error", "response.error"} {
		if node := gjson.GetBytes(summaryPayload, path); node.Exists() && node.Type != gjson.Null {
			return fmt.Errorf("compaction summary response contains %s", path)
		}
	}
	if output := gjson.GetBytes(summaryPayload, "output"); output.IsArray() {
		for _, item := range output.Array() {
			itemStatus := strings.ToLower(strings.TrimSpace(item.Get("status").String()))
			if itemStatus != "" && itemStatus != "completed" {
				return fmt.Errorf("compaction summary output item status is %q, want \"completed\"", itemStatus)
			}
		}
	}
	return nil
}

// BuildResponsesCompactionResponseFromSummary seals summary text from an OpenAI Responses payload into a response.compaction payload.
func BuildResponsesCompactionResponseFromSummary(modelName string, summaryPayload []byte) ([]byte, error) {
	if errStatus := ValidateResponsesCompactionSummaryStatus(summaryPayload); errStatus != nil {
		return nil, errStatus
	}
	summaryText, errExtract := ExtractAntigravitySummaryText(summaryPayload)
	if errExtract != nil {
		return nil, fmt.Errorf("extract summary: %w", errExtract)
	}
	capsule, errSeal := SealAntigravityCompaction(summaryText, modelName)
	if errSeal != nil {
		return nil, fmt.Errorf("seal compaction capsule: %w", errSeal)
	}
	inputTokens, outputTokens, totalTokens, cachedTokens, reasoningTokens := ExtractResponsesCompactionUsage(summaryPayload)
	respPayload := BuildAntigravityCompactionResponse(modelName, capsule, inputTokens, outputTokens, totalTokens)
	respPayload, _ = sjson.SetBytes(respPayload, "usage.input_tokens_details.cached_tokens", cachedTokens)
	respPayload, _ = sjson.SetBytes(respPayload, "usage.output_tokens_details.reasoning_tokens", reasoningTokens)
	return respPayload, nil
}

// BuildResponsesCompactionStreamChunksFromResponse converts a sealed response.compaction payload into SSE stream chunks.
func BuildResponsesCompactionStreamChunksFromResponse(modelName string, compactionPayload []byte) ([][]byte, error) {
	item := gjson.GetBytes(compactionPayload, "output.0")
	capsule := item.Get("encrypted_content").String()
	if item.Get("type").String() != "compaction" || capsule == "" {
		return nil, fmt.Errorf("extract summary: compaction item missing")
	}
	inputTokens := int(gjson.GetBytes(compactionPayload, "usage.input_tokens").Int())
	outputTokens := int(gjson.GetBytes(compactionPayload, "usage.output_tokens").Int())
	totalTokens := int(gjson.GetBytes(compactionPayload, "usage.total_tokens").Int())
	cachedTokens := int(gjson.GetBytes(compactionPayload, "usage.input_tokens_details.cached_tokens").Int())
	reasoningTokens := int(gjson.GetBytes(compactionPayload, "usage.output_tokens_details.reasoning_tokens").Int())
	chunks := BuildAntigravityCompactionStreamChunks(modelName, capsule, inputTokens, outputTokens, totalTokens)
	for i := range chunks {
		chunks[i] = PatchResponsesCompactionStreamUsage(chunks[i], inputTokens, outputTokens, totalTokens, cachedTokens, reasoningTokens)
	}
	return chunks, nil
}

func buildSSEFrame(event string, data []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("event: ")
	buf.WriteString(event)
	buf.WriteString("\ndata: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return buf.Bytes()
}
