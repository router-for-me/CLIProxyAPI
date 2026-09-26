package helps

import (
	"sort"
	"strconv"

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
