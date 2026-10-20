package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type siwcBindHook struct {
	NoopHook
	updates int
}

func (h *siwcBindHook) OnAuthUpdated(context.Context, *Auth) { h.updates++ }

func setupSIWCBinding(t *testing.T, name string) (*Manager, *Auth, string, *siwcBindHook) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	metadata := map[string]any{"type": "codex", "access_token": "outer", "refresh_token": "outer-refresh", "account_id": "account", "notes": "old", "disabled": false, "siwc": map[string]any{"access_token": "old-siwc", "refresh_token": "old-siwc-refresh", "client_id": "client"}}
	data, errMarshal := json.Marshal(metadata)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	hook := &siwcBindHook{}
	m := NewManager(nil, nil, hook)
	m.runtimeConfig.Store(&config.Config{AuthDir: dir})
	a, err := m.Register(context.Background(), &Auth{ID: "binding", Provider: "codex", FileName: name, Attributes: map[string]string{"path": path}, Metadata: metadata, Status: StatusActive, ProxyURL: "http://runtime-proxy", Prefix: "runtime-prefix", Success: 7})
	if err != nil {
		t.Fatal(err)
	}
	return m, a, path, hook
}
func bindingTokens(access string) *codex.CodexSIWCTokenData {
	return &codex.CodexSIWCTokenData{AccessToken: access, RefreshToken: access + "-refresh", ClientID: "client"}
}
func readBindingDisk(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBindCodexSIWCPreservesLatestDiskAndRebinds(t *testing.T) {
	m, base, path, hook := setupSIWCBinding(t, "auth.json")
	latest := readBindingDisk(t, path)
	latest["access_token"] = "latest-outer"
	latest["refresh_token"] = "latest-refresh"
	latest["notes"] = "latest notes"
	latest["disabled"] = true
	latest["unknown"] = json.RawMessage(`9007199254740993`)
	if err := codex.WriteJSONFileAtomic(path, latest); err != nil {
		t.Fatal(err)
	}
	for _, access := range []string{"bound", "rebound"} {
		got, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, "account", bindingTokens(access))
		if err != nil {
			t.Fatal(err)
		}
		disk := readBindingDisk(t, path)
		if disk["access_token"] != "latest-outer" || disk["refresh_token"] != "latest-refresh" || disk["notes"] != "latest notes" || disk["disabled"] != true || disk["use_siwc"] != true {
			t.Fatal("binding overwrote unrelated disk fields")
		}
		if disk["siwc"].(map[string]any)["access_token"] != access {
			t.Fatal("binding did not replace SIWC")
		}
		if got.RegistrationEpoch != base.RegistrationEpoch || got.Success != base.Success || got.ProxyURL != base.ProxyURL || got.Prefix != base.Prefix || !got.Disabled || got.Status != StatusDisabled {
			t.Fatal("binding lost runtime settings or disk disabled state")
		}
		if got.CredentialVersion <= base.CredentialVersion || got.Generation <= base.Generation {
			t.Fatal("binding did not advance versions")
		}
		raw, _ := os.ReadFile(path)
		if !strings.Contains(string(raw), "9007199254740993") {
			t.Fatal("binding rounded unrelated JSON number")
		}
	}
	if hook.updates != 2 {
		t.Fatalf("updates = %d", hook.updates)
	}
}

func TestBindCodexSIWCFailureDoesNotPublish(t *testing.T) {
	for _, failure := range []string{"missing", "invalid", "type", "outer", "account", "outside", "write"} {
		t.Run(failure, func(t *testing.T) {
			name := "auth.json"
			// The credential and lock names fit NAME_MAX; atomic-write temporary
			// names do not. This fails writes deterministically even when run as root.
			if failure == "write" {
				name = strings.Repeat("a", 244) + ".json"
			}
			m, base, path, hook := setupSIWCBinding(t, name)
			switch failure {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "invalid":
				if err := os.WriteFile(path, []byte("{invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "type", "outer", "account":
				disk := readBindingDisk(t, path)
				if failure == "type" {
					disk["type"] = "claude"
				}
				if failure == "outer" {
					delete(disk, "refresh_token")
				}
				if failure == "account" {
					disk["account_id"] = "other"
				}
				if err := codex.WriteJSONFileAtomic(path, disk); err != nil {
					t.Fatal(err)
				}
			case "outside":
				m.runtimeConfig.Store(&config.Config{AuthDir: t.TempDir()})
			}
			_, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, "account", bindingTokens("bound"))
			if err == nil {
				t.Fatal("binding unexpectedly succeeded")
			}
			got, _ := m.GetByID(base.ID)
			if !reflect.DeepEqual(got, base) || hook.updates != 0 {
				t.Fatal("failed binding published state")
			}
		})
	}
}

func TestBindCodexSIWCRejectsDeletedAndReimportedEpoch(t *testing.T) {
	m, base, path, _ := setupSIWCBinding(t, "auth.json")
	m.Remove(context.Background(), base.ID)
	if _, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, "account", bindingTokens("bound")); err == nil {
		t.Fatal("deleted registration accepted")
	}
	imported, err := m.Register(context.Background(), base.Clone())
	if err != nil {
		t.Fatal(err)
	}
	for _, epoch := range []uint64{0, base.RegistrationEpoch, imported.RegistrationEpoch + 1} {
		if _, err = m.BindCodexSIWC(context.Background(), base.ID, epoch, "account", bindingTokens("bound")); err == nil {
			t.Fatal("incorrect epoch accepted")
		}
	}
	if readBindingDisk(t, path)["use_siwc"] == true {
		t.Fatal("stale binding wrote disk")
	}
}

func TestBindCodexSIWCFencesInflightRefreshAndWatcher(t *testing.T) {
	m, base, path, _ := setupSIWCBinding(t, "auth.json")
	m.SetStore(&bindingFileStore{path: path})
	oldBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	watcher := base.Clone()
	watcher.Generation, watcher.RegistrationEpoch = 0, 0
	watcher.BindFileSnapshot(path, oldBytes)
	refreshed := base.Clone()
	refreshed.Metadata["access_token"] = "refreshed-outer"
	refreshed.Metadata["siwc"] = bindingTokens("stale-refreshed-siwc").ToMap()
	refreshed.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, refreshed.Metadata, true, true)
	bound, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, "account", bindingTokens("bound"))
	if err != nil {
		t.Fatal(err)
	}
	// An earlier refresh finishes after explicit binding. Its independent outer
	// partition may commit, but it must not restore the previous SIWC identity.
	got, err := m.UpdateRefreshedAuth(context.Background(), base, refreshed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["siwc"].(map[string]any)["access_token"] != "bound" || got.Metadata["use_siwc"] != true {
		t.Fatal("refresh overwrote binding")
	}
	disk := readBindingDisk(t, path)
	if disk["access_token"] != "refreshed-outer" || disk["siwc"].(map[string]any)["access_token"] != "bound" {
		t.Fatal("refresh failed to preserve bound partition on disk")
	}
	got, err = m.Update(context.Background(), watcher)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["siwc"].(map[string]any)["access_token"] != "bound" || got.Generation < bound.Generation {
		t.Fatal("stale watcher overwrote binding")
	}
}

// bindingFileStore exercises the production scoped refresh persistence path.
type bindingFileStore struct{ path string }

func (s *bindingFileStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (s *bindingFileStore) Delete(context.Context, string) error  { return os.Remove(s.path) }
func (s *bindingFileStore) Save(ctx context.Context, a *Auth) (string, error) {
	_, err := PersistCodexAuthFile(ctx, s.path, a)
	return s.path, err
}

func TestBindCodexSIWCConcurrentFileRefresh(t *testing.T) {
	m, base, path, _ := setupSIWCBinding(t, "auth.json")
	locked := make(chan struct{})
	finishRefresh := make(chan struct{})
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- codex.WithFileLock(path, func() error {
			close(locked)
			<-finishRefresh
			disk := map[string]any{"type": "codex", "account_id": "account", "access_token": "concurrent-outer", "refresh_token": "concurrent-refresh", "notes": "concurrent notes", "disabled": true}
			return codex.WriteJSONFileAtomic(path, disk)
		})
	}()
	<-locked
	bindDone := make(chan error, 1)
	go func() {
		_, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, "account", bindingTokens("bound"))
		bindDone <- err
	}()
	close(finishRefresh)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	if err := <-bindDone; err != nil {
		t.Fatal(err)
	}
	got, _ := m.GetByID(base.ID)
	if got.Metadata["access_token"] != "concurrent-outer" || got.Metadata["notes"] != "concurrent notes" || !got.Disabled {
		t.Fatal("binding did not re-read locked disk")
	}
	disk := readBindingDisk(t, path)
	if disk["siwc"].(map[string]any)["access_token"] != "bound" || disk["access_token"] != "concurrent-outer" {
		t.Fatal("binding lost concurrent refresh")
	}
}

func TestBindCodexSIWCRejectsNonFileSources(t *testing.T) {
	for _, attrs := range []map[string]string{
		{"source": "config:codex[test]"}, {"source": "config"}, {"runtime_only": "true"},
		{"source_backend": "memory"}, {"source_backend": "postgres"}, {"source_backend": "git"},
		{"source_backend": "objectstore"}, {"auth_kind": "apikey"},
	} {
		t.Run(fmt.Sprint(attrs), func(t *testing.T) {
			m, base, path, hook := setupSIWCBinding(t, "auth.json")
			m.mu.Lock()
			for k, v := range attrs {
				m.auths[base.ID].Attributes[k] = v
			}
			m.mu.Unlock()
			before, _ := os.ReadFile(path)
			if _, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, "account", bindingTokens("bound")); err == nil {
				t.Fatal("non-file OAuth target accepted")
			}
			after, _ := os.ReadFile(path)
			if !reflect.DeepEqual(before, after) || hook.updates != 0 {
				t.Fatal("rejection changed state")
			}
		})
	}
}

func TestBindCodexSIWCTildeAndAttributeOverride(t *testing.T) {
	m, base, path, _ := setupSIWCBinding(t, "auth.json")
	t.Setenv("HOME", filepath.Dir(path))
	m.runtimeConfig.Store(&config.Config{AuthDir: "~"})
	m.mu.Lock()
	m.auths[base.ID].Attributes["use_siwc"] = "false"
	m.mu.Unlock()
	got, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, "account", bindingTokens("bound"))
	if err != nil {
		t.Fatal(err)
	}
	if !IsCodexUseSIWC(got) {
		t.Fatal("stale attribute masks binding")
	}
	if _, ok := got.Attributes["use_siwc"]; ok {
		t.Fatal("binding retained permanent override")
	}
	got.Metadata["use_siwc"] = false
	if IsCodexUseSIWC(got) {
		t.Fatal("file can no longer disable SIWC")
	}
}

func TestBindCodexSIWCRecoversUnauthorizedPreservingQuotaAndDisabled(t *testing.T) {
	for _, mode := range []string{"enabled", "quota", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			m, base, path, _ := setupSIWCBinding(t, "auth.json")
			future := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
			quota := QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: future}
			unauthorized := &Error{HTTPStatus: 401, Code: "unauthorized", Message: "401 unauthorized"}
			m.mu.Lock()
			a := m.auths[base.ID]
			a.RejectedAccessToken = "bound"
			a.Status, a.Unavailable, a.LastError = StatusError, true, unauthorized
			a.StatusMessage = "unauthorized"
			a.ModelStates = map[string]*ModelState{
				"unauthorized": {Status: StatusError, Unavailable: true, LastError: unauthorized, NextRetryAfter: future},
				"quota":        {Status: StatusError, Unavailable: true, Quota: quota, NextRetryAfter: future},
			}
			if mode == "quota" {
				a.Quota, a.NextRetryAfter = quota, future
			}
			if mode == "disabled" {
				a.Disabled, a.Status = true, StatusDisabled
			}
			before := a.Clone()
			m.mu.Unlock()
			if mode == "disabled" {
				disk := readBindingDisk(t, path)
				disk["disabled"] = true
				if err := codex.WriteJSONFileAtomic(path, disk); err != nil {
					t.Fatal(err)
				}
			}
			got, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, "account", bindingTokens("bound"))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "disabled" {
				if !got.Disabled || got.Status != StatusDisabled || !reflect.DeepEqual(got.ModelStates, before.ModelStates) {
					t.Fatal("disabled state changed")
				}
				return
			}
			if got.RejectedAccessToken != "" || got.LastError != nil || got.ModelStates["unauthorized"].Unavailable {
				t.Fatal("stale unauthorized state survived")
			}
			if !reflect.DeepEqual(got.ModelStates["quota"], before.ModelStates["quota"]) {
				t.Fatal("model quota changed")
			}
			if mode == "quota" && (!reflect.DeepEqual(got.Quota, before.Quota) || got.NextRetryAfter != future || !got.Unavailable) {
				t.Fatal("auth quota changed")
			}
		})
	}
}

func TestBindCodexSIWCRelativeAuthDir(t *testing.T) {
	for _, tc := range []struct {
		name       string
		attributes func(rel string) map[string]string
	}{
		{
			name: "cwd_relative_path",
			attributes: func(rel string) map[string]string {
				return map[string]string{"path": rel, "source": rel}
			},
		},
		{
			name: "cwd_relative_source_only",
			attributes: func(rel string) map[string]string {
				return map[string]string{"source": rel, AttributeSourceBackend: AuthSourceFile}
			},
		},
		{
			name: "filename_fallback",
			attributes: func(string) map[string]string {
				return map[string]string{AttributeSourceBackend: AuthSourceFile}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if errMkdir := os.Mkdir("auths", 0o700); errMkdir != nil {
				t.Fatal(errMkdir)
			}
			rel := filepath.Join("auths", "auth.json")
			metadata := map[string]any{"type": "codex", "access_token": "outer", "refresh_token": "outer-refresh", "account_id": "account"}
			data, errMarshal := json.Marshal(metadata)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if errWrite := os.WriteFile(rel, data, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			m := NewManager(nil, nil, nil)
			m.runtimeConfig.Store(&config.Config{AuthDir: "./auths"})
			a, errRegister := m.Register(context.Background(), &Auth{
				ID:         "auth.json",
				Provider:   "codex",
				FileName:   "auth.json",
				Attributes: tc.attributes(rel),
				Metadata:   metadata,
				Status:     StatusActive,
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			got, errBind := m.BindCodexSIWC(context.Background(), a.ID, a.RegistrationEpoch, "account", bindingTokens("bound"))
			if errBind != nil {
				t.Fatalf("bind failed: %v", errBind)
			}
			if !IsCodexUseSIWC(got) {
				t.Fatal("binding did not enable SIWC")
			}
			disk := readBindingDisk(t, rel)
			if disk["use_siwc"] != true || disk["siwc"].(map[string]any)["access_token"] != "bound" {
				t.Fatal("binding did not persist to relative auth-dir file")
			}
		})
	}
	t.Run("cwd_relative_outside_rejected", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if errMkdir := os.Mkdir("auths", 0o700); errMkdir != nil {
			t.Fatal(errMkdir)
		}
		metadata := map[string]any{"type": "codex", "access_token": "outer", "refresh_token": "outer-refresh", "account_id": "account"}
		data, errMarshal := json.Marshal(metadata)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if errWrite := os.WriteFile("outside.json", data, 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		m := NewManager(nil, nil, nil)
		m.runtimeConfig.Store(&config.Config{AuthDir: "./auths"})
		a, errRegister := m.Register(context.Background(), &Auth{
			ID:         "outside.json",
			Provider:   "codex",
			FileName:   "outside.json",
			Attributes: map[string]string{"path": "outside.json", "source": "outside.json"},
			Metadata:   metadata,
			Status:     StatusActive,
		})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		if _, errBind := m.BindCodexSIWC(context.Background(), a.ID, a.RegistrationEpoch, "account", bindingTokens("bound")); errBind == nil {
			t.Fatal("relative path outside auth-dir unexpectedly succeeded")
		}
	})
}

func TestBindCodexSIWCRejectsReplacedAccount(t *testing.T) {
	m, base, path, hook := setupSIWCBinding(t, "auth.json")
	replaced := base.Clone()
	replaced.RegistrationEpoch = 0
	replaced.Metadata["account_id"] = "account-b"
	if err := codex.WriteJSONFileAtomic(path, replaced.Metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(context.Background(), replaced); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetByID(base.ID); got.RegistrationEpoch != base.RegistrationEpoch {
		t.Fatalf("epoch changed: %d", got.RegistrationEpoch)
	}
	if _, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, "account", bindingTokens("bound")); err == nil {
		t.Fatal("binding accepted a replaced account")
	}
	got, _ := m.GetByID(base.ID)
	if got.Metadata["account_id"] != "account-b" || got.Metadata["use_siwc"] == true || hook.updates != 1 {
		t.Fatalf("failed binding mutated replaced account: %#v, updates=%d", got.Metadata, hook.updates)
	}
	disk := readBindingDisk(t, path)
	if disk["account_id"] != "account-b" || disk["use_siwc"] == true || disk["siwc"].(map[string]any)["access_token"] != "old-siwc" {
		t.Fatalf("failed binding mutated replaced account on disk: %#v", disk)
	}
}
