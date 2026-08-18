package helps

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NeedsNativeResponses also preserves opaque state when a compacted conversation resumes.
func NeedsNativeResponses(payload []byte) bool {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		switch item.Get("type").String() {
		case "compaction_trigger", "compaction", "compaction_summary":
			return true
		}
	}
	return false
}

// ResponsesCompactionStream retains raw items without narrowing their upstream schema.
// A zero value leaves ordinary Responses events untouched.
type ResponsesCompactionStream struct {
	Required bool
	items    map[string][]byte
	order    map[string]int64
	done     map[string]bool
}

func (s *ResponsesCompactionStream) Normalize(data []byte) []byte {
	if !s.Required {
		return data
	}
	event := gjson.GetBytes(data, "type").String()
	if event == "response.output_item.done" || event == "response.output_item.added" {
		item := gjson.GetBytes(data, "item")
		if !item.IsObject() {
			return data
		}
		if event == "response.output_item.added" && (item.Get("type").String() != "compaction" || item.Get("encrypted_content").String() == "") {
			return data
		}
		index := gjson.GetBytes(data, "output_index")
		key := item.Get("id").String()
		if index.Exists() {
			key = "index:" + strconv.FormatInt(index.Int(), 10)
		}
		if key == "" {
			return data
		}
		if s.items == nil {
			s.items = make(map[string][]byte)
			s.order = make(map[string]int64)
			s.done = make(map[string]bool)
		}
		if event == "response.output_item.added" && s.done[key] {
			return data
		}
		s.items[key] = []byte(item.Raw)
		if index.Exists() {
			s.order[key] = index.Int()
		} else if _, exists := s.order[key]; !exists {
			s.order[key] = int64(len(s.order))
		}
		s.done[key] = event == "response.output_item.done"
		return data
	}
	if event != "response.completed" && event != "response.done" {
		return data
	}
	output := gjson.GetBytes(data, "response.output")
	if (!output.Exists() || output.Type == gjson.Null || output.IsArray() && len(output.Array()) == 0) && len(s.items) > 0 {
		keys := make([]string, 0, len(s.items))
		for key := range s.items {
			keys = append(keys, key)
		}
		sort.SliceStable(keys, func(i, j int) bool {
			if s.order[keys[i]] == s.order[keys[j]] {
				return keys[i] < keys[j]
			}
			return s.order[keys[i]] < s.order[keys[j]]
		})
		raw := []byte{'['}
		for i, key := range keys {
			if i > 0 {
				raw = append(raw, ',')
			}
			raw = append(raw, s.items[key]...)
		}
		raw = append(raw, ']')
		data, _ = sjson.SetRawBytes(data, "response.output", raw)
	}
	count := 0
	valid := true
	for _, item := range gjson.GetBytes(data, "response.output").Array() {
		if item.Get("type").String() == "compaction" {
			count++
			valid = valid && item.Get("encrypted_content").Type == gjson.String && item.Get("encrypted_content").String() != ""
		}
	}
	if count != 1 || !valid {
		// Do not turn an unsupported upstream into a successful ordinary assistant response.
		id := gjson.GetBytes(data, "response.id").String()
		data = []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"unsupported_compaction","message":"Upstream did not return exactly one valid compaction item for remote compaction v2"}}}`)
		if id != "" {
			data, _ = sjson.SetBytes(data, "response.id", id)
		}
		return data
	}
	data, _ = sjson.SetBytes(data, "type", "response.completed")
	return data
}

const (
	responsesV1CompactionCapsulePrefix = "cpa-responses-v1-compaction-v1."
	responsesV1CompactionDomain        = "cpa-responses-v1-compaction\x00"
)

type responsesV1CompactionCapsule struct {
	Version int    `json:"version"`
	Summary string `json:"summary"`
}

func sealResponsesV1CompactionCapsule(summary, scope string, secrets []string) (string, error) {
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("compaction summary is empty")
	}
	if strings.TrimSpace(scope) == "" {
		return "", fmt.Errorf("compaction capsule scope is missing")
	}
	keys := responsesV1CompactionKeys(scope, secrets)
	if len(keys) == 0 {
		return "", fmt.Errorf("compaction capsule requires a configured credential")
	}
	plaintext, errMarshal := json.Marshal(responsesV1CompactionCapsule{Version: 1, Summary: summary})
	if errMarshal != nil {
		return "", fmt.Errorf("compaction summary could not be sealed")
	}
	block, errCipher := aes.NewCipher(keys[0])
	if errCipher != nil {
		return "", fmt.Errorf("compaction summary could not be sealed")
	}
	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		return "", fmt.Errorf("compaction summary could not be sealed")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, errRead := io.ReadFull(rand.Reader, nonce); errRead != nil {
		return "", fmt.Errorf("compaction summary could not be sealed")
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, responsesV1CompactionAAD(scope))
	return responsesV1CompactionCapsulePrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func openResponsesV1CompactionCapsule(encoded, scope string, secrets []string) (string, error) {
	if !strings.HasPrefix(encoded, responsesV1CompactionCapsulePrefix) || strings.TrimSpace(scope) == "" {
		return "", fmt.Errorf("compaction capsule is invalid for this provider")
	}
	sealed, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, responsesV1CompactionCapsulePrefix))
	if errDecode != nil {
		return "", fmt.Errorf("compaction capsule is corrupted")
	}
	for _, key := range responsesV1CompactionKeys(scope, secrets) {
		if summary, ok := responsesV1CompactionSummaryWithKey(sealed, scope, key); ok {
			return summary, nil
		}
	}
	return "", fmt.Errorf("compaction capsule cannot be authenticated with this provider")
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
