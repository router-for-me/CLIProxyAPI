package helps

import (
	"crypto/sha256"
	"maps"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/tidwall/gjson"
)

type claudeThreadToolsKey struct {
	caller    [32]byte
	messageID string
}

type claudeThreadToolsEntry struct {
	names   map[string]string
	expires time.Time
}

var claudeThreadTools = cache.NewBoundedLRU[
	claudeThreadToolsKey, claudeThreadToolsEntry,
](1024, nil)

type ClaudeThreadTools struct {
	caller  [32]byte
	enabled bool
}

// Continuations omit declarations, so response aliases must use the original
// thread's symbols. A lost symbol table requires full-context client replay.
func NewClaudeThreadTools(body []byte, caller string) (
	ClaudeThreadTools, map[string]string, bool,
) {
	kind := gjson.GetBytes(body, "thread.type").String()
	state := ClaudeThreadTools{
		caller:  sha256.Sum256([]byte(caller)),
		enabled: kind == "create" || kind == "continue",
	}
	if kind != "continue" {
		return state, nil, true
	}
	key := claudeThreadToolsKey{state.caller,
		gjson.GetBytes(body, "thread.previous_message_id").String()}
	entry, found := claudeThreadTools.Get(key)
	if !found || time.Now().After(entry.expires) {
		return state, nil, false
	}
	return state, maps.Clone(entry.names), true
}

func (state ClaudeThreadTools) StoreResponse(
	body []byte, names map[string]string,
) {
	if !state.enabled {
		return
	}
	body = []byte(strings.TrimSpace(strings.TrimPrefix(string(body), "data:")))
	id := gjson.GetBytes(body, "id").String()
	if gjson.GetBytes(body, "type").String() == "message_start" {
		id = gjson.GetBytes(body, "message.id").String()
	}
	if id == "" || len(id) > 256 {
		return
	}
	size := 0
	for alias, original := range names {
		size += len(alias) + len(original) + 64
	}
	if size > 64<<10 {
		return
	}
	key := claudeThreadToolsKey{state.caller, id}
	claudeThreadTools.GetOrAdd(key, func() claudeThreadToolsEntry {
		return claudeThreadToolsEntry{maps.Clone(names), time.Now().Add(time.Hour)}
	})
}
