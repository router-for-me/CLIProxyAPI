package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeMetadataForSync(t *testing.T) {
	meta := map[string]any{
		"access_token":  "tok123",
		"refresh_token": "ref456",
		"refreshToken":  "ref789",
		"expired":       "2026-01-01T00:00:00Z",
	}
	result := sanitizeMetadataForSync(meta)
	if _, ok := result["refresh_token"]; ok {
		t.Error("refresh_token should be stripped")
	}
	if _, ok := result["refreshToken"]; ok {
		t.Error("refreshToken should be stripped")
	}
	if result["access_token"] != "tok123" {
		t.Error("access_token should be preserved")
	}
	if result["expired"] != "2026-01-01T00:00:00Z" {
		t.Error("expired should be preserved")
	}
}

func TestSanitizeMetadataForSync_Nil(t *testing.T) {
	if sanitizeMetadataForSync(nil) != nil {
		t.Error("nil input should return nil")
	}
}

func TestSyncDataToAuth(t *testing.T) {
	data := AuthSyncData{
		ID:       "test-auth",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "abc"},
	}
	auth := syncDataToAuth(data, "/tmp/auths")
	if auth.ID != "test-auth" {
		t.Errorf("expected ID test-auth, got %s", auth.ID)
	}
	if auth.Provider != "claude" {
		t.Errorf("expected Provider claude, got %s", auth.Provider)
	}
	if auth.FileName != "test-auth.json" {
		t.Errorf("expected FileName test-auth.json, got %s", auth.FileName)
	}
	if auth.Status != StatusActive {
		t.Errorf("expected StatusActive, got %v", auth.Status)
	}
	if auth.Attributes["path"] != filepath.Join("/tmp/auths", "test-auth.json") {
		t.Errorf("unexpected path attribute: %s", auth.Attributes["path"])
	}
}

func TestSyncDataToAuth_AlreadyHasJsonSuffix(t *testing.T) {
	data := AuthSyncData{ID: "my.json", Provider: "claude"}
	auth := syncDataToAuth(data, "/tmp")
	if auth.FileName != "my.json" {
		t.Errorf("should not double-append .json, got %s", auth.FileName)
	}
}

func TestWriteAuthToFile(t *testing.T) {
	dir := t.TempDir()
	data := AuthSyncData{
		ID:       "write-test",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "tok"},
	}
	if err := writeAuthToFile(dir, data); err != nil {
		t.Fatalf("writeAuthToFile failed: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "write-test.json"))
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(content, &meta); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if meta["access_token"] != "tok" {
		t.Errorf("expected access_token=tok, got %v", meta["access_token"])
	}
}

func TestWriteAuthToFile_EmptyDir(t *testing.T) {
	if err := writeAuthToFile("", AuthSyncData{ID: "x"}); err != nil {
		t.Error("empty authDir should return nil")
	}
}

func TestWriteAuthToFile_EmptyID(t *testing.T) {
	if err := writeAuthToFile("/tmp", AuthSyncData{}); err != nil {
		t.Error("empty ID should return nil")
	}
}

func TestFetchCredentialFromMaster(t *testing.T) {
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/internal/credential" {
			http.NotFound(w, r)
			return
		}
		peerSecret := r.Header.Get("X-Peer-Secret")
		if peerSecret != "test-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := r.URL.Query().Get("id")
		json.NewEncoder(w).Encode(map[string]string{
			"id":           id,
			"access_token": "new-token-123",
			"expired":      "2026-12-31T23:59:59Z",
		})
	}))
	defer master.Close()

	mgr := NewManager(nil, nil, nil)
	mgr.credentialMaster = master.URL
	mgr.peerSecret = "test-secret"

	// Register a local auth entry
	mgr.mu.Lock()
	mgr.auths["auth-1"] = &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "old-token"},
		Status:   StatusActive,
	}
	mgr.mu.Unlock()

	err := mgr.fetchCredentialFromMaster(context.Background(), "auth-1", "claude")
	if err != nil {
		t.Fatalf("fetchCredentialFromMaster failed: %v", err)
	}

	mgr.mu.RLock()
	auth := mgr.auths["auth-1"]
	mgr.mu.RUnlock()

	if at, ok := auth.Metadata["access_token"].(string); !ok || at != "new-token-123" {
		t.Errorf("expected new-token-123, got %v", auth.Metadata["access_token"])
	}
	if exp, ok := auth.Metadata["expired"].(string); !ok || exp != "2026-12-31T23:59:59Z" {
		t.Errorf("expected expired field to be updated, got %v", auth.Metadata["expired"])
	}
}

func TestFetchCredentialFromMaster_NoMaster(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	err := mgr.fetchCredentialFromMaster(context.Background(), "x", "claude")
	if err == nil || err.Error() != "credential-master not configured" {
		t.Errorf("expected 'credential-master not configured', got %v", err)
	}
}

func TestFetchCredentialFromMaster_NoSecret(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.credentialMaster = "http://localhost:9999"
	err := mgr.fetchCredentialFromMaster(context.Background(), "x", "claude")
	if err == nil || err.Error() != "peer secret not configured" {
		t.Errorf("expected 'peer secret not configured', got %v", err)
	}
}

func TestGetAllAuthsForSync(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.mu.Lock()
	mgr.auths["a1"] = &Auth{
		ID: "a1", Provider: "claude",
		Metadata: map[string]any{"access_token": "t1", "refresh_token": "rt1"},
	}
	mgr.auths["a2"] = &Auth{
		ID: "a2", Provider: "claude", Disabled: true,
		Metadata: map[string]any{"access_token": "t2"},
	}
	mgr.mu.Unlock()

	result := mgr.GetAllAuthsForSync()
	if len(result) != 1 {
		t.Fatalf("expected 1 auth (disabled excluded), got %d", len(result))
	}
	if result[0].ID != "a1" {
		t.Errorf("expected a1, got %s", result[0].ID)
	}
	if _, ok := result[0].Metadata["refresh_token"]; ok {
		t.Error("refresh_token should be stripped from sync data")
	}
}

func TestSyncAuthsFromMaster(t *testing.T) {
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/internal/auth-list" {
			http.NotFound(w, r)
			return
		}
		peerSecret := r.Header.Get("X-Peer-Secret")
		if peerSecret != "sync-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"auths": []map[string]any{
				{"id": "sync-1", "provider": "claude", "metadata": map[string]any{"access_token": "st1"}},
				{"id": "sync-2", "provider": "claude", "metadata": map[string]any{"access_token": "st2"}},
			},
		})
	}))
	defer master.Close()

	dir := t.TempDir()
	mgr := NewManager(nil, nil, nil)
	mgr.credentialMaster = master.URL
	mgr.peerSecret = "sync-secret"

	err := mgr.SyncAuthsFromMaster(context.Background(), dir)
	if err != nil {
		t.Fatalf("SyncAuthsFromMaster failed: %v", err)
	}

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	if len(mgr.auths) != 2 {
		t.Errorf("expected 2 auths, got %d", len(mgr.auths))
	}
	if mgr.auths["sync-1"] == nil || mgr.auths["sync-2"] == nil {
		t.Error("expected both sync-1 and sync-2 to be registered")
	}

	// Check files were written
	for _, id := range []string{"sync-1", "sync-2"} {
		path := filepath.Join(dir, id+".json")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", path)
		}
	}
}

func TestSyncAuthsFromMaster_NoMaster(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	err := mgr.SyncAuthsFromMaster(context.Background(), "/tmp")
	if err != nil {
		t.Errorf("expected nil when no master configured, got %v", err)
	}
}
