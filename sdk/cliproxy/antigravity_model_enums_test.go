package cliproxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityModelEnumsFromRefresh(t *testing.T) {
	for _, mode := range []string{"memory", "local-kv"} {
		t.Run(mode, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			if mode == "local-kv" {
				if errConfigure := kvstore.ConfigureLocal(true, filepath.Join(t.TempDir(), "state.db")); errConfigure != nil {
					t.Fatal(errConfigure)
				}
			}
			t.Cleanup(func() {
				if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
					t.Error(errShutdown)
				}
			})
			var requests, response atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch response.Load() {
				case 1:
					w.WriteHeader(http.StatusServiceUnavailable)
				case 2:
					_, _ = fmt.Fprint(w, `{"webSearchModelIds":["gemini-2.5-pro"]}`)
				case 3:
					_, _ = fmt.Fprint(w, `{"models":{"gemini-2.5-pro":{}}}`)
				default:
					_, _ = fmt.Fprint(w, `{"models":{"gemini-2.5-pro":{"model":"MODEL_GOOGLE_GEMINI_2_5_PRO"},"gemini-3.1-flash-lite":{"model":"MODEL_PLACEHOLDER_M50"},"gemini-3-flash":{"model":42}}}`)
				}
			}))
			t.Cleanup(srv.Close)
			manager := coreauth.NewManager(nil, nil, nil)
			svc := &Service{cfg: &config.Config{}, coreManager: manager}
			credential := antigravityTestAuth("enum-refresh-"+mode, srv.URL)
			credential.Metadata["refresh_token"] = "refresh-credential"
			auth, errRegister := manager.Register(t.Context(), credential)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
			svc.registerModelsForAuth(t.Context(), auth)
			svc.WaitAntigravityProbes()
			assertEnum := func(snapshot *coreauth.Auth, id, want string) {
				t.Helper()
				got, found := coreauth.LookupAntigravityModelEnum(t.Context(), snapshot, id)
				if got != want || found != (want != "") {
					t.Fatalf("lookup %s = %q, %v; want %q", id, got, found, want)
				}
			}
			const upstreamID, upstreamEnum = "gemini-2.5-pro", "MODEL_GOOGLE_GEMINI_2_5_PRO"
			assertEnum(auth, upstreamID, upstreamEnum)
			assertEnum(auth, "gemini-3.1-flash-lite", "MODEL_PLACEHOLDER_M50")
			assertEnum(auth, "gemini-3-flash", "")
			assertEnum(auth, "tenant/"+upstreamID, "")
			if requests.Load() != 1 {
				t.Fatalf("read-only lookups triggered probes: %d requests", requests.Load())
			}
			rotated := auth.Clone()
			rotated.Metadata["access_token"] = "rotated-access"
			assertEnum(rotated, upstreamID, upstreamEnum)
			replaced := auth.Clone()
			replaced.Attributes["base_url"] = "https://different.example"
			assertEnum(replaced, upstreamID, "")
			other := auth.Clone()
			other.ID += "-another-credential"
			assertEnum(other, upstreamID, "")

			refresh := func(kind int32) {
				response.Store(kind)
				key := svc.antigravityCapabilityKey(auth)
				antigravityCapabilityMu.Lock()
				entry := antigravityCapabilityCache[key]
				entry.expiresAt = time.Time{}
				antigravityCapabilityCache[key] = entry
				clear(antigravityAuthFailureCache)
				antigravityCapabilityMu.Unlock()
				svc.refreshAntigravityModels(t.Context())
				svc.WaitAntigravityProbes()
			}
			key := "antigravity:model-enums:local:v1:" + kvstore.HashKeyPart(auth.ID)
			client, active, _ := kvstore.CurrentKVClient()
			if active {
				if changed, errExpire := client.KVExpire(t.Context(), key, time.Hour); errExpire != nil || !changed {
					t.Fatalf("failed to shorten the test snapshot TTL: %v", errExpire)
				}
			}
			refresh(1)
			assertEnum(auth, upstreamID, upstreamEnum)
			if active {
				if ttl, found, errTTL := client.KVTTL(t.Context(), key); errTTL != nil || !found || ttl <= 24*time.Hour {
					t.Fatalf("failed refresh did not renew the last success: %v, %v, %v", ttl, found, errTTL)
				}
			}
			refresh(2)
			assertEnum(auth, upstreamID, upstreamEnum)
			refresh(3)
			assertEnum(auth, upstreamID, "")
			assertEnum(auth, "gemini-3.1-flash-lite", "")
			if requests.Load() != 4 {
				t.Fatalf("probes = %d, want 4", requests.Load())
			}
			refresh(0)
			authAgain, errAgain := manager.Register(t.Context(), auth.Clone())
			if errAgain != nil {
				t.Fatal(errAgain)
			}
			assertEnum(authAgain, upstreamID, "")
		})
	}
}
