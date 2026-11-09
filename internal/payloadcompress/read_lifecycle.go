package payloadcompress

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// read-lifecycle (upstream engines/readLifecycle): an earlier file read becomes
// stale once the same path is read again or modified later in the conversation.
// Only the tool-result text is replaced; the call records stay intact. Opt-in
// and chat-shape only, matching upstream.

var readLifecycleReadNames = map[string]struct{}{
	"read": {}, "read_file": {}, "readfile": {}, "view": {}, "view_file": {}, "cat": {},
}

var readLifecycleWriteNames = map[string]struct{}{
	"write": {}, "write_file": {}, "writefile": {}, "edit": {}, "edit_file": {},
	"multiedit": {}, "str_replace": {}, "str_replace_editor": {}, "apply_patch": {},
	"patch": {}, "notebook_edit": {}, "notebookedit": {},
}

type readLifecycleCall struct {
	path  string
	order int
}

// applyReadLifecycle collapses stale reads in a chat `messages` body. Other
// shapes, or bodies without tool calls, pass through untouched.
func applyReadLifecycle(body []byte) []byte {
	doc := ojson.NewDocString(string(body))
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return body
	}
	items := messages.Array()
	stale := readLifecycleStale(items)
	if len(stale) == 0 {
		return body
	}
	kept := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		if rewritten, ok := readLifecycleRewrite(item, stale); ok {
			kept = append(kept, rewritten)
			changed = true
			continue
		}
		kept = append(kept, item.Raw)
	}
	if !changed {
		return body
	}
	if err := doc.SetRaw("messages", "["+strings.Join(kept, ",")+"]"); err != nil {
		return body
	}
	return []byte(doc.String())
}

func readLifecycleStale(items []ojson.Result) map[string]string {
	reads := make(map[string]readLifecycleCall)
	last := make(map[string]int)
	order := 0
	add := func(id, name, path string) {
		order++
		last[path] = order
		if _, isRead := readLifecycleReadNames[name]; isRead {
			reads[id] = readLifecycleCall{path: path, order: order}
		}
	}
	for _, item := range items {
		if item.Get("role").String() != "assistant" {
			continue
		}
		for _, block := range item.Get("content").Array() {
			if block.Get("type").String() != "tool_use" {
				continue
			}
			id := block.Get("id").String()
			name := readLifecycleToolName(block.Get("name").String())
			path := readLifecyclePath(block.Get("input"))
			if id != "" && name != "" && path != "" {
				add(id, name, path)
			}
		}
		for _, call := range item.Get("tool_calls").Array() {
			id := call.Get("id").String()
			name := readLifecycleToolName(call.Get("function.name").String())
			path := readLifecyclePathString(call.Get("function.arguments").String())
			if id != "" && name != "" && path != "" {
				add(id, name, path)
			}
		}
	}
	if len(reads) == 0 {
		return nil
	}
	stale := make(map[string]string)
	for id, read := range reads {
		if last[read.path] > read.order {
			stale[id] = readLifecycleStub(read.path)
		}
	}
	return stale
}

func readLifecycleToolName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if _, ok := readLifecycleReadNames[name]; ok {
		return name
	}
	if _, ok := readLifecycleWriteNames[name]; ok {
		return name
	}
	return ""
}

func readLifecycleStub(path string) string {
	return `[read superseded: "` + path + `" was re-read or modified later in the conversation; the current content appears below]`
}

// readLifecycleRewrite replaces the stale tool-result text in one message.
func readLifecycleRewrite(item ojson.Result, stale map[string]string) (string, bool) {
	content := item.Get("content")
	// OpenAI chat: role "tool" with a string content and tool_call_id.
	if content.Type == ojson.String {
		stub, ok := stale[item.Get("tool_call_id").String()]
		if !ok || strings.HasPrefix(content.String(), "[read superseded") {
			return "", false
		}
		sub := ojson.NewDocString(item.Raw)
		if err := sub.Set("content", stub); err != nil {
			return "", false
		}
		return sub.String(), true
	}
	// Anthropic: tool_result blocks inside a user message.
	if !content.IsArray() {
		return "", false
	}
	blocks := content.Array()
	raws := make([]string, 0, len(blocks))
	changed := false
	for _, block := range blocks {
		stub, ok := stale[block.Get("tool_use_id").String()]
		if !ok || block.Get("type").String() != "tool_result" {
			raws = append(raws, block.Raw)
			continue
		}
		rewritten, ok := readLifecycleRewriteBlock(block, stub)
		if ok {
			raws = append(raws, rewritten)
			changed = true
		} else {
			raws = append(raws, block.Raw)
		}
	}
	if !changed {
		return "", false
	}
	sub := ojson.NewDocString(item.Raw)
	if err := sub.SetRaw("content", "["+strings.Join(raws, ",")+"]"); err != nil {
		return "", false
	}
	return sub.String(), true
}

func readLifecycleRewriteBlock(block ojson.Result, stub string) (string, bool) {
	sub := ojson.NewDocString(block.Raw)
	content := sub.Get("content")
	switch {
	case content.Type == ojson.String:
		if strings.HasPrefix(content.String(), "[read superseded") {
			return "", false
		}
		if err := sub.Set("content", stub); err != nil {
			return "", false
		}
		return sub.String(), true
	case content.IsArray():
		parts := content.Array()
		if len(parts) == 0 {
			return "", false
		}
		raws := make([]string, 0, len(parts))
		changed := false
		for _, part := range parts {
			if part.Get("type").String() == "text" {
				raws = append(raws, `{"type":"text","text":`+string(appendJSONString(nil, stub))+`}`)
				changed = true
				continue
			}
			raws = append(raws, part.Raw)
		}
		if !changed {
			return "", false
		}
		if err := sub.SetRaw("content", "["+strings.Join(raws, ",")+"]"); err != nil {
			return "", false
		}
		return sub.String(), true
	}
	return "", false
}

func readLifecyclePath(input ojson.Result) string {
	for _, key := range []string{"file_path", "path", "filePath", "filename", "file", "target_file"} {
		if v := strings.TrimSpace(input.Get(key).String()); v != "" {
			return v
		}
	}
	return ""
}

// readLifecyclePathString parses an OpenAI tool-call arguments JSON string.
func readLifecyclePathString(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return ""
	}
	for _, key := range []string{"file_path", "path", "filePath", "filename", "file", "target_file"} {
		if v := strings.TrimSpace(ojson.Get(arguments, key).String()); v != "" {
			return v
		}
	}
	return ""
}
