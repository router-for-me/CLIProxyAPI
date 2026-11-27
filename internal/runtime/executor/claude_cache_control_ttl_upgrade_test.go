package executor

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// The ttl upgrade rebuilds cache_control in native key order. It must keep
// the caller's other fields, or it changes their semantics.
func TestUpgradeClaudeCacheControlTTLDocKeepsOtherFields(t *testing.T) {
	doc := ojson.NewDocString(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"scope":"global","type":"ephemeral","evict_on_complete":true}}]}]}`)
	if !upgradeClaudeCacheControlTTLDoc(doc, claudeCacheControlTTL1h) {
		t.Fatal("upgradeClaudeCacheControlTTLDoc() = false, want true")
	}
	got := ojson.GetBytes(doc.Bytes(), "messages.0.content.0.cache_control").Raw
	want := `{"type":"ephemeral","ttl":"1h","scope":"global","evict_on_complete":true}`
	if got != want {
		t.Fatalf("cache_control = %s, want %s", got, want)
	}
}
