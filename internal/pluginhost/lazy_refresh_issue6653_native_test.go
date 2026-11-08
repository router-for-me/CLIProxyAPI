//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func issue6653NativeHost(t *testing.T) (*Host, pluginapi.Plugin, capabilityRecord) {
	t.Helper()
	library := filepath.Join(t.TempDir(), "qa-native"+testPluginLibraryExtension())
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", library, ".")
	cmd.Dir = filepath.Join("testdata", "issue6653_native")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native fixture build: %v\n%s", err, output)
	}
	host := New()
	client, err := dynamicLibraryLoader{}.Open(pluginFile{ID: "qa-native-6653", Path: library, Version: "1"}, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Shutdown)
	p, err := registerRPCPlugin(context.Background(), host, "qa-native-6653", client, pluginabi.MethodPluginRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.SchemaVersion != 11 || pluginabi.ABIVersion != 1 {
		t.Fatalf("schema/ABI=%d/%d", p.SchemaVersion, pluginabi.ABIVersion)
	}
	record := capabilityRecord{id: "qa-native-6653", path: library, version: "1", plugin: p}
	setHostSnapshotForTest(host, true, record)
	t.Log("built and loaded C-shared fixture: native ABI 1, registration schema 11")
	return host, p, record
}
func issue6653NativeRaw(id, upstream string, lazy bool) []byte {
	d := ojson.NewDoc([]byte(`{}`))
	for key, value := range map[string]any{"id": id, "type": "qa-native-6653", "qa_upstream": upstream, "access_token": "qa-old-access", "refresh_token": "qa-old-refresh", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339Nano)} {
		_ = d.Set(key, value)
	}
	if lazy {
		_ = d.Set("qa_policy", map[string]any{"mode": "lazy", "expiry_margin_seconds": 60})
	}
	return d.Bytes()
}
func issue6653NativeManager(t *testing.T, host *Host, p pluginapi.Plugin, record capabilityRecord, store coreauth.Store) (*coreauth.Manager, *executorAdapter) {
	t.Helper()
	manager := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
	host.SetAuthManager(manager)
	adapter := newExecutorAdapterForRecordForTest(host, record, p.Capabilities.Executor, []translator.Format{translator.FormatOpenAI}, []translator.Format{translator.FormatOpenAI})
	adapter.provider = "qa-native-6653"
	manager.RegisterExecutor(adapter)
	return manager, adapter
}
func issue6653NativeRegister(t *testing.T, host *Host, m *coreauth.Manager, id, url string, lazy bool) *coreauth.Auth {
	t.Helper()
	a, handled, err := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "qa-native-6653", FileName: id + ".json", RawJSON: issue6653NativeRaw(id, url, lazy)})
	if err != nil || !handled || a == nil {
		t.Fatalf("native parse handled=%v auth=%v err=%v", handled, a, err)
	}
	a, err = m.Register(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(id, "qa-native-6653", []*registry.ModelInfo{{ID: "qa-model-6653"}})
	t.Cleanup(func() { reg.UnregisterClient(id) })
	return a
}
func issue6653NativeOperation(m *coreauth.Manager, h *Host, a *coreauth.Auth, operation string) error {
	ctx := context.Background()
	req := executor.Request{Model: "qa-model-6653", Format: translator.FormatOpenAI, Payload: []byte(`{"model":"qa-model-6653","messages":[{"role":"user","content":"qa"}]}`)}
	opts := executor.Options{SourceFormat: translator.FormatOpenAI, ResponseFormat: translator.FormatOpenAI}
	switch operation {
	case "quota":
		_, handled, err := h.FetchQuota(ctx, pluginapi.QuotaFetchRequest{Provider: a.Provider, AuthID: a.ID, Metadata: map[string]any{"access_token": "qa-stale-caller"}})
		if !handled && err == nil {
			return fmt.Errorf("quota not handled")
		}
		return err
	case "quota-index":
		_, _, err := h.FetchQuotaByPlugin(ctx, "qa-native-6653", pluginapi.QuotaFetchRequest{AuthID: a.ID, AuthIndex: a.Index, Metadata: map[string]any{"access_token": "qa-stale-caller"}})
		return err
	case "quota-reset":
		_, _, err := h.ResetQuota(ctx, pluginapi.QuotaResetRequest{Provider: a.Provider, AuthID: a.ID, Metadata: map[string]any{"access_token": "qa-stale-caller"}})
		return err
	case "stream":
		stream, err := m.ExecuteStream(ctx, []string{a.Provider}, req, opts)
		if err != nil {
			return err
		}
		count := 0
		for c := range stream.Chunks {
			if c.Err != nil {
				return c.Err
			}
			count++
		}
		if count != 1 {
			return fmt.Errorf("stream chunks=%d", count)
		}
		return nil
	case "count":
		_, err := m.ExecuteCount(ctx, []string{a.Provider}, req, opts)
		return err
	default:
		if operation == "image" {
			opts.Metadata = map[string]any{executor.RequestPathMetadataKey: "/v1/images/generations"}
		}
		_, err := m.Execute(ctx, []string{a.Provider}, req, opts)
		return err
	}
}

func TestIssue6653NativeLoopbackOperationsAndRestart(t *testing.T) {
	host, p, record := issue6653NativeHost(t)
	restartHost, restartPlugin, restartRecord := issue6653NativeHost(t)
	sdkauth.RegisterPluginAuthParser(restartHost)
	t.Cleanup(func() { sdkauth.RegisterPluginAuthParser(nil) })
	for _, operation := range []string{"buffered", "stream", "image", "count", "quota", "quota-index", "quota-reset"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			id := "qa-native-" + operation
			file := filepath.Join(dir, id+".json")
			var mu sync.Mutex
			refreshes, operations := 0, 0
			var observedPath string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/refresh" {
					refreshes++
					_, _ = w.Write([]byte(`{"access_token":"qa-native-rotated-access","refresh_token":"qa-native-rotated-refresh"}`))
					return
				}
				operations++
				observedPath = r.URL.Path
				if r.Header.Get("Authorization") != "Bearer qa-native-rotated-access" {
					t.Errorf("stale token reached %s", r.URL.Path)
					w.WriteHeader(401)
					return
				}
				saved, err := os.ReadFile(file)
				if err != nil || ojson.GetBytes(saved, "access_token").String() != "qa-native-rotated-access" || ojson.GetBytes(saved, "refresh_token").String() != "qa-native-rotated-refresh" || ojson.GetBytes(saved, "host_refresh_policy.mode").String() != "lazy" {
					t.Errorf("upstream before durable rotated pair/policy: %v", err)
					w.WriteHeader(500)
					return
				}
				switch r.URL.Path {
				case "/sse":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"id\":\"qa\",\"choices\":[]}\n\n"))
				case "/count":
					_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":7}}`))
				default:
					_, _ = w.Write([]byte(`{"id":"qa","choices":[],"data":[]}`))
				}
			}))
			defer upstream.Close()
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			m, _ := issue6653NativeManager(t, host, p, record, store)
			a := issue6653NativeRegister(t, host, m, id, upstream.URL, true)
			if err := issue6653NativeOperation(m, host, a, operation); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			if refreshes != 1 || operations != 1 {
				t.Errorf("refresh=%d operation=%d", refreshes, operations)
			}
			path := observedPath
			mu.Unlock()
			fresh, _ := m.GetByID(a.ID)
			if fresh.Metadata["access_token"] != "qa-native-rotated-access" || fresh.Metadata["refresh_token"] != "qa-native-rotated-refresh" {
				t.Fatal("metadata and saved pair disagree")
			}
			restartStore := sdkauth.NewFileTokenStore()
			restartStore.SetBaseDir(dir)
			restart, _ := issue6653NativeManager(t, restartHost, restartPlugin, restartRecord, restartStore)
			if err := restart.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			reloaded, ok := restart.GetByID(a.ID)
			if !ok || reloaded == nil || !reloaded.UsesLazyRefresh() {
				t.Fatal("fresh host/store/manager did not restore saved auth and policy")
			}
			if err := issue6653NativeOperation(restart, restartHost, reloaded, operation); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if refreshes != 1 || operations != 2 {
				t.Fatalf("restart unnecessarily rotated: refresh=%d operation=%d", refreshes, operations)
			}
			t.Logf("%s: upstream=%s refresh=1 operations=2; rotated pair and lazy policy reload from real FileTokenStore", operation, path)
		})
	}
}

func TestIssue6653NativeDefaultAndRefreshFailure(t *testing.T) {
	host, p, record := issue6653NativeHost(t)
	for _, lazy := range []bool{false, true} {
		t.Run(fmt.Sprintf("lazy=%v", lazy), func(t *testing.T) {
			var mu sync.Mutex
			refreshes, operations := 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/refresh" {
					refreshes++
					w.WriteHeader(503)
					_, _ = io.WriteString(w, "qa-refresh-503")
				} else {
					operations++
					w.WriteHeader(200)
				}
			}))
			defer upstream.Close()
			m, _ := issue6653NativeManager(t, host, p, record, nil)
			a := issue6653NativeRegister(t, host, m, "qa-failure", upstream.URL, lazy)
			if err := issue6653NativeOperation(m, host, a, "buffered"); err == nil {
				t.Fatal("expired/failing native refresh executed")
			}
			mu.Lock()
			defer mu.Unlock()
			want := 0
			if lazy {
				want = 1
			}
			if refreshes != want || operations != 0 {
				t.Fatalf("refresh=%d want=%d inference=%d", refreshes, want, operations)
			}
		})
	}
}

func TestIssue6653NativeConcurrentInferenceAndQuota(t *testing.T) {
	host, p, record := issue6653NativeHost(t)
	var mu sync.Mutex
	refreshes, operations := 0, 0
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/refresh" {
			mu.Lock()
			refreshes++
			if refreshes == 1 {
				close(started)
			}
			mu.Unlock()
			<-release
			_, _ = io.WriteString(w, `{"access_token":"qa-native-rotated-access","refresh_token":"qa-native-rotated-refresh"}`)
			return
		}
		mu.Lock()
		operations++
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer qa-native-rotated-access" {
			t.Error("concurrent operation used stale token")
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/sse" {
			_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\n")
		} else if r.URL.Path == "/count" {
			_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1}}`)
		} else {
			_, _ = io.WriteString(w, `{"choices":[],"data":[]}`)
		}
	}))
	defer upstream.Close()
	m, _ := issue6653NativeManager(t, host, p, record, nil)
	a := issue6653NativeRegister(t, host, m, "qa-native-concurrent", upstream.URL, true)
	names := []string{"buffered", "stream", "image", "count", "quota", "quota-index", "quota-reset"}
	results := make(chan error, len(names))
	start := make(chan struct{})
	for _, name := range names {
		go func() { <-start; results <- issue6653NativeOperation(m, host, a, name) }()
	}
	close(start)
	<-started
	close(release)
	for range names {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if refreshes != 1 || operations != len(names) {
		t.Fatalf("cross-operation refresh=%d calls=%d", refreshes, operations)
	}
	t.Log("barrier-started buffered/SSE/image/count/quota fetch/index/reset shared one native RefreshAuth rotation")
}

func TestIssue6653NativeQuotaBadIndexCannotBypassLazyAuth(t *testing.T) {
	host, p, record := issue6653NativeHost(t)
	var mu sync.Mutex
	refreshes, quotas := 0, 0
	stale := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/refresh" {
			refreshes++
			_, _ = io.WriteString(w, `{"access_token":"qa-native-rotated-access","refresh_token":"qa-native-rotated-refresh"}`)
			return
		}
		quotas++
		if r.Header.Get("Authorization") != "Bearer qa-native-rotated-access" {
			stale = true
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	m, _ := issue6653NativeManager(t, host, p, record, nil)
	a := issue6653NativeRegister(t, host, m, "qa-bad-index", upstream.URL, true)
	_, _, err := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: a.Provider, AuthID: a.ID, AuthIndex: "qa-nonexistent-index", Metadata: map[string]any{"qa_upstream": upstream.URL, "access_token": "qa-old-access"}})
	mu.Lock()
	defer mu.Unlock()
	if stale || quotas > 0 && refreshes != 1 {
		t.Fatalf("bad index bypassed registered lazy AuthID: refresh=%d quota=%d stale=%v err=%v", refreshes, quotas, stale, err)
	}
	if err == nil && (quotas != 1 || refreshes != 1) {
		t.Fatal("neither fail-closed nor fresh AuthID fallback")
	}
}

func TestIssue6653NativeQuotaIdentifiersFetchAndReset(t *testing.T) {
	h, p, r := issue6653NativeHost(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Errorf("conflicting identifiers reached native upstream %s", r.URL.Path)
	}))
	defer upstream.Close()
	m, _ := issue6653NativeManager(t, h, p, r, nil)
	a := issue6653NativeRegister(t, h, m, "qa-lazy-id", upstream.URL, true)
	other := issue6653NativeRegister(t, h, m, "qa-default-id", upstream.URL, false)
	for _, disabled := range []bool{false, true} {
		for _, index := range []string{"qa-unknown-index", other.Index} {
			for _, reset := range []bool{false, true} {
				t.Run(fmt.Sprintf("disabled=%v/index=%s/reset=%v", disabled, index, reset), func(t *testing.T) {
					current, _ := m.GetByID(a.ID)
					current.Disabled = disabled
					if _, err := m.Update(context.Background(), current); err != nil {
						t.Fatal(err)
					}
					var err error
					if reset {
						_, _, err = h.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{Provider: a.Provider, AuthID: a.ID, AuthIndex: index, Metadata: map[string]any{"qa_upstream": upstream.URL, "access_token": "qa-old-access"}})
					} else {
						_, _, err = h.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: a.Provider, AuthID: a.ID, AuthIndex: index, Metadata: map[string]any{"qa_upstream": upstream.URL, "access_token": "qa-old-access"}})
					}
					if err == nil {
						t.Fatal("conflicting lazy ID/index admitted")
					}
					if calls.Load() != 0 {
						t.Fatal("native rotation/quota callback before identifier rejection")
					}
				})
			}
		}
	}
}

func TestIssue6653NativeTerminalEnvelopeAndIdentity(t *testing.T) {
	h, p, r := issue6653NativeHost(t)
	for _, kind := range []string{"invalid_grant/400", "invalid_grant/0", "token_revoked/400", "token_revoked/0", "identity"} {
		t.Run(kind, func(t *testing.T) {
			var refreshes, operations atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/refresh" {
					operations.Add(1)
					t.Errorf("failed renewal dispatched %s", req.URL.Path)
					return
				}
				refreshes.Add(1)
				d := ojson.NewDoc([]byte(`{}`))
				if kind == "identity" {
					_ = d.Set("qa_response_id", "qa-other-B")
					_ = d.Set("access_token", "qa-forbidden-B-access")
					_ = d.Set("refresh_token", "qa-forbidden-B-grant")
				} else {
					parts := strings.Split(kind, "/")
					_ = d.Set("qa_terminal_code", parts[0])
					if parts[1] == "400" {
						_ = d.Set("qa_terminal_status", 400)
					}
				}
				_, _ = w.Write(d.Bytes())
			}))
			defer upstream.Close()
			m, _ := issue6653NativeManager(t, h, p, r, nil)
			a := issue6653NativeRegister(t, h, m, "qa-envelope-A", upstream.URL, true)
			b := issue6653NativeRegister(t, h, m, "qa-other-B", upstream.URL, true)
			registry.GetGlobalRegistry().UnregisterClient(b.ID)
			before := b.Clone()
			for _, op := range []string{"buffered", "quota", "quota-reset"} {
				if err := issue6653NativeOperation(m, h, a, op); err == nil {
					t.Fatalf("%s accepted %s refresh", op, kind)
				}
			}
			after, _ := m.GetByID(b.ID)
			if !reflect.DeepEqual(before, after) || operations.Load() != 0 || refreshes.Load() != 1 {
				t.Fatalf("identity/terminal failure mutated B or dispatched: refresh=%d operation=%d", refreshes.Load(), operations.Load())
			}
			if kind != "identity" {
				latest, _ := m.GetByID(a.ID)
				if !latest.RequiresSignIn || latest.RejectedRefreshToken == "" {
					t.Fatal("loaded native terminal envelope treated as transient")
				}
			}
		})
	}
}
