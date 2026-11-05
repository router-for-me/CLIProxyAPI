package helps

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const (
	antigravityCompactionCapsulePrefix = "cpa-ag-compact-v1:"
	antigravityCompactionItemIDPrefix  = "cmp_ag_compact_"
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

func isRecognizedCPACompactionItem(item ojson.Result) bool {
	if item.Get("type").String() != "compaction" {
		return false
	}
	if RecognizedAntigravityCompactionCapsule(item.Get("encrypted_content").String()) {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(item.Get("id").String()), antigravityCompactionItemIDPrefix)
}

// HasRecognizedResponsesCompactionItem checks whether input contains a CPA-minted compaction item.
func HasRecognizedResponsesCompactionItem(payload []byte) bool {
	input := ojson.GetBytesNoCopy(payload, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if isRecognizedCPACompactionItem(item) {
			return true
		}
	}
	return false
}

// HasResponsesCompactionTrigger checks whether input contains a compaction_trigger item.
func HasResponsesCompactionTrigger(payload []byte) bool {
	input := ojson.GetBytesNoCopy(payload, "input")
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
	input := ojson.GetBytesNoCopy(payload, "input")
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
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !DropForeignResponsesCompactionItemsDoc(doc, provider) {
		return payload
	}
	return doc.Bytes()
}

// DropForeignResponsesCompactionItemsDoc removes non-CPA compaction items in place on doc.
func DropForeignResponsesCompactionItemsDoc(doc *ojson.Doc, provider string) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}
	dropped := 0
	items := make([]string, 0, len(input.Array()))
	for _, item := range input.Array() {
		if item.Get("type").String() == "compaction" && !isRecognizedCPACompactionItem(item) {
			dropped++
			continue
		}
		items = append(items, item.Raw)
	}
	if dropped == 0 {
		return false
	}
	log.Warnf("%s compaction: dropped %d non-CPA compaction item(s)", provider, dropped)
	return doc.SetRaw("input", "["+strings.Join(items, ",")+"]") == nil
}

// ExpandResponsesCompactionCapsules drops foreign compaction items and expands recognized CPA compaction capsules in req and opts.
func ExpandResponsesCompactionCapsules(req *cliproxyexecutor.Request, opts *cliproxyexecutor.Options, provider string) error {
	sameOriginal := req != nil && opts != nil && len(opts.OriginalRequest) > 0 && bytes.Equal(req.Payload, opts.OriginalRequest)
	if req != nil && len(req.Payload) > 0 {
		doc := ojson.NewDoc(req.Payload)
		changed := DropForeignResponsesCompactionItemsDoc(doc, provider)
		expanded, errExpand := ExpandAntigravityCompactionCapsulesDoc(doc)
		if errExpand != nil {
			return errExpand
		}
		if changed || expanded {
			req.Payload = doc.Bytes()
		}
	}
	if opts != nil && len(opts.OriginalRequest) > 0 {
		if sameOriginal {
			opts.OriginalRequest = req.Payload
			return nil
		}
		doc := ojson.NewDoc(opts.OriginalRequest)
		changed := DropForeignResponsesCompactionItemsDoc(doc, provider)
		expanded, errExpand := ExpandAntigravityCompactionCapsulesDoc(doc)
		if errExpand != nil {
			return errExpand
		}
		if changed || expanded {
			opts.OriginalRequest = doc.Bytes()
		}
	}
	return nil
}

// ExpandRecognizedResponsesCompactionCapsules expands recognized CPA compaction capsules in req and opts while preserving foreign compaction items.
func ExpandRecognizedResponsesCompactionCapsules(req *cliproxyexecutor.Request, opts *cliproxyexecutor.Options) error {
	sameOriginal := req != nil && opts != nil && len(opts.OriginalRequest) > 0 && bytes.Equal(req.Payload, opts.OriginalRequest)
	if req != nil && HasRecognizedResponsesCompactionItem(req.Payload) {
		expanded, errExpand := ExpandRecognizedAntigravityCompactionCapsules(req.Payload)
		if errExpand != nil {
			return errExpand
		}
		req.Payload = expanded
	}
	if opts != nil && len(opts.OriginalRequest) > 0 {
		if sameOriginal {
			opts.OriginalRequest = req.Payload
			return nil
		}
		if HasRecognizedResponsesCompactionItem(opts.OriginalRequest) {
			expanded, errExpand := ExpandRecognizedAntigravityCompactionCapsules(opts.OriginalRequest)
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
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	PrepareAntigravityCompactionSummaryPayloadDoc(doc, modelName)
	return doc.Bytes()
}

// PrepareAntigravityCompactionSummaryPayloadDoc prepares doc in place for non-stream Antigravity summary generation.
func PrepareAntigravityCompactionSummaryPayloadDoc(doc *ojson.Doc, modelName string) {
	if doc == nil || doc.Len() == 0 {
		return
	}
	_ = modelName
	summaryPrompt := `{"type":"message","role":"user","content":[{"type":"input_text","text":"Please provide a concise and comprehensive summary of the preceding conversation and task progress so far, including user goals, key findings, actions taken, and current status, so that work can continue smoothly."}]}`

	input := doc.Get("input")
	if input.IsArray() {
		var filtered []string
		for _, item := range input.Array() {
			itemType := item.Get("type").String()
			if itemType == "compaction_trigger" || itemType == "additional_tools" {
				continue
			}
			filtered = append(filtered, item.Raw)
		}
		filtered = append(filtered, summaryPrompt)
		newInput := "[" + strings.Join(filtered, ",") + "]"
		_ = doc.SetRaw("input", newInput)
	} else if input.Type == ojson.String && input.String() != "" {
		text := input.String()
		_ = doc.SetRaw("input", `[{"type":"message","role":"user","content":[{"type":"input_text"}]},`+summaryPrompt+`]`)
		_ = doc.Set("input.0.content.0.text", text)
	} else {
		newInput := "[" + summaryPrompt + "]"
		_ = doc.SetRaw("input", newInput)
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
		_ = util.DeleteJSONDocIfExists(doc, field)
	}

	_ = doc.Set("stream", false)
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
	encryptedContent = strings.TrimSpace(encryptedContent)
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
	if len(payload) == 0 {
		return payload, nil
	}
	doc := ojson.NewDoc(payload)
	changed, errExpand := ExpandAntigravityCompactionCapsulesDoc(doc)
	if errExpand != nil {
		return nil, errExpand
	}
	if !changed {
		return payload, nil
	}
	return doc.Bytes(), nil
}

// ExpandAntigravityCompactionCapsulesDoc expands compaction items in input into developer context in place on doc.
func ExpandAntigravityCompactionCapsulesDoc(doc *ojson.Doc) (bool, error) {
	if doc == nil || doc.Len() == 0 {
		return false, nil
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false, nil
	}

	type capsuleExpansion struct {
		index   int
		summary string
	}
	var expansions []capsuleExpansion
	for idx, item := range input.Array() {
		if item.Get("type").String() == "compaction" {
			encryptedContent := item.Get("encrypted_content").String()
			summary, errUnseal := UnsealAntigravityCompaction(encryptedContent)
			if errUnseal != nil {
				return false, fmt.Errorf("invalid compaction capsule: %w", errUnseal)
			}
			expansions = append(expansions, capsuleExpansion{index: idx, summary: summary})
		}
	}
	if len(expansions) == 0 {
		return false, nil
	}
	for _, exp := range expansions {
		itemPath := fmt.Sprintf("input.%d", exp.index)
		if errSet := doc.SetRaw(itemPath, `{"type":"message","role":"developer","content":[{"type":"input_text"}]}`); errSet != nil {
			return false, errSet
		}
		if errSet := doc.Set(itemPath+".content.0.text", "Context summary from previous turns:\n"+exp.summary); errSet != nil {
			return false, errSet
		}
	}
	return true, nil
}

// ExpandRecognizedAntigravityCompactionCapsules expands recognized CPA compaction items in input into developer context while leaving non-CPA compaction items untouched.
func ExpandRecognizedAntigravityCompactionCapsules(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return payload, nil
	}
	doc := ojson.NewDoc(payload)
	changed, errExpand := ExpandRecognizedAntigravityCompactionCapsulesDoc(doc)
	if errExpand != nil {
		return nil, errExpand
	}
	if !changed {
		return payload, nil
	}
	return doc.Bytes(), nil
}

// ExpandRecognizedAntigravityCompactionCapsulesDoc expands recognized CPA compaction items in input in place on doc.
func ExpandRecognizedAntigravityCompactionCapsulesDoc(doc *ojson.Doc) (bool, error) {
	if doc == nil || doc.Len() == 0 {
		return false, nil
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false, nil
	}

	type capsuleExpansion struct {
		index   int
		summary string
	}
	var expansions []capsuleExpansion
	for idx, item := range input.Array() {
		if isRecognizedCPACompactionItem(item) {
			encryptedContent := item.Get("encrypted_content").String()
			summary, errUnseal := UnsealAntigravityCompaction(encryptedContent)
			if errUnseal != nil {
				return false, fmt.Errorf("invalid compaction capsule: %w", errUnseal)
			}
			expansions = append(expansions, capsuleExpansion{index: idx, summary: summary})
		}
	}
	if len(expansions) == 0 {
		return false, nil
	}
	for _, exp := range expansions {
		itemPath := fmt.Sprintf("input.%d", exp.index)
		if errSet := doc.SetRaw(itemPath, `{"type":"message","role":"developer","content":[{"type":"input_text"}]}`); errSet != nil {
			return false, errSet
		}
		if errSet := doc.Set(itemPath+".content.0.text", "Context summary from previous turns:\n"+exp.summary); errSet != nil {
			return false, errSet
		}
	}
	return true, nil
}

// ExtractAntigravitySummaryText extracts summary text from Antigravity or Responses response.
func ExtractAntigravitySummaryText(respPayload []byte) (string, error) {
	// 1. OpenAI Responses format: iterate through output array and skip reasoning
	output := ojson.GetBytesNoCopy(respPayload, "output")
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
				} else if content.Type == ojson.String && content.String() != "" {
					textParts = append(textParts, content.String())
				}
			}
		}
		if len(textParts) > 0 {
			return strings.Clone(strings.Join(textParts, "\n")), nil
		}
	}

	// 2. Antigravity Gemini format: response.candidates.0.content.parts or candidates.0.content.parts
	candidates := ojson.GetBytesNoCopy(respPayload, "response.candidates")
	if !candidates.Exists() {
		candidates = ojson.GetBytesNoCopy(respPayload, "candidates")
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
			return strings.Clone(strings.Join(textParts, "\n")), nil
		}
	}

	// 3. Claude format: content array
	content := ojson.GetBytesNoCopy(respPayload, "content")
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
			return strings.Clone(strings.Join(textParts, "\n")), nil
		}
	}

	// 4. OpenAI Chat format: choices.0.message.content
	if text := ojson.GetBytesNoCopy(respPayload, "choices.0.message.content").String(); text != "" {
		return strings.Clone(text), nil
	}

	return "", fmt.Errorf("no summary text found in upstream response")
}

// BuildAntigravityCompactionStreamChunks creates SSE frames for compaction stream response.
func BuildAntigravityCompactionStreamChunks(modelName, capsule string, inputTokens, outputTokens, totalTokens int) [][]byte {
	now := time.Now().Unix()
	responseID := fmt.Sprintf("resp_ag_compact_%d", time.Now().UnixNano())
	itemID := fmt.Sprintf("cmp_ag_compact_%d", time.Now().UnixNano())

	itemInProgressDoc := ojson.NewDocString(`{"type":"compaction","status":"in_progress"}`)
	_ = itemInProgressDoc.Set("id", itemID)
	_ = itemInProgressDoc.Set("encrypted_content", capsule)
	itemInProgress := itemInProgressDoc.Bytes()

	itemCompletedDoc := ojson.NewDocString(`{"type":"compaction","status":"completed"}`)
	_ = itemCompletedDoc.Set("id", itemID)
	_ = itemCompletedDoc.Set("encrypted_content", capsule)
	itemCompleted := itemCompletedDoc.Bytes()

	usageDoc := ojson.NewDocString(`{"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`)
	_ = usageDoc.Set("input_tokens", inputTokens)
	_ = usageDoc.Set("output_tokens", outputTokens)
	_ = usageDoc.Set("total_tokens", totalTokens)
	usageNode := usageDoc.Bytes()

	createdResponseDoc := ojson.NewDocString(`{"object":"response","status":"in_progress","background":false,"error":null,"output":[]}`)
	_ = createdResponseDoc.Set("id", responseID)
	_ = createdResponseDoc.Set("created_at", now)
	_ = createdResponseDoc.Set("model", modelName)
	createdResponse := createdResponseDoc.Bytes()

	inProgressResponse := createdResponse

	completedResponseDoc := ojson.NewDocString(`{"object":"response","status":"completed","background":false,"error":null}`)
	_ = completedResponseDoc.Set("id", responseID)
	_ = completedResponseDoc.Set("created_at", now)
	_ = completedResponseDoc.Set("completed_at", now)
	_ = completedResponseDoc.Set("model", modelName)
	_ = completedResponseDoc.SetRaw("output", "["+string(itemCompleted)+"]")
	_ = completedResponseDoc.SetRawBytes("usage", usageNode)
	completedResponse := completedResponseDoc.Bytes()

	createdPayloadDoc := ojson.NewDocString(`{"type":"response.created","sequence_number":0}`)
	_ = createdPayloadDoc.SetRawBytes("response", createdResponse)
	createdPayload := createdPayloadDoc.Bytes()

	inProgressPayloadDoc := ojson.NewDocString(`{"type":"response.in_progress","sequence_number":1}`)
	_ = inProgressPayloadDoc.SetRawBytes("response", inProgressResponse)
	inProgressPayload := inProgressPayloadDoc.Bytes()

	addedPayloadDoc := ojson.NewDocString(`{"type":"response.output_item.added","sequence_number":2,"output_index":0}`)
	_ = addedPayloadDoc.SetRawBytes("item", itemInProgress)
	addedPayload := addedPayloadDoc.Bytes()

	donePayloadDoc := ojson.NewDocString(`{"type":"response.output_item.done","sequence_number":3,"output_index":0}`)
	_ = donePayloadDoc.SetRawBytes("item", itemCompleted)
	donePayload := donePayloadDoc.Bytes()

	completedPayloadDoc := ojson.NewDocString(`{"type":"response.completed","sequence_number":4}`)
	_ = completedPayloadDoc.SetRawBytes("response", completedResponse)
	completedPayload := completedPayloadDoc.Bytes()

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

	itemDoc := ojson.NewDocString(`{"type":"compaction","status":"completed"}`)
	_ = itemDoc.Set("id", itemID)
	_ = itemDoc.Set("encrypted_content", capsule)

	usageDoc := ojson.NewDocString(`{"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`)
	_ = usageDoc.Set("input_tokens", inputTokens)
	_ = usageDoc.Set("output_tokens", outputTokens)
	_ = usageDoc.Set("total_tokens", totalTokens)

	respDoc := ojson.NewDocString(`{"object":"response.compaction","status":"completed"}`)
	_ = respDoc.Set("id", responseID)
	_ = respDoc.Set("created_at", now)
	_ = respDoc.Set("model", modelName)
	_ = respDoc.SetRaw("output", "["+itemDoc.String()+"]")
	_ = respDoc.SetRaw("usage", usageDoc.String())

	return respDoc.Bytes()
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
	doc := ojson.NewDoc(data)
	path := ""
	switch {
	case doc.Get("response.usage").Exists():
		path = "response.usage"
	case doc.Get("usage").Exists():
		path = "usage"
	default:
		return chunk
	}
	_ = doc.Set(path+".input_tokens", inputTokens)
	_ = doc.Set(path+".output_tokens", outputTokens)
	_ = doc.Set(path+".total_tokens", totalTokens)
	_ = doc.Set(path+".input_tokens_details.cached_tokens", cachedTokens)
	_ = doc.Set(path+".output_tokens_details.reasoning_tokens", reasoningTokens)
	var buf bytes.Buffer
	buf.Write(chunk[:idx])
	buf.WriteString(dataPrefix)
	buf.Write(doc.Bytes())
	buf.WriteString("\n\n")
	return buf.Bytes()
}

// ValidateResponsesCompactionSummaryStatus ensures the upstream summary response finished with status "completed".
func ValidateResponsesCompactionSummaryStatus(summaryPayload []byte) error {
	status := strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(summaryPayload, "status").String()))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(summaryPayload, "response.status").String()))
	}
	if status != "completed" {
		if status == "" {
			status = "missing"
		}
		return fmt.Errorf("compaction summary response status is %q, want \"completed\"", status)
	}
	if eventType := strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(summaryPayload, "type").String())); eventType == "response.incomplete" || eventType == "response.failed" {
		return fmt.Errorf("compaction summary response terminal event is %q", eventType)
	}
	for _, path := range []string{"incomplete_details", "response.incomplete_details", "error", "response.error"} {
		if node := ojson.GetBytesNoCopy(summaryPayload, path); node.Exists() && node.Type != ojson.Null {
			return fmt.Errorf("compaction summary response contains %s", path)
		}
	}
	if output := ojson.GetBytesNoCopy(summaryPayload, "output"); output.IsArray() {
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
	respDoc := ojson.NewDoc(respPayload)
	_ = respDoc.Set("usage.input_tokens_details.cached_tokens", cachedTokens)
	_ = respDoc.Set("usage.output_tokens_details.reasoning_tokens", reasoningTokens)
	return respDoc.Bytes(), nil
}

// BuildResponsesCompactionStreamChunksFromResponse converts a sealed response.compaction payload into SSE stream chunks.
func BuildResponsesCompactionStreamChunksFromResponse(modelName string, compactionPayload []byte) ([][]byte, error) {
	item := ojson.GetBytesNoCopy(compactionPayload, "output.0")
	capsule := item.Get("encrypted_content").String()
	if item.Get("type").String() != "compaction" || capsule == "" {
		return nil, fmt.Errorf("extract summary: compaction item missing")
	}
	inputTokens := int(ojson.GetBytesNoCopy(compactionPayload, "usage.input_tokens").Int())
	outputTokens := int(ojson.GetBytesNoCopy(compactionPayload, "usage.output_tokens").Int())
	totalTokens := int(ojson.GetBytesNoCopy(compactionPayload, "usage.total_tokens").Int())
	cachedTokens := int(ojson.GetBytesNoCopy(compactionPayload, "usage.input_tokens_details.cached_tokens").Int())
	reasoningTokens := int(ojson.GetBytesNoCopy(compactionPayload, "usage.output_tokens_details.reasoning_tokens").Int())
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
