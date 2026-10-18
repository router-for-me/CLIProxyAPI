package helps

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	responsesV1CompactionCapsulePrefix = "cpa-responses-v1-compaction-v1."
	responsesV1CompactionFamilyPrefix  = "cpa-responses-v1-compaction-"
	responsesV1CompactionDomain        = "cpa-responses-v1-compaction\x00"
	responsesV1CompactionInstruction   = "Summarize the conversation so far for continuation. Preserve the user's goals, constraints, decisions, important facts, and unfinished work. Return only the summary."
)

type responsesV1CompactionCapsule struct {
	Version int    `json:"version"`
	Summary string `json:"summary"`
}

// ResponsesCompactionError marks failures that belong to the request protocol.
type ResponsesCompactionError struct{ Message string }

func (e ResponsesCompactionError) Error() string       { return e.Message }
func (ResponsesCompactionError) StatusCode() int       { return http.StatusBadGateway }
func (ResponsesCompactionError) IsRequestScoped() bool { return true }

func validateResponsesCompactionTerminalOutput(event []byte) error {
	response := gjson.GetBytes(event, "response")
	if response.IsObject() {
		if output := response.Get("output"); !output.Exists() || output.IsArray() {
			return nil
		}
		return ResponsesCompactionError{Message: "compaction upstream returned invalid output"}
	}
	return ResponsesCompactionError{Message: "compaction upstream completed event omitted its response"}
}

// PrepareV1CompactionPayload keeps the trigger and conversation while requesting a summary.
func PrepareV1CompactionPayload(payload []byte) []byte {
	if !gjson.ValidBytes(payload) || !gjson.ParseBytes(payload).IsObject() {
		return payload
	}
	input := gjson.GetBytes(payload, "input")
	items := make([]json.RawMessage, 0, 2)
	if input.IsArray() {
		insertPromptBeforeTrigger := false
		for _, item := range input.Array() {
			if !insertPromptBeforeTrigger && item.Get("type").String() == "compaction_trigger" {
				items = append(items, responsesV1CompactionSummaryMessage())
				insertPromptBeforeTrigger = true
			}
			items = append(items, json.RawMessage(item.Raw))
		}
		if !insertPromptBeforeTrigger {
			items = append(items, responsesV1CompactionSummaryMessage())
		}
	} else if input.Type == gjson.String {
		message, _ := json.Marshal(map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []map[string]string{{"type": "input_text", "text": input.String()}},
		})
		items = append(items, message)
		items = append(items, responsesV1CompactionSummaryMessage())
	} else if input.Exists() && input.Type != gjson.Null {
		items = append(items, json.RawMessage(input.Raw))
		items = append(items, responsesV1CompactionSummaryMessage())
	} else {
		items = append(items, responsesV1CompactionSummaryMessage())
	}
	prepared, err := sjson.SetBytes(payload, "input", items)
	if err != nil {
		return payload
	}
	for _, field := range []string{"tools", "tool_choice", "parallel_tool_calls", "text", "response_format", "context_management"} {
		prepared, _ = sjson.DeleteBytes(prepared, field)
	}
	prepared, _ = sjson.SetBytes(prepared, "stream", true)
	return prepared
}

func responsesV1CompactionSummaryMessage() json.RawMessage {
	message, _ := json.Marshal(map[string]any{
		"type":    "message",
		"role":    "user",
		"content": []map[string]string{{"type": "input_text", "text": responsesV1CompactionInstruction}},
	})
	return message
}

// ExpandResponsesCompactionCapsules expands only capsules created by this bridge.
func ExpandResponsesCompactionCapsules(payload []byte, scope string, secrets []string) ([]byte, error) {
	if !gjson.ValidBytes(payload) || !gjson.ParseBytes(payload).IsObject() {
		return nil, ResponsesCompactionError{Message: "compaction request is invalid JSON"}
	}
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload, nil
	}
	items := make([]json.RawMessage, 0, len(input.Array()))
	changed := false
	for _, item := range input.Array() {
		encoded := item.Get("encrypted_content").String()
		if item.Get("type").String() == "compaction" && strings.HasPrefix(encoded, responsesV1CompactionFamilyPrefix) {
			if !strings.HasPrefix(encoded, responsesV1CompactionCapsulePrefix) {
				return nil, ResponsesCompactionError{Message: "compaction capsule version is unsupported"}
			}
			summary, err := openResponsesV1CompactionCapsule(encoded, scope, secrets)
			if err != nil {
				return nil, err
			}
			message, errMarshal := json.Marshal(map[string]any{
				"type":    "message",
				"role":    "developer",
				"content": []map[string]string{{"type": "input_text", "text": "Context summary from previous turns:\n" + summary}},
			})
			if errMarshal != nil {
				return nil, ResponsesCompactionError{Message: "compaction summary could not be expanded"}
			}
			items = append(items, message)
			changed = true
		} else {
			items = append(items, json.RawMessage(item.Raw))
		}
	}
	if !changed {
		return payload, nil
	}
	result, err := sjson.SetBytes(payload, "input", items)
	if err != nil {
		return nil, ResponsesCompactionError{Message: "compaction summary could not be expanded"}
	}
	return result, nil
}

// ConvertResponsesCompactionResponse adds one authenticated capsule to a completed summary response.
func ConvertResponsesCompactionResponse(payload []byte, model, scope string, secrets []string) ([]byte, error) {
	if !gjson.ValidBytes(payload) || !gjson.ParseBytes(payload).IsObject() {
		return nil, ResponsesCompactionError{Message: "compaction upstream returned invalid JSON"}
	}
	output := gjson.GetBytes(payload, "output")
	if !output.IsArray() {
		return nil, ResponsesCompactionError{Message: "compaction upstream returned invalid output"}
	}
	nativeCount := 0
	for _, item := range output.Array() {
		if item.Get("type").String() == "compaction" {
			nativeCount++
		}
	}
	if nativeCount > 0 {
		if err := ValidateResponsesCompaction(payload); err != nil {
			return nil, err
		}
		return completeResponsesEnvelope(payload, model)
	}
	if gjson.GetBytes(payload, "status").String() != "completed" {
		return nil, ResponsesCompactionError{Message: "compaction upstream did not complete"}
	}
	if upstreamError := gjson.GetBytes(payload, "error"); upstreamError.Exists() && upstreamError.Type != gjson.Null {
		return nil, ResponsesCompactionError{Message: "compaction upstream returned an error"}
	}
	summary, errSummary := responsesV1CompactionSummary(output)
	if errSummary != nil {
		return nil, errSummary
	}
	capsule, errSeal := sealResponsesV1CompactionCapsule(summary, scope, secrets)
	if errSeal != nil {
		return nil, errSeal
	}
	result, errEnvelope := completeResponsesEnvelope(payload, model)
	if errEnvelope != nil {
		return nil, errEnvelope
	}
	items := make([]json.RawMessage, 0, len(output.Array())+1)
	for _, item := range output.Array() {
		items = append(items, json.RawMessage(item.Raw))
	}
	compactionItem, _ := json.Marshal(map[string]string{
		"id":                "cmp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"type":              "compaction",
		"encrypted_content": capsule,
	})
	items = append(items, compactionItem)
	result, errAppend := sjson.SetBytes(result, "output", items)
	if errAppend != nil {
		return nil, ResponsesCompactionError{Message: "compaction response could not be encoded"}
	}
	return result, nil
}

func completeResponsesEnvelope(payload []byte, model string) ([]byte, error) {
	result, err := sjson.SetBytes(payload, "object", "response")
	if err != nil {
		return nil, ResponsesCompactionError{Message: "compaction response could not be encoded"}
	}
	result, _ = sjson.SetBytes(result, "status", "completed")
	result, _ = sjson.SetBytes(result, "error", nil)
	result, _ = sjson.SetBytes(result, "background", false)
	if !gjson.GetBytes(result, "model").Exists() && strings.TrimSpace(model) != "" {
		result, _ = sjson.SetBytes(result, "model", model)
	}
	if !gjson.GetBytes(result, "id").Exists() || strings.TrimSpace(gjson.GetBytes(result, "id").String()) == "" {
		result, _ = sjson.SetBytes(result, "id", "resp_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	}
	if !gjson.GetBytes(result, "created_at").Exists() {
		result, _ = sjson.SetBytes(result, "created_at", time.Now().Unix())
	}
	return result, nil
}

func responsesV1CompactionSummary(output gjson.Result) (string, error) {
	var parts []string
	for _, item := range output.Array() {
		if item.Get("type").String() == "message" && item.Get("role").String() == "assistant" {
			if status := item.Get("status"); status.Exists() && status.String() != "completed" {
				return "", ResponsesCompactionError{Message: "compaction upstream returned an incomplete summary"}
			}
			for _, content := range item.Get("content").Array() {
				if content.Get("type").String() == "output_text" {
					text := content.Get("text")
					if text.Type != gjson.String {
						return "", ResponsesCompactionError{Message: "compaction upstream returned invalid summary text"}
					}
					if summary := strings.TrimSpace(text.String()); summary != "" {
						parts = append(parts, summary)
					}
				}
			}
		}
	}
	if len(parts) == 0 {
		return "", ResponsesCompactionError{Message: "compaction upstream returned no assistant summary"}
	}
	return strings.Join(parts, "\n"), nil
}

func sealResponsesV1CompactionCapsule(summary, scope string, secrets []string) (string, error) {
	if strings.TrimSpace(summary) == "" {
		return "", ResponsesCompactionError{Message: "compaction summary is empty"}
	}
	if strings.TrimSpace(scope) == "" {
		return "", ResponsesCompactionError{Message: "compaction capsule scope is missing"}
	}
	keys := responsesV1CompactionKeys(scope, secrets)
	if len(keys) == 0 {
		return "", ResponsesCompactionError{Message: "compaction capsule requires a configured credential"}
	}
	plaintext, errMarshal := json.Marshal(responsesV1CompactionCapsule{Version: 1, Summary: summary})
	if errMarshal != nil {
		return "", ResponsesCompactionError{Message: "compaction summary could not be sealed"}
	}
	block, errCipher := aes.NewCipher(keys[0])
	if errCipher != nil {
		return "", ResponsesCompactionError{Message: "compaction summary could not be sealed"}
	}
	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		return "", ResponsesCompactionError{Message: "compaction summary could not be sealed"}
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, errRead := io.ReadFull(rand.Reader, nonce); errRead != nil {
		return "", ResponsesCompactionError{Message: "compaction summary could not be sealed"}
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, responsesV1CompactionAAD(scope))
	return responsesV1CompactionCapsulePrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func openResponsesV1CompactionCapsule(encoded, scope string, secrets []string) (string, error) {
	if !strings.HasPrefix(encoded, responsesV1CompactionCapsulePrefix) || strings.TrimSpace(scope) == "" {
		return "", ResponsesCompactionError{Message: "compaction capsule is invalid for this provider"}
	}
	sealed, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, responsesV1CompactionCapsulePrefix))
	if errDecode != nil {
		return "", ResponsesCompactionError{Message: "compaction capsule is corrupted"}
	}
	for _, key := range responsesV1CompactionKeys(scope, secrets) {
		if summary, ok := responsesV1CompactionSummaryWithKey(sealed, scope, key); ok {
			return summary, nil
		}
	}
	return "", ResponsesCompactionError{Message: "compaction capsule cannot be authenticated with this provider"}
}

func responsesV1CompactionSummaryWithKey(sealed []byte, scope string, key []byte) (string, bool) {
	block, errCipher := aes.NewCipher(key)
	if errCipher != nil {
		return "", false
	}
	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil || len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return "", false
	}
	plaintext, errOpen := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], responsesV1CompactionAAD(scope))
	if errOpen != nil {
		return "", false
	}
	var capsule responsesV1CompactionCapsule
	valid := json.Unmarshal(plaintext, &capsule) == nil && capsule.Version == 1 && strings.TrimSpace(capsule.Summary) != ""
	return capsule.Summary, valid
}

func responsesV1CompactionKeys(scope string, secrets []string) [][]byte {
	seen := make(map[[32]byte]struct{}, len(secrets))
	keys := make([][]byte, 0, len(secrets))
	for _, secret := range secrets {
		if normalizedSecret := strings.TrimSpace(secret); normalizedSecret != "" {
			digest := sha256.Sum256([]byte(responsesV1CompactionDomain + strings.TrimSpace(scope) + "\x00" + normalizedSecret))
			if _, exists := seen[digest]; !exists {
				seen[digest] = struct{}{}
				keys = append(keys, digest[:])
			}
		}
	}
	return keys
}

func responsesV1CompactionAAD(scope string) []byte {
	return []byte(responsesV1CompactionDomain + strings.TrimSpace(scope))
}

// BuildResponsesCompactionStream emits item events followed by the complete response.
func BuildResponsesCompactionStream(response []byte) [][]byte {
	initial, _ := sjson.SetBytes(response, "status", "in_progress")
	initial, _ = sjson.SetRawBytes(initial, "output", []byte(`[]`))
	initial, _ = sjson.DeleteBytes(initial, "usage")
	frames := [][]byte{
		responsesCompactionEvent("response.created", 0, "response", initial, -1),
		responsesCompactionEvent("response.in_progress", 1, "response", initial, -1),
	}
	sequence := 2
	for index, item := range gjson.GetBytes(response, "output").Array() {
		frames = append(frames,
			responsesCompactionEvent("response.output_item.added", sequence, "item", []byte(item.Raw), index),
			responsesCompactionEvent("response.output_item.done", sequence+1, "item", []byte(item.Raw), index),
		)
		sequence += 2
	}
	return append(frames, responsesCompactionEvent("response.completed", sequence, "response", response, -1))
}

func responsesCompactionEvent(event string, sequence int, field string, value []byte, index int) []byte {
	data, _ := sjson.SetBytes([]byte(`{}`), "type", event)
	data, _ = sjson.SetBytes(data, "sequence_number", sequence)
	data, _ = sjson.SetRawBytes(data, field, value)
	if index >= 0 {
		data, _ = sjson.SetBytes(data, "output_index", index)
	}
	return buildSSEFrame(event, data)
}

// ValidateResponsesCompaction requires one complete opaque compaction item.
func ValidateResponsesCompaction(payload []byte) error {
	if !gjson.ValidBytes(payload) || !gjson.ParseBytes(payload).IsObject() {
		return ResponsesCompactionError{Message: "compaction upstream returned invalid JSON"}
	}
	if gjson.GetBytes(payload, "status").String() != "completed" {
		return ResponsesCompactionError{Message: "compaction upstream did not complete"}
	}
	if upstreamError := gjson.GetBytes(payload, "error"); upstreamError.Exists() && upstreamError.Type != gjson.Null {
		return ResponsesCompactionError{Message: "compaction upstream returned an error"}
	}
	count := 0
	valid := true
	for _, item := range gjson.GetBytes(payload, "output").Array() {
		if item.Get("type").String() == "compaction" {
			count++
			encrypted := item.Get("encrypted_content")
			valid = valid && encrypted.Type == gjson.String && strings.TrimSpace(encrypted.String()) != ""
		}
	}
	if count != 1 || !valid {
		return ResponsesCompactionError{Message: "compaction upstream did not return exactly one valid compaction item"}
	}
	return nil
}
