package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// A gateway token refresh rewrites the auth file with new tokens; the watcher then reads that file back as a snapshot. A snapshot
// that replaces the credential starts clean, so the tokens the gateway itself just wrote must not count as a replacement.

// tokenRefreshExecutor answers every refresh with the next token pair: T2/R2, T3/R3, ...
type tokenRefreshExecutor struct {
	mu sync.Mutex
	n  int
}

func (e *tokenRefreshExecutor) Identifier() string { return "claude" }

func (e *tokenRefreshExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.mu.Lock()
	e.n++
	n := e.n
	e.mu.Unlock()
	updated := auth.Clone()
	updated.Metadata["access_token"] = fmt.Sprintf("T%d", n+1)
	updated.Metadata["refresh_token"] = fmt.Sprintf("R%d", n+1)
	updated.Metadata["expired"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	return updated, nil
}

func (e *tokenRefreshExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *tokenRefreshExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *tokenRefreshExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *tokenRefreshExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// newRefreshSnapshotManager returns a manager on a real file store holding one Claude auth file with tokens T1/R1 and the executor above.
func newRefreshSnapshotManager(t *testing.T) (*coreauth.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	manager.RegisterExecutor(&tokenRefreshExecutor{})
	path := filepath.Join(dir, trafficAuthID)
	if errWrite := os.WriteFile(path, []byte(`{"type":"claude","email":"a@b.c","access_token":"T1","refresh_token":"R1","prefix":"pfx-1"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, errRegister := manager.Register(context.Background(), readTrafficSnapshot(t, path, "old-model")); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	return manager, path
}

func requireRefreshed(t *testing.T, manager *coreauth.Manager, path, token string) {
	t.Helper()
	if _, errRefresh := manager.ForceRefreshAuth(context.Background(), trafficAuthID); errRefresh != nil {
		t.Fatalf("refresh: %v", errRefresh)
	}
	if got := diskField(t, path, "access_token"); got != token {
		t.Fatalf("the refresh left access_token %v on disk, want %s", got, token)
	}
	if current, _ := manager.GetByID(trafficAuthID); current.Metadata["access_token"] != token {
		t.Fatalf("runtime access_token = %v, want %s", current.Metadata["access_token"], token)
	}
}

// failedRequest records a 429 from a request that ran on the auth's current credential, as the executors tag their results.
func failedRequest(manager *coreauth.Manager) {
	current, _ := manager.GetByID(trafficAuthID)
	result := trafficResult(false)
	result.AccessToken, _ = current.Metadata["access_token"].(string)
	result.CredentialVersion = current.CredentialVersion
	result.RegistrationEpoch = current.RegistrationEpoch
	manager.MarkResult(context.Background(), result)
}

// The scan reads the file a refresh just wrote and a request fails before the snapshot applies: the snapshot carries the tokens the
// runtime already holds, so it keeps the request's cooldown, quota and model state like any snapshot of an unchanged credential.
func TestFileSnapshotOfAGatewayTokenRefreshKeepsRuntimeState(t *testing.T) {
	manager, path := newRefreshSnapshotManager(t)
	requireRefreshed(t, manager, path, "T2")

	snapshot := readTrafficSnapshot(t, path, "new-model")
	failedRequest(manager)
	want, _ := manager.GetByID(trafficAuthID)

	requireApplied(t, manager, snapshot)
	got, _ := manager.GetByID(trafficAuthID)
	if got.Attributes["excluded_models"] != "new-model" || got.Metadata["access_token"] != "T2" {
		t.Fatalf("snapshot not applied: excluded_models=%q access_token=%v", got.Attributes["excluded_models"], got.Metadata["access_token"])
	}
	assertResultRuntimeState(t, got, want)
}

// A scan read the file after the first refresh; a second refresh rewrites it before that snapshot applies. The snapshot is stale: it
// must be discarded, not applied as a replacement that rolls the tokens back and wipes the runtime state.
func TestStaleFileSnapshotOfAnEarlierGatewayRefreshIsDiscarded(t *testing.T) {
	manager, path := newRefreshSnapshotManager(t)
	requireRefreshed(t, manager, path, "T2")
	stale := readTrafficSnapshot(t, path, "new-model")

	requireRefreshed(t, manager, path, "T3")
	failedRequest(manager)
	want, _ := manager.GetByID(trafficAuthID)

	requireDiscarded(t, manager, stale)
	got, _ := manager.GetByID(trafficAuthID)
	if got.Metadata["access_token"] != "T3" {
		t.Fatalf("the stale snapshot rolled the access token back to %v", got.Metadata["access_token"])
	}
	assertResultRuntimeState(t, got, want)
}

// The synthesizer derives a Kimi file's base_url from its domain, so moving the file from kimi.com to kimi.ai under the same
// token is a change of endpoint that CredentialsChanged sees through the attribute.
func TestSynthesizedKimiDomainChangeIsACredentialChange(t *testing.T) {
	dir := t.TempDir()
	kimi := func(domain string) *coreauth.Auth {
		path := filepath.Join(dir, "kimi-"+domain+".json")
		data := []byte(`{"type":"kimi","access_token":"same-token","domain":"` + domain + `"}`)
		if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		auths, errSynthesize := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
			Config: &config.Config{}, AuthDir: dir, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator(),
		}, path, data)
		if errSynthesize != nil || len(auths) != 1 {
			t.Fatalf("synthesize %s: err=%v auths=%v", domain, errSynthesize, auths)
		}
		return auths[0]
	}
	com, ai := kimi("kimi.com"), kimi("kimi.ai")
	if com.Attributes["base_url"] == ai.Attributes["base_url"] || com.Attributes["base_url"] == "" {
		t.Fatalf("the synthesizer did not derive distinct base URLs: %q %q", com.Attributes["base_url"], ai.Attributes["base_url"])
	}
	if !coreauth.CredentialsChanged(com, ai) {
		t.Fatal("moving a Kimi file to another domain under the same token did not count as a credential change")
	}
	if coreauth.CredentialsChanged(com, kimi("kimi.com")) {
		t.Fatal("the same Kimi file counted as a credential change")
	}
}

// The auth package cannot import the executor helpers, so it carries a copy of the Devin default host; this pins the copy.
func TestDevinDefaultEndpointMatchesTheExecutor(t *testing.T) {
	devin := func(baseURL string) *coreauth.Auth {
		a := &coreauth.Auth{Provider: "devin", Metadata: map[string]any{"session_token": "same-token"}}
		if baseURL != "" {
			a.Metadata["base_url"] = baseURL
		}
		return a
	}
	if coreauth.CredentialsChanged(devin(helps.DevinDefaultBaseURL), devin("")) {
		t.Fatalf("the executor's default Devin host %q is not the default the identity assumes", helps.DevinDefaultBaseURL)
	}
	if !coreauth.CredentialsChanged(devin("https://devin.example"), devin("")) {
		t.Fatal("dropping a custom Devin base URL did not count as a change")
	}
}
