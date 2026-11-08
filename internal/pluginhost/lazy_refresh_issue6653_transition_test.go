package pluginhost

import (
	"bytes"
	"context"
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
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestIssue6653TransitionRuntimeOnlyPendingActualStore(t *testing.T) {
	var exchanges atomic.Int32
	p := fakeAuthProvider{identifier: "plugin-provider", parseAuth: issue6653FinalParser, refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
		exchanges.Add(1)
		return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{RefreshPolicy: &pluginapi.AuthRefreshPolicy{Mode: "scheduled"}, Attributes: map[string]string{"runtime_only": "true"}, Metadata: map[string]any{"accessToken": "qa-durable-rotated-access", "refreshToken": "qa-durable-rotated-grant", "expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}}}, nil
	}}
	h, m := issue6653ReviewHost(t, p)
	dir := t.TempDir()
	physical := sdkauth.NewFileTokenStore()
	physical.SetBaseDir(dir)
	store := &issue6653CountedFileStore{Store: physical}
	a := issue6653FinalPhysicalAuth(t, h, m, store, "qa-runtime-transition", dir)
	before, err := os.ReadFile(filepath.Join(dir, a.FileName))
	if err != nil {
		t.Fatal(err)
	}
	var dispatches atomic.Int32
	nativeExecutor := &fakeExecutor{identifier: "plugin-provider", execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
		dispatches.Add(1)
		raw, err := os.ReadFile(filepath.Join(dir, a.FileName))
		if err != nil {
			t.Fatal(err)
		}
		if ojson.GetBytes(raw, "refreshToken").String() != "qa-durable-rotated-grant" {
			t.Fatal("dispatch preceded actual rotated save")
		}
		return pluginapi.ExecutorResponse{Payload: []byte(`{}`)}, nil
	}}
	record := capabilityRecord{id: "qa-review", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: p, Executor: nativeExecutor}}}
	m.RegisterExecutor(newExecutorAdapterForRecordForTest(h, record, nativeExecutor, []translator.Format{translator.FormatOpenAI}, []translator.Format{translator.FormatOpenAI}))
	registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: "qa-model-6653"}})
	defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
	execute := func() error {
		_, err := m.Execute(context.Background(), []string{a.Provider}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
		return err
	}
	initialSaves := store.saves.Load()
	for i := 0; i < 2; i++ {
		if err := execute(); err == nil {
			t.Fatal("unsupported runtime-only rotation admitted before durable save")
		}
		disk, err := os.ReadFile(filepath.Join(dir, a.FileName))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, disk) || store.saves.Load() != initialSaves || exchanges.Load() != 1 || dispatches.Load() != 0 {
			t.Fatal("pending retry changed file, skipped required save, or reused consumed grant")
		}
	}
	current, _ := m.GetByID(a.ID)
	current.Attributes["runtime_only"] = "false"
	if _, err := m.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	fresh, err := m.EnsureAuthFresh(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Metadata["refreshToken"] != "qa-durable-rotated-grant" || exchanges.Load() != 1 {
		t.Fatal("pending rotated pair lost or re-exchanged")
	}
	if err := execute(); err != nil {
		t.Fatal(err)
	}
	if dispatches.Load() != 1 {
		t.Fatal("durably saved credential did not dispatch")
	}
	disk, err := os.ReadFile(filepath.Join(dir, a.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if ojson.GetBytes(disk, "refreshToken").String() != "qa-durable-rotated-grant" || ojson.GetBytes(disk, "refresh_token").Exists() || ojson.GetBytes(disk, "host_refresh_policy.mode").String() != "scheduled" {
		t.Fatalf("pair not durably saved: %s", disk)
	}
	sdkauth.RegisterPluginAuthParser(h)
	defer sdkauth.RegisterPluginAuthParser(nil)
	restartStore := sdkauth.NewFileTokenStore()
	restartStore.SetBaseDir(dir)
	restart := coreauth.NewManager(restartStore, &coreauth.FillFirstSelector{}, nil)
	if err := restart.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, ok := restart.GetByID(a.ID)
	if !ok || loaded.Metadata["refreshToken"] != "qa-durable-rotated-grant" || loaded.RefreshPolicy.Mode != "scheduled" {
		t.Fatal("actual-store reload restored consumed grant")
	}
}

func TestIssue6653TransitionBindingActualFileUnchanged(t *testing.T) {
	for _, binding := range []string{"provider", "executor"} {
		t.Run(binding, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			closeRelease := sync.OnceFunc(func() { close(release) })
			defer closeRelease()
			p := fakeAuthProvider{identifier: "plugin-provider", parseAuth: issue6653FinalParser, refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
				close(entered)
				<-release
				return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Metadata: map[string]any{"accessToken": "qa-stale-A-access", "refreshToken": "qa-stale-A-grant", "expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}}}, nil
			}}
			h, m := issue6653ReviewHost(t, p)
			dir := t.TempDir()
			physical := sdkauth.NewFileTokenStore()
			physical.SetBaseDir(dir)
			store := &issue6653CountedFileStore{Store: physical}
			a := issue6653FinalPhysicalAuth(t, h, m, store, "qa-binding-file", dir)
			done := make(chan error, 1)
			go func() { _, err := m.EnsureAuthFresh(context.Background(), a); done <- err }()
			<-entered
			c, _ := m.GetByID(a.ID)
			if binding == "provider" {
				c.Provider = "qa-provider-b"
				c.Storage = &pluginTokenStorage{provider: c.Provider, rawJSON: c.Storage.(*pluginTokenStorage).RawJSON(), meta: cloneAnyMap(c.Metadata)}
			} else {
				c.Attributes["compat_name"] = "qa-executor-B"
				c.Attributes["provider_key"] = "qa-executor-B"
			}
			c.Metadata["operator_binding_marker"] = "qa-new-B"
			c, err := m.Update(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			disk, err := os.ReadFile(filepath.Join(dir, a.FileName))
			if err != nil {
				t.Fatal(err)
			}
			if binding == "provider" && ojson.GetBytes(disk, "type").String() != c.Provider {
				t.Fatal("operator B file setup failed")
			}
			if ojson.GetBytes(disk, "operator_binding_marker").String() != "qa-new-B" {
				t.Fatal("operator binding file marker missing")
			}
			saves := store.saves.Load()
			closeRelease()
			if err := <-done; err == nil {
				t.Fatal("stale A operation admitted after live binding change")
			}
			after, err := os.ReadFile(filepath.Join(dir, a.FileName))
			if err != nil {
				t.Fatal(err)
			}
			current, _ := m.GetByID(a.ID)
			if !bytes.Equal(disk, after) || store.saves.Load() != saves || current.Provider != c.Provider || current.Attributes["provider_key"] != c.Attributes["provider_key"] || current.Metadata["refresh_token"] != "qa-old-snake-grant" {
				t.Fatal("stale A response mutated live binding or overwrote actual file")
			}
		})
	}
}
