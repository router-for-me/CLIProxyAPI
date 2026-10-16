package helps

import (
	"encoding/base64"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

const devinToolIDPrefix = "cpa_tid_v1_"

func isPortableToolCallID(id string) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// EncodeDevinToolCallID reserves a namespace so native IDs cannot collide with
// encoded IDs. Empty deltas remain empty until upstream supplies the call ID.
func EncodeDevinToolCallID(id string) string {
	if id == "" || (isPortableToolCallID(id) && !strings.HasPrefix(id, devinToolIDPrefix)) {
		return id
	}
	return devinToolIDPrefix + base64.RawURLEncoding.EncodeToString([]byte(id))
}

// DecodeDevinToolCallID removes exactly one canonical encoding layer. Ordinary
// legal IDs, malformed encodings and arbitrary base64-looking IDs stay intact.
func DecodeDevinToolCallID(id string) string {
	if !strings.HasPrefix(id, devinToolIDPrefix) {
		return id
	}
	decoded, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, devinToolIDPrefix))
	if errDecode != nil || !utf8.Valid(decoded) || len(decoded) == 0 {
		return id
	}
	original := string(decoded)
	if EncodeDevinToolCallID(original) != id {
		return id
	}
	return original
}

// RestoreDevinToolCallIDs runs before protobuf construction and user payload
// rules, keeping assistant calls and tool results in the upstream namespace.
func RestoreDevinToolCallIDs(prompts []DevinPrompt) {
	for i := range prompts {
		for j := range prompts[i].ToolCalls {
			prompts[i].ToolCalls[j].ID = DecodeDevinToolCallID(prompts[i].ToolCalls[j].ID)
		}
		prompts[i].ToolCallID = DecodeDevinToolCallID(prompts[i].ToolCallID)
		prompts[i].OriginalToolCallID = DecodeDevinToolCallID(prompts[i].OriginalToolCallID)
	}
}

// NormalizeClaudeToolCallIDs repairs only client tool ID fields. Existing legal
// IDs (including Devin encodings) are never re-encoded. Legacy collisions get
// a deterministic underscore-prefixed alias, without a process-wide ledger.
func NormalizeClaudeToolCallIDs(body []byte) []byte {
	type idField struct {
		start int
		end   int
		id    string
	}
	var fields []idField
	used := make(map[string]bool)
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			field := ""
			switch block.Get("type").String() {
			case "tool_use":
				field = "id"
			case "tool_result":
				field = "tool_use_id"
			default:
				continue
			}
			value := block.Get(field)
			if value.Type != gjson.String || value.String() == "" {
				continue
			}
			id := value.String()
			if isPortableToolCallID(id) {
				used[id] = true
			} else {
				fields = append(fields, idField{start: value.Index, end: value.Index + len(value.Raw), id: id})
			}
		}
	}
	if len(fields) == 0 {
		return body
	}
	mapped := make(map[string]string)
	// Field offsets are in document order. Copy unchanged spans only once so
	// repairing large transcripts does not copy the whole payload per ID.
	out := make([]byte, 0, len(body))
	offset := 0
	for _, field := range fields {
		id := mapped[field.id]
		if id == "" {
			id = EncodeDevinToolCallID(field.id)
			for used[id] {
				id = "_" + id
			}
			mapped[field.id] = id
			used[id] = true
		}
		out = append(out, body[offset:field.start]...)
		out = strconv.AppendQuote(out, id)
		offset = field.end
	}
	return append(out, body[offset:]...)
}
