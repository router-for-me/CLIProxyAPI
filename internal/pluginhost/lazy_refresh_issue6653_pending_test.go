//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func issue6653PendingOperation(m *coreauth.Manager, h *Host, a *coreauth.Auth, op, url string) error {
	if op != "direct" {
		return issue6653NativeOperation(m, h, a, op)
	}
	req, err := http.NewRequest(http.MethodGet, url+"/direct", nil)
	if err != nil {
		return err
	}
	resp, err := m.HttpRequest(context.Background(), a, req)
	if resp != nil {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return err
}

func TestIssue6653PendingScheduledSnapshotNativeActualStore(t *testing.T) {
	h, p, record := issue6653NativeHost(t)
	for _, during := range []bool{false, true} {
		for _, op := range []string{"buffered", "stream", "direct", "quota", "quota-reset"} {
			t.Run(fmt.Sprintf("during=%v/%s", during, op), func(t *testing.T) {
				dir := t.TempDir()
				file := filepath.Join(dir, "qa-pending.json")
				var exchanges, dispatches atomic.Int32
				entered, release := make(chan struct{}), make(chan struct{})
				closeRelease := sync.OnceFunc(func() { close(release) })
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/refresh" {
						if exchanges.Add(1) != 1 {
							t.Error("consumed grant exchanged again")
							return
						}
						close(entered)
						if during {
							<-release
						}
						_, _ = io.WriteString(w, `{"access_token":"qa-native-rotated-access","refresh_token":"qa-native-rotated-refresh"}`)
						return
					}
					dispatches.Add(1)
					disk, err := os.ReadFile(file)
					if err != nil || ojson.GetBytes(disk, "refresh_token").String() != "qa-native-rotated-refresh" || r.Header.Get("Authorization") != "Bearer qa-native-rotated-access" {
						t.Error("dispatch preceded durable rotated pair or used stale token")
						w.WriteHeader(500)
						return
					}
					if r.URL.Path == "/sse" {
						_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\n")
					} else {
						_, _ = io.WriteString(w, `{"choices":[]}`)
					}
				}))
				defer upstream.Close()
				defer closeRelease()
				physical := sdkauth.NewFileTokenStore()
				physical.SetBaseDir(dir)
				store := &issue6653CountedFileStore{Store: physical}
				m, _ := issue6653NativeManager(t, h, p, record, store)
				a := issue6653NativeRegister(t, h, m, "qa-pending", upstream.URL, true)
				a.Metadata["expires_at"] = time.Now().Add(30 * time.Second).Format(time.RFC3339Nano)
				a, err := m.Update(context.Background(), a)
				if err != nil {
					t.Fatal(err)
				}
				switchPolicy := func() {
					current, _ := m.GetByID(a.ID)
					current.RefreshPolicy = &coreauth.RefreshPolicy{Mode: "scheduled"}
					if _, err := m.Update(context.Background(), current); err != nil {
						t.Fatal(err)
					}
				}
				if during {
					done := make(chan error, 1)
					go func() { _, err := m.EnsureAuthFresh(context.Background(), a); done <- err }()
					<-entered
					switchPolicy()
					store.fail.Store(true)
					closeRelease()
					if err := <-done; err == nil {
						t.Fatal("initial failed save admitted")
					}
				} else {
					store.fail.Store(true)
					if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
						t.Fatal("initial failed save admitted")
					}
					store.fail.Store(false)
					switchPolicy()
					store.fail.Store(true)
				}
				current, _ := m.GetByID(a.ID)
				if current.UsesLazyRefresh() {
					t.Fatal("fresh scheduled snapshot setup failed")
				}
				for attempt := 0; attempt < 2; attempt++ {
					current, _ = m.GetByID(a.ID)
					if err := issue6653PendingOperation(m, h, current, op, upstream.URL); err == nil {
						t.Fatal("fresh scheduled snapshot bypassed pending save")
					}
					disk, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					if ojson.GetBytes(disk, "refresh_token").String() != "qa-old-refresh" || dispatches.Load() != 0 || exchanges.Load() != 1 {
						t.Fatal("failed pending save dispatched, changed disk, or reused grant")
					}
				}
				store.fail.Store(false)
				current, _ = m.GetByID(a.ID)
				if err := issue6653PendingOperation(m, h, current, op, upstream.URL); err != nil {
					t.Fatal(err)
				}
				if exchanges.Load() != 1 || dispatches.Load() != 1 {
					t.Fatal("pending recovery exchange/dispatch counts wrong")
				}
				sdkauth.RegisterPluginAuthParser(h)
				defer sdkauth.RegisterPluginAuthParser(nil)
				reloadStore := sdkauth.NewFileTokenStore()
				reloadStore.SetBaseDir(dir)
				reload := coreauth.NewManager(reloadStore, &coreauth.FillFirstSelector{}, nil)
				if err := reload.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
				loaded, ok := reload.GetByID(a.ID)
				if !ok || loaded.RefreshPolicy.Mode != "scheduled" || loaded.Metadata["refresh_token"] != "qa-native-rotated-refresh" {
					t.Fatal("fresh actual-store reload lost durable rotated scheduled pair")
				}
			})
		}
	}
}

func TestIssue6653PendingScheduledQuotaJointLookupAndLegacyFields(t *testing.T) {
	h, p, record := issue6653NativeHost(t)
	var dispatches, callerFields atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/refresh" {
			_, _ = io.WriteString(w, `{"access_token":"qa-native-rotated-access","refresh_token":"qa-native-rotated-refresh"}`)
			return
		}
		dispatches.Add(1)
		if r.Header.Get("Authorization") == "Bearer qa-caller-scheduled" {
			callerFields.Add(1)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	store := &issue6653CountedFileStore{Store: sdkauth.NewFileTokenStore()}
	store.Store.(*sdkauth.FileTokenStore).SetBaseDir(t.TempDir())
	m, _ := issue6653NativeManager(t, h, p, record, store)
	a := issue6653NativeRegister(t, h, m, "qa-pending-quota", upstream.URL, true)
	b := issue6653NativeRegister(t, h, m, "qa-other-quota", upstream.URL, false)
	registry.GetGlobalRegistry().UnregisterClient(b.ID)
	a.Metadata["expires_at"] = time.Now().Add(30 * time.Second).Format(time.RFC3339Nano)
	a, err := m.Update(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	store.fail.Store(true)
	if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
		t.Fatal("pending setup accepted")
	}
	store.fail.Store(false)
	c, _ := m.GetByID(a.ID)
	c.RefreshPolicy = &coreauth.RefreshPolicy{Mode: "scheduled"}
	if _, err := m.Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	store.fail.Store(true)
	for _, pair := range [][2]string{{a.ID, "unknown-index"}, {a.ID, b.Index}, {"unknown-id", a.Index}, {b.ID, a.Index}} {
		saves := store.saves.Load()
		_, _, err := h.FetchQuotaByPlugin(context.Background(), "qa-native-6653", pluginapi.QuotaFetchRequest{AuthID: pair[0], AuthIndex: pair[1]})
		if err == nil {
			t.Fatal("mismatched scheduled-pending quota ID/index accepted")
		}
		_, _, err = h.ResetQuotaByPlugin(context.Background(), "qa-native-6653", pluginapi.QuotaResetRequest{AuthID: pair[0], AuthIndex: pair[1]})
		if err == nil || store.saves.Load() != saves || dispatches.Load() != 0 {
			t.Fatal("mismatched quota pair retried save or dispatched")
		}
	}
	// Ordinary scheduled records preserve caller metadata rather than substituting registered fields.
	fresh, err := h.freshQuotaAuth(context.Background(), b.ID, b.Index)
	if err != nil || fresh != nil {
		t.Fatal("ordinary scheduled quota unexpectedly replaces caller fields")
	}
	store.fail.Store(false)
	if err := issue6653NativeOperation(m, h, c, "quota-index"); err != nil {
		t.Fatal(err)
	}
	if dispatches.Load() != 1 {
		t.Fatal("matching scheduled pending quota failed recovery")
	}
	_, handled, err := h.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: b.Provider, AuthID: b.ID, Metadata: map[string]any{"qa_upstream": upstream.URL, "access_token": "qa-caller-scheduled"}})
	if err != nil || !handled || dispatches.Load() != 2 || callerFields.Load() != 1 {
		t.Fatalf("ordinary scheduled quota overrode caller metadata or refreshed: handled=%v err=%v", handled, err)
	}
}

func TestIssue6653PendingPolicySaveAndRefreshStorageRace(t *testing.T) {
	p := fakeAuthProvider{identifier: "plugin-provider", parseAuth: issue6653FinalParser, refreshAuth: func(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
		return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{StorageJSON: req.StorageJSON, Metadata: req.Metadata}}, nil
	}}
	h, m := issue6653ReviewHost(t, p)
	dir := t.TempDir()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	a := issue6653FinalPhysicalAuth(t, h, m, store, "qa-policy-save-race", dir)
	a.RefreshPolicy = &coreauth.RefreshPolicy{Mode: "scheduled"}
	a.Metadata["expires_at"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	var err error
	a, err = m.Update(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		for i := 0; i < 50; i++ {
			if _, err := m.ForceRefreshAuth(context.Background(), a.ID); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	close(start)
	for i := 0; i < 50; i++ {
		c, _ := m.GetByID(a.ID)
		c.RefreshPolicy = &coreauth.RefreshPolicy{Mode: "scheduled", ExpiryMarginSeconds: int64(i % 2)}
		if _, err := m.Update(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
