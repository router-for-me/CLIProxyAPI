package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestFileTokenStorePersistsProxyPoolNodeMetadata verifies that the sticky node
// binding stored in Auth.Metadata survives a save/relist cycle on disk.
func TestFileTokenStorePersistsProxyPoolNodeMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")
	initial := []byte(`{"type":"claude","access_token":"access","refresh_token":"refresh","proxy_pool_id":"pool-us"}`)
	if errWrite := os.WriteFile(path, initial, 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	loaded, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(loaded) != 1 || loaded[0] == nil {
		t.Fatalf("List() returned %d auths, want 1", len(loaded))
	}
	auth := loaded[0]
	if got := auth.Attributes[cliproxyauth.AttributeProxyPoolID]; got != "pool-us" {
		t.Fatalf("Attributes[proxy_pool_id] = %q, want pool-us", got)
	}

	const node = "http://proxy-a.example.com:8080"
	auth.ProxyURL = node
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata[cliproxyauth.MetadataProxyPoolNode] = node
	if _, errSave := store.Save(context.Background(), auth); errSave != nil {
		t.Fatalf("Save() error = %v", errSave)
	}

	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var onDisk map[string]any
	if errUnmarshal := json.Unmarshal(raw, &onDisk); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if got, _ := onDisk[cliproxyauth.MetadataProxyPoolNode].(string); got != node {
		t.Fatalf("on-disk %s = %q, want %q", cliproxyauth.MetadataProxyPoolNode, got, node)
	}

	reloaded, errRelist := store.List(context.Background())
	if errRelist != nil {
		t.Fatalf("List() after save error = %v", errRelist)
	}
	if len(reloaded) != 1 || reloaded[0] == nil {
		t.Fatalf("List() after save returned %d auths, want 1", len(reloaded))
	}
	if got := cliproxyauth.ProxyPoolNode(reloaded[0]); got != node {
		t.Fatalf("ProxyPoolNode() after restart = %q, want %q", got, node)
	}
	if reloaded[0].ProxyURL != "" {
		t.Fatalf("ProxyURL after reload = %q, want empty (re-resolved from the binding)", reloaded[0].ProxyURL)
	}
}
