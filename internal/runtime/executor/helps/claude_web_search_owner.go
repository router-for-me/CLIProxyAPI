package helps

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	log "github.com/sirupsen/logrus"
)

// Anthropic binds the encrypted_index of a web search citation and the
// encrypted_content of a web search result to the account that ran the search.
// Replayed under another account, either one fails the whole request with a 400
// such as "Invalid `encrypted_index` in `text` block", and a client that keeps
// them in its history fails the same way on every later turn. The owner of each
// value is recorded as responses pass through, and values that cannot be
// attributed to the outgoing credential are removed before the request is sent.

var (
	claudeEncryptedIndexMarker   = []byte(`"encrypted_index"`)
	claudeEncryptedContentMarker = []byte(`"encrypted_content"`)
)

func claudeWebSearchOwnedValuesPresent(data []byte) bool {
	return bytes.Contains(data, claudeEncryptedIndexMarker) || bytes.Contains(data, claudeEncryptedContentMarker)
}

// RecordClaudeWebSearchOwners records owner for every citation encrypted_index
// and web search result encrypted_content in data, which is either a complete
// Messages response or SSE text (one or more lines).
func RecordClaudeWebSearchOwners(owner string, data []byte) {
	if strings.TrimSpace(owner) == "" || !claudeWebSearchOwnedValuesPresent(data) {
		return
	}
	if content := ojson.GetBytesNoCopy(data, "content"); content.IsArray() {
		content.ForEach(func(_, block ojson.Result) bool {
			recordClaudeWebSearchBlockOwners(owner, block)
			return true
		})
		return
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		event := ojson.ParseBytes(bytes.TrimSpace(line[len("data:"):]))
		switch event.Get("type").String() {
		case "content_block_start":
			recordClaudeWebSearchBlockOwners(owner, event.Get("content_block"))
		case "content_block_delta":
			if event.Get("delta.type").String() == "citations_delta" {
				cache.RecordClaudeWebSearchOwner(event.Get("delta.citation.encrypted_index").String(), owner)
			}
		}
	}
}

func recordClaudeWebSearchBlockOwners(owner string, block ojson.Result) {
	switch block.Get("type").String() {
	case "text":
		block.Get("citations").ForEach(func(_, citation ojson.Result) bool {
			cache.RecordClaudeWebSearchOwner(citation.Get("encrypted_index").String(), owner)
			return true
		})
	case "web_search_tool_result":
		block.Get("content").ForEach(func(_, result ojson.Result) bool {
			cache.RecordClaudeWebSearchOwner(result.Get("encrypted_content").String(), owner)
			return true
		})
	}
}

// DropForeignClaudeWebSearchReplayDoc removes from the assistant turns of a
// Messages request every citation whose encrypted_index, and every web search
// result whose encrypted_content, is not recorded for owner, and reports whether
// it changed doc. Citations without encrypted_index and
// web_search_tool_result_error entries are kept, and a result block left without
// results keeps `content: []`, which Anthropic accepts.
func DropForeignClaudeWebSearchReplayDoc(doc *ojson.Doc, owner string) bool {
	if doc == nil || doc.Len() == 0 || strings.TrimSpace(owner) == "" {
		return false
	}
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return false
	}
	dropped := 0
	for i, message := range messages.Array() {
		if message.Get("role").String() != "assistant" {
			continue
		}
		for j, block := range message.Get("content").Array() {
			var field, path string
			var items ojson.Result
			switch block.Get("type").String() {
			case "text":
				field, path, items = "encrypted_index", fmt.Sprintf("messages.%d.content.%d.citations", i, j), block.Get("citations")
			case "web_search_tool_result":
				field, path, items = "encrypted_content", fmt.Sprintf("messages.%d.content.%d.content", i, j), block.Get("content")
			default:
				continue
			}
			if !items.IsArray() {
				continue
			}
			kept, removed := keepClaudeWebSearchOwned(items, field, owner)
			if removed == 0 {
				continue
			}
			var errUpdate error
			if len(kept) == 0 && field == "encrypted_index" {
				errUpdate = doc.Delete(path)
			} else {
				errUpdate = doc.SetRawBytes(path, []byte("["+strings.Join(kept, ",")+"]"))
			}
			if errUpdate != nil {
				log.Debugf("claude web search replay: update %s failed: %v", path, errUpdate)
				continue
			}
			dropped += removed
		}
	}
	if dropped > 0 {
		log.Debugf("claude web search replay: dropped %d encrypted value(s) not minted for the outgoing credential", dropped)
		return true
	}
	return false
}

func keepClaudeWebSearchOwned(items ojson.Result, field, owner string) ([]string, int) {
	var kept []string
	removed := 0
	items.ForEach(func(_, item ojson.Result) bool {
		if value := item.Get(field).String(); strings.TrimSpace(value) != "" {
			if recorded, ok := cache.ClaudeWebSearchOwner(value); !ok || recorded != owner {
				removed++
				return true
			}
		}
		kept = append(kept, item.Raw)
		return true
	})
	return kept, removed
}
