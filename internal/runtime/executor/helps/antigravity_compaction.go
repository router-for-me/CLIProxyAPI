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

// PrepareAntigravityCompactionSummaryPayload prepares a payload for non-stream Antigravity summary generation.
func PrepareAntigravityCompactionSummaryPayload(payload []byte, modelName string) []byte {
	return PrepareResponsesCompactionSummaryPayload(payload, modelName)
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
	return ExtractResponsesCompactionSummaryText(respPayload)
}

// BuildAntigravityCompactionStreamChunks creates SSE frames for compaction stream response.
func BuildAntigravityCompactionStreamChunks(modelName, capsule string, inputTokens, outputTokens, totalTokens int) [][]byte {
	return BuildResponsesCompactionStreamChunks(modelName, capsule, "ag_compact", inputTokens, outputTokens, totalTokens)
}

// BuildAntigravityCompactionResponse creates JSON response for non-stream response.compaction.
func BuildAntigravityCompactionResponse(modelName, capsule string, inputTokens, outputTokens, totalTokens int) []byte {
	return BuildResponsesCompactionResponse(modelName, capsule, "ag_compact", inputTokens, outputTokens, totalTokens)
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
