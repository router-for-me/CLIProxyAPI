package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	clineauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func clineAuthWithDetectedModels(id string, detected []clineauth.ClineModelInfo) *coreauth.Auth {
	auth := clineFileSynthAuthForTest(id)
	auth.Metadata[clineauth.ModelsMetadataKey] = detected
	return auth
}

func waitForModelCount(t *testing.T, clientID string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		n := len(GlobalModelRegistry().GetModelsForClient(clientID))
		if n > 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestClineModelsPreferDetectedCatalog: per-account detected models take
// precedence over the 15-entry static catalog when metadata carries them.
func TestClineModelsPreferDetectedCatalog(t *testing.T) {
	ctx := context.Background()
	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}

	detected := []clineauth.ClineModelInfo{
		{ID: "moonshotai/kimi-k3", Name: "kimi-k3", Description: "K3 for this account"},
		{ID: "anthropic/claude-sonnet-4-6"},
	}
	auth := clineAuthWithDetectedModels("cline-detect-test.json", detected)
	s.applyCoreAuthAddOrUpdate(ctx, auth)
	if got := waitForModelCount(t, auth.ID, 3*time.Second); got != 2 {
		t.Fatalf("advertised %d models, want exactly the 2 detected", got)
	}
	models := GlobalModelRegistry().GetModelsForClient(auth.ID)
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
	}
	if !ids["moonshotai/kimi-k3"] || !ids["anthropic/claude-sonnet-4-6"] {
		t.Fatalf("detected models missing: %+v", models)
	}
	if ids["cline-pass/glm-5.3"] {
		t.Fatal("static fallback must not leak into detected advertisement")
	}
	for _, m := range models {
		if m.ID == "moonshotai/kimi-k3" {
			if m.DisplayName != "kimi-k3" || m.Type != "cline" || m.OwnedBy != "cline" {
				t.Fatalf("detected model info shape wrong: %+v", m)
			}
		}
		// Static-catalog entries keep their static metadata (display name).
		if m.ID == "anthropic/claude-sonnet-4-6" && m.DisplayName != "Claude Sonnet 4.6" {
			t.Fatalf("static metadata lost for known model: %+v", m)
		}
	}

	// The re-registration path (auth file update) must keep preferring detected.
	s.handleAuthUpdates(ctx, []watcher.AuthUpdate{{
		Action: watcher.AuthUpdateActionModify, ID: auth.ID, Auth: auth.Clone(),
	}})
	time.Sleep(100 * time.Millisecond)
	if got := len(GlobalModelRegistry().GetModelsForClient(auth.ID)); got != 2 {
		t.Fatalf("after modify update: %d models advertised, want 2", got)
	}
}

// TestClineModelsStaticFallbackUntilDetected: with no detection marker the
// static registry catalog (15 entries) is advertised.
func TestClineModelsStaticFallbackUntilDetected(t *testing.T) {
	ctx := context.Background()
	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := clineFileSynthAuthForTest("cline-fallback-test.json")
	s.applyCoreAuthAddOrUpdate(ctx, auth)
	if got := waitForModelCount(t, auth.ID, 3*time.Second); got != 15 {
		t.Fatalf("advertised %d models before detection, want 15 static", got)
	}
}

// TestClineLazyModelDetectionOnce covers the first-import lazy path:
// - an imported cline auth WITHOUT stored models triggers exactly one fetch;
// - the fetched catalog is persisted into the auth file metadata;
// - re-registering from the persisted shape advertises only detected models;
// - a follow-up update does not refetch (metadata key marks detection done).
func TestClineLazyModelDetectionOnce(t *testing.T) {
	var fetchCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ai/cline/models" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", got)
		}
		fetchCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"moonshotai/kimi-k3","name":"kimi-k3"}]`))
	}))
	defer upstream.Close()

	// Isolate the process-wide once-map entry for this test credential.
	authID := fmt.Sprintf("cline-lazy-%d.json", time.Now().UnixNano())
	tmpDir := t.TempDir()
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(tmpDir)
	sdkAuth.RegisterTokenStore(store)
	t.Cleanup(func() { sdkAuth.RegisterTokenStore(nil) })

	ctx := context.Background()
	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := clineFileSynthAuthForTest(authID)
	auth.Attributes[coreauth.AttributePath] = filepath.Join(tmpDir, authID)
	auth.Attributes[coreauth.AttributeSource] = filepath.Join(tmpDir, authID)
	delete(auth.Metadata, clineauth.ModelsMetadataKey)
	// The credential's own base URL drives the detection fetch (attributes win).
	auth.Attributes["base_url"] = upstream.URL + "/api/v1"
	auth.Metadata["base_url"] = upstream.URL + "/api/v1"

	s.applyCoreAuthAddOrUpdate(ctx, auth)
	if got := waitForModelCount(t, authID, 3*time.Second); got != 15 {
		t.Fatalf("before lazy detection advertised %d, want 15 static", got)
	}

	s.handleAuthUpdates(ctx, []watcher.AuthUpdate{{
		Action: watcher.AuthUpdateActionAdd, ID: authID, Auth: auth,
	}})

	// Wait for the detached detection goroutine to fetch and persist.
	deadline := time.Now().Add(5 * time.Second)
	var persisted []byte
	for {
		raw, errRead := os.ReadFile(filepath.Join(tmpDir, authID))
		if errRead == nil && len(raw) > 0 {
			var meta map[string]any
			if errJSON := json.Unmarshal(raw, &meta); errJSON == nil {
				if _, present := meta[clineauth.ModelsMetadataKey]; present {
					persisted = raw
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("lazy detection did not persist models marker within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := fetchCount.Load(); got != 1 {
		t.Fatalf("models endpoint fetched %d times, want exactly 1", got)
	}

	var meta map[string]any
	if err := json.Unmarshal(persisted, &meta); err != nil {
		t.Fatalf("persisted auth file not valid JSON: %v", err)
	}
	persistedModels := clineauth.ModelsFromMetadata(meta)
	if len(persistedModels) != 1 || persistedModels[0].ID != "moonshotai/kimi-k3" {
		t.Fatalf("persisted detected models wrong: %+v", persistedModels)
	}

	// Simulate the watcher re-registering from the persisted file shape.
	replayed := auth.Clone()
	replayed.Metadata[clineauth.ModelsMetadataKey] = persistedModels
	s.handleAuthUpdates(ctx, []watcher.AuthUpdate{{
		Action: watcher.AuthUpdateActionModify, ID: authID, Auth: replayed,
	}})
	time.Sleep(200 * time.Millisecond)
	if got := len(GlobalModelRegistry().GetModelsForClient(authID)); got != 1 {
		t.Fatalf("after lazy detection + replay advertised %d models, want 1 (detected only)", got)
	}

	// Marker present: further updates never fetch again.
	s.handleAuthUpdates(ctx, []watcher.AuthUpdate{{
		Action: watcher.AuthUpdateActionModify, ID: authID, Auth: replayed.Clone(),
	}})
	time.Sleep(200 * time.Millisecond)
	if got := fetchCount.Load(); got != 1 {
		t.Fatalf("models endpoint fetched %d times after marker present, want 1", got)
	}
}
