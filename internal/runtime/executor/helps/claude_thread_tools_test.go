package helps

import (
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestClaudeThreadToolsIsolationAndExpiry(t *testing.T) {
	id := "msg-" + uuid.NewString()
	create := []byte(`{"thread":{"type":"create"}}`)
	continuation := []byte(`{"thread":{"type":"continue","previous_message_id":"` + id + `"}}`)
	state, _, _ := NewClaudeThreadTools(create, "caller-one")
	names := map[string]string{"alias": "Read"}
	state.StoreResponse([]byte(`data: {"type":"message_start","message":{"id":"`+id+`"}}`), names)
	names["alias"] = "Bash"
	_, loaded, found := NewClaudeThreadTools(continuation, "caller-one")
	if !found || loaded["alias"] != "Read" {
		t.Fatal("stored mapping mutated")
	}
	loaded["alias"] = "Write"
	_, loaded, found = NewClaudeThreadTools(continuation, "caller-one")
	if !found || loaded["alias"] != "Read" {
		t.Fatal("reader mutated cache")
	}
	if _, _, found = NewClaudeThreadTools(continuation, "caller-two"); found {
		t.Fatal("mapping leaked across callers")
	}
	key := claudeThreadToolsKey{state.caller, id}
	entry, _ := claudeThreadTools.Get(key)
	claudeThreadTools.Delete(key)
	entry.expires = time.Now().Add(-time.Hour)
	claudeThreadTools.GetOrAdd(key, func() claudeThreadToolsEntry { return entry })
	if _, _, found = NewClaudeThreadTools(continuation, "caller-one"); found {
		t.Fatal("expired mapping was used")
	}
	claudeThreadTools.Delete(key)
}
