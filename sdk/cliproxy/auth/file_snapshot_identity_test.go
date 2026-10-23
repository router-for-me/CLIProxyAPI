package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rawJSONTestStorage stands in for plugin storage that exposes its credential only as raw JSON.
type rawJSONTestStorage struct {
	raw []byte
}

func (s *rawJSONTestStorage) SaveTokenToFile(string) error { return nil }
func (s *rawJSONTestStorage) RawJSON() []byte              { return s.raw }

// bindTestSnapshot returns an auth bound to a file holding data, as the watcher produces it.
func bindTestSnapshot(t *testing.T, authID, provider string, data []byte, attrs map[string]string) *Auth {
	t.Helper()
	path := filepath.Join(t.TempDir(), authID+".json")
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	attrs[AttributePath] = path
	snapshot := &Auth{ID: authID, Provider: provider, Status: StatusActive, Attributes: attrs}
	snapshot.BindFileSnapshot(path, data)
	return snapshot
}

// A file rewritten for another provider keeps its path-derived ID; the old provider's
// cooldown must not block the new credential.
func TestFileSnapshotForAnotherProviderStartsClean(t *testing.T) {
	const authID = "snapshot-provider-change"
	ctx := WithSkipPersist(context.Background())
	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "claude", Status: StatusActive}); errRegister != nil {
		t.Fatal(errRegister)
	}
	retryAfter := time.Minute
	manager.MarkResult(ctx, Result{
		AuthID: authID, Provider: "claude", Model: "m", RetryAfter: &retryAfter,
		Error: &Error{Message: "rate limited", HTTPStatus: http.StatusTooManyRequests},
	})

	snapshot := bindTestSnapshot(t, authID, "codex", []byte(`{"type":"codex"}`), map[string]string{})
	if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	got, _ := manager.GetByID(authID)
	if got.Provider != "codex" || got.Unavailable || got.Quota.Exceeded || !got.NextRetryAfter.IsZero() || got.LastError != nil {
		t.Fatalf("provider change kept the old cooldown: provider=%q unavailable=%v quota=%+v retry=%v last=%v",
			got.Provider, got.Unavailable, got.Quota, got.NextRetryAfter, got.LastError)
	}
	if blocked, _, _ := isAuthBlockedForModel(got, "m", time.Now()); blocked {
		t.Fatalf("provider change kept the old model cooldown: %+v", got.ModelStates)
	}
}

// A same-provider snapshot with replacement credentials must not inherit a transient
// refresh failure recorded for the previous token.
func TestFileSnapshotForSameProviderCredentialChangeStartsClean(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(transientErrorRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	const authID = "snapshot-credential-change"
	old := &Auth{
		ID:       authID,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token": "old-access-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, old); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	manager.refreshAuth(ctx, authID)
	failed, _ := manager.GetByID(authID)
	if failed.Status != StatusError || !failed.Unavailable || failed.LastError == nil {
		t.Fatalf("refresh failure did not mark old credential unavailable: %+v", failed)
	}

	snapshot := bindTestSnapshot(t, authID, "codex", []byte(`{"type":"codex","access_token":"new-access-token"}`), map[string]string{})
	snapshot.Metadata = map[string]any{
		"access_token":  "new-access-token",
		"refresh_token": "new-refresh-token",
	}
	if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	got, _ := manager.GetByID(authID)
	if got.Status != StatusActive || got.Unavailable || got.LastError != nil || !got.NextRetryAfter.IsZero() {
		t.Fatalf("credential replacement kept transient refresh failure: status=%q unavailable=%v last=%v retry=%v",
			got.Status, got.Unavailable, got.LastError, got.NextRetryAfter)
	}

}

// A Codex account switch must reset runtime state even when OAuth tokens are unchanged.
func TestFileSnapshotForCodexAccountChangeStartsClean(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(transientErrorRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	const authID = "snapshot-codex-account-change"
	old := &Auth{
		ID:       authID,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token": "same-access-token",

			"expired": time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, old); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	manager.refreshAuth(ctx, authID)
	failed, _ := manager.GetByID(authID)
	if failed.Status != StatusError || !failed.Unavailable || failed.LastError == nil {
		t.Fatalf("refresh failure did not mark old account unavailable: %+v", failed)
	}

	snapshot := bindTestSnapshot(t, authID, "codex", []byte(`{"type":"codex","access_token":"same-access-token","account_id":"account-b"}`), map[string]string{})
	snapshot.Metadata = map[string]any{
		"access_token": "same-access-token",
		"account_id":   "account-b",
	}
	if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	got, _ := manager.GetByID(authID)
	if got.Status != StatusActive || got.Unavailable || got.LastError != nil || !got.NextRetryAfter.IsZero() {
		t.Fatalf("account replacement kept transient refresh failure: status=%q unavailable=%v last=%v retry=%v",
			got.Status, got.Unavailable, got.LastError, got.NextRetryAfter)
	}
}

func TestCredentialsChangedCodexAccountRemoval(t *testing.T) {
	existing := &Auth{
		Provider: "codex",
		Metadata: map[string]any{"access_token": "same-access-token", "account_id": "account-a"},
	}
	incoming := &Auth{
		Provider: "codex",
		Metadata: map[string]any{"access_token": "same-access-token"},
	}
	if !CredentialsChanged(existing, incoming) {
		t.Fatal("removing Codex account_id must count as a credential change")
	}
}

// A same-provider Vertex service-account replacement must not inherit the old account's runtime state.
func TestFileSnapshotForVertexServiceAccountChangeStartsClean(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	const authID = "snapshot-vertex-service-account-change"
	old := &Auth{
		ID:       authID,
		Provider: "vertex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token": "same-access-token",
			"project":      "project-a",
			"service_account": map[string]any{
				"client_email":   "old@example.com",
				"private_key_id": "old-key",
			},
		},
	}
	if _, errRegister := manager.Register(ctx, old); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "vertex",
		Error:    &Error{Message: "upstream 503", HTTPStatus: http.StatusServiceUnavailable},
	})
	failed, _ := manager.GetByID(authID)
	if failed.Status != StatusError || !failed.Unavailable || failed.LastError == nil {
		t.Fatalf("failure did not mark old service account unavailable: %+v", failed)
	}

	snapshot := bindTestSnapshot(t, authID, "vertex", []byte(`{"type":"vertex","project_id":"project-b"}`), map[string]string{})
	snapshot.Metadata = map[string]any{
		"access_token": "same-access-token",
		"project":      "project-b",
		"service_account": map[string]any{
			"client_email":   "new@example.com",
			"private_key_id": "new-key",
		},
	}
	if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	got, _ := manager.GetByID(authID)
	if got.Status != StatusActive || got.Unavailable || got.LastError != nil || !got.NextRetryAfter.IsZero() {
		t.Fatalf("service-account replacement kept runtime failure: status=%q unavailable=%v last=%v retry=%v",
			got.Status, got.Unavailable, got.LastError, got.NextRetryAfter)
	}
}

func TestCredentialsChangedVertexServiceAccountIdentity(t *testing.T) {
	key, errKey := rsa.GenerateKey(rand.Reader, 2048)
	if errKey != nil {
		t.Fatalf("generate key: %v", errKey)
	}
	pkcs8, errMarshal := x509.MarshalPKCS8PrivateKey(key)
	if errMarshal != nil {
		t.Fatalf("marshal key: %v", errMarshal)
	}
	canonical := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	equivalent := strings.ReplaceAll(canonical, "\n", "\r\n")
	vertexAuth := func(privateKey, email string) *Auth {
		return &Auth{
			Provider: "vertex",
			Metadata: map[string]any{
				"project_id": "project-a",
				"service_account": map[string]any{
					"client_email": email,
					"private_key":  privateKey,
				},
			},
		}
	}
	if CredentialsChanged(vertexAuth(canonical, "same@example.com"), vertexAuth(equivalent, "same@example.com")) {
		t.Fatal("equivalent normalized Vertex service accounts must not count as a credential change")
	}

	malformed := func(email string) *Auth {
		return &Auth{
			Provider: "vertex",
			Metadata: map[string]any{
				"project_id":      "project-a",
				"service_account": map[string]any{"client_email": email},
			},
		}
	}
	if CredentialsChanged(malformed("same@example.com"), malformed("same@example.com")) {
		t.Fatal("equal malformed Vertex service accounts must compare by their raw map")
	}
	if !CredentialsChanged(malformed("old@example.com"), malformed("new@example.com")) {
		t.Fatal("different malformed Vertex service accounts must count as a credential change")
	}
}

func TestFileSnapshotForOpaqueStorageChangeStartsClean(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	const authID = "snapshot-opaque-storage-change"
	old := &Auth{
		ID:       authID,
		Provider: "plugin-provider",
		Status:   StatusActive,
		Metadata: map[string]any{"type": "plugin-provider"},
		Storage:  &rawJSONTestStorage{raw: []byte(`{"token":"old","scope":"default"}`)},
	}
	if _, errRegister := manager.Register(ctx, old); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: old.Provider,
		Error:    &Error{Message: "upstream 503", HTTPStatus: http.StatusServiceUnavailable},
	})
	failed, _ := manager.GetByID(authID)
	if failed.Status != StatusError || !failed.Unavailable || failed.LastError == nil {
		t.Fatalf("failure did not mark old storage unavailable: %+v", failed)
	}

	// Reordered JSON is the same canonical identity and must remain eligible for carry.
	equivalent := &Auth{
		ID:       authID,
		Provider: old.Provider,
		Status:   StatusActive,
		Metadata: map[string]any{"type": "plugin-provider"},
		Storage:  &rawJSONTestStorage{raw: []byte(`{"scope":"default","token":"old"}`)},
	}
	if CredentialsChanged(failed, equivalent) {
		t.Fatal("reordered equivalent storage unexpectedly changed identity")
	}

	snapshot := bindTestSnapshot(t, authID, old.Provider, []byte(`{"type":"plugin-provider"}`), map[string]string{})
	snapshot.Metadata = map[string]any{"type": "plugin-provider"}
	snapshot.Storage = &rawJSONTestStorage{raw: []byte(`{"token":"new","scope":"default"}`)}
	if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	got, _ := manager.GetByID(authID)
	if got.Status != StatusActive || got.Unavailable || got.LastError != nil || !got.NextRetryAfter.IsZero() {
		t.Fatalf("storage replacement kept runtime failure: status=%q unavailable=%v last=%v retry=%v",
			got.Status, got.Unavailable, got.LastError, got.NextRetryAfter)
	}
}

// An active credential-scoped quota belongs to the old token; a relogin snapshot starts clean.
func TestFileSnapshotForCredentialChangeDropsCredentialQuota(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	manager := NewManager(nil, nil, nil)
	const authID = "snapshot-credential-quota-change"
	if _, errRegister := manager.Register(ctx, &Auth{
		ID: authID, Provider: "claude", Status: StatusActive,
		Metadata: map[string]any{"access_token": "old-access-token"},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	retry := 3 * time.Hour
	manager.MarkResult(ctx, Result{
		AuthID: authID, Provider: "claude", Model: "m", RetryAfter: &retry, CredentialScope: true,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage limit"},
	})
	cooled, _ := manager.GetByID(authID)
	if cooled.Quota.Reason != "credential_quota" || !cooled.Unavailable {
		t.Fatalf("expected a credential_quota cooldown, got %+v", cooled.Quota)
	}

	snapshot := bindTestSnapshot(t, authID, "claude", []byte(`{"type":"claude"}`), map[string]string{})
	snapshot.Metadata = map[string]any{"access_token": "new-access-token"}
	if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	got, _ := manager.GetByID(authID)
	if got.Unavailable || got.Quota.Exceeded || !got.NextRetryAfter.IsZero() {
		t.Fatalf("relogin kept the old credential quota: unavailable=%v quota=%+v retry=%v", got.Unavailable, got.Quota, got.NextRetryAfter)
	}
	if blocked, _, _ := isAuthBlockedForModel(got, "m", time.Now()); blocked {
		t.Fatalf("relogin kept the old model cooldown: %+v", got.ModelStates)
	}
}

// Plugin RawJSON merges Auth.Metadata, so a routing edit such as priority is not a
// credential change; storage that cannot be inspected (login storage) is ignored.
func TestCredentialsChangedStorageIgnoresMetadataAndOpaqueStorage(t *testing.T) {
	pluginAuth := func(priority int) *Auth {
		return &Auth{
			Provider: "plugin-provider",
			Metadata: map[string]any{"priority": priority},
			Storage:  &rawJSONTestStorage{raw: []byte(fmt.Sprintf(`{"token":"same","priority":%d}`, priority))},
		}
	}
	if CredentialsChanged(pluginAuth(1), pluginAuth(5)) {
		t.Fatal("a priority edit counted as a plugin credential change")
	}
	login := &Auth{Provider: "codex", Metadata: map[string]any{"access_token": "t"}, Storage: &dummyMetadataStorage{}}
	reread := &Auth{Provider: "codex", Metadata: map[string]any{"access_token": "t"}}
	if CredentialsChanged(login, reread) {
		t.Fatal("storage without RawJSON counted as a credential change")
	}
}

// A Meta file can hold only the device token (dca_token); replacing it is a new login.
func TestCredentialsChangedMetaDCAToken(t *testing.T) {
	meta := func(dca string) *Auth { return &Auth{Provider: "meta", Metadata: map[string]any{"dca_token": dca}} }
	if !CredentialsChanged(meta("dca-a"), meta("dca-b")) {
		t.Fatal("a replaced Meta device token did not count as a credential change")
	}
	if CredentialsChanged(meta("dca-a"), meta("dca-a")) {
		t.Fatal("the same Meta device token counted as a credential change")
	}
	attribute := func(dca string) *Auth {
		return &Auth{Provider: "meta", Attributes: map[string]string{"dca_token": dca}}
	}
	if !CredentialsChanged(attribute("dca-a"), attribute("dca-b")) {
		t.Fatal("a replaced Meta device token kept in attributes did not count as a credential change")
	}
}

// A Devin file may hold only a session token (metadata or attributes), or the "token" attribute the executor falls back to.
func TestCredentialsChangedDevinSessionToken(t *testing.T) {
	metadata := func(token string) *Auth {
		return &Auth{Provider: "devin", Metadata: map[string]any{"session_token": token}}
	}
	attribute := func(key, token string) *Auth {
		return &Auth{Provider: "devin", Attributes: map[string]string{key: token}}
	}
	if !CredentialsChanged(metadata("session-a"), metadata("session-b")) {
		t.Fatal("a rotated Devin session token in metadata did not count as a credential change")
	}
	if !CredentialsChanged(attribute("session_token", "session-a"), attribute("session_token", "session-b")) {
		t.Fatal("a rotated Devin session token in attributes did not count as a credential change")
	}
	if !CredentialsChanged(attribute("token", "token-a"), attribute("token", "token-b")) {
		t.Fatal("a rotated Devin token attribute did not count as a credential change")
	}
	if CredentialsChanged(metadata("session-a"), metadata("session-a")) {
		t.Fatal("the same Devin session token counted as a credential change")
	}
}

// The upstream base URL is part of where a credential is sent: another endpoint under the same tokens is another target. The
// executors write base_url themselves when it is missing, so only two known and different values count.
func TestCredentialsChangedBaseURL(t *testing.T) {
	xai := func(baseURL string) *Auth {
		metadata := map[string]any{"access_token": "same-access-token"}
		if baseURL != "" {
			metadata["base_url"] = baseURL
		}
		return &Auth{Provider: "xai", Metadata: metadata}
	}
	if !CredentialsChanged(xai("https://a.example/v1"), xai("https://b.example/v1")) {
		t.Fatal("another upstream base URL did not count as a change")
	}
	if CredentialsChanged(xai("https://a.example/v1"), xai(" HTTPS://A.Example/v1/ ")) {
		t.Fatal("a padded, slash-terminated base URL or one with another scheme or host case counted as a change")
	}
	if !CredentialsChanged(xai("https://gateway.example/TeamA"), xai("https://gateway.example/teama")) {
		t.Fatal("a base URL that differs in path case counted as the same endpoint")
	}
	// xAI without a base_url sends to the official API: dropping a custom endpoint, or setting one, moves the requests; spelling
	// out the default does not.
	if !CredentialsChanged(xai("https://a.example/v1"), xai("")) || !CredentialsChanged(xai(""), xai("https://a.example/v1")) {
		t.Fatal("dropping or setting a custom xAI base URL did not count as a change")
	}
	if CredentialsChanged(xai("https://api.x.ai/v1"), xai("")) || CredentialsChanged(xai(""), xai(" https://API.x.ai/v1/ ")) {
		t.Fatal("spelling out the default xAI base URL counted as a change")
	}
	// Other executors write base_url themselves when it is missing, so a missing value is not a switch.
	meta := func(baseURL string) *Auth {
		a := &Auth{Provider: "meta", Metadata: map[string]any{"api_key": "same-key"}}
		if baseURL != "" {
			a.Metadata["base_url"] = baseURL
		}
		return a
	}
	if CredentialsChanged(meta(""), meta("https://a.example/v1")) || CredentialsChanged(meta("https://a.example/v1"), meta("")) {
		t.Fatal("a base URL the executor wrote, or a missing one, counted as a change")
	}
	kimi := func(baseURL string) *Auth {
		return &Auth{Provider: "kimi", Attributes: map[string]string{"base_url": baseURL}, Metadata: map[string]any{"access_token": "same-access-token"}}
	}
	if !CredentialsChanged(kimi("https://a.example/v1"), kimi("https://b.example/v1")) {
		t.Fatal("another base URL kept in attributes did not count as a change")
	}
	apiBase := func(baseURL string) *Auth {
		return &Auth{Provider: "meta", Metadata: map[string]any{"api_key": "same-key", "api_base_url": baseURL}}
	}
	if !CredentialsChanged(apiBase("https://a.example"), apiBase("https://b.example")) {
		t.Fatal("another Meta api_base_url did not count as a change")
	}
}

// An Antigravity request carries the file's project_id, so another project under the same tokens is another account.
// The executor stores the project it discovers; gaining or losing the field is not a replacement.
func TestCredentialsChangedAntigravityProject(t *testing.T) {
	antigravity := func(project string) *Auth {
		metadata := map[string]any{"access_token": "same-access-token"}
		if project != "" {
			metadata["project_id"] = project
		}
		return &Auth{Provider: "antigravity", Metadata: metadata}
	}
	if !CredentialsChanged(antigravity("project-a"), antigravity("project-b")) {
		t.Fatal("another Antigravity project did not count as a credential change")
	}
	if CredentialsChanged(antigravity(""), antigravity("project-a")) || CredentialsChanged(antigravity("project-a"), antigravity("")) {
		t.Fatal("a discovered or missing Antigravity project counted as a credential change")
	}
}

// The host reads prefix, proxy_url, disabled, priority, weight, the model routing keys and the fingerprint profile from the auth
// file itself and keeps them off Auth.Metadata, so an edit of one of them (under its legacy spelling too) changes the plugin's raw
// storage JSON without replacing the credential.
func TestCredentialsChangedStorageIgnoresHostOwnedFileKeys(t *testing.T) {
	plugin := func(raw string) *Auth {
		return &Auth{Provider: "plugin-provider", Storage: &rawJSONTestStorage{raw: []byte(raw)}}
	}
	for _, key := range []string{"prefix", "proxy_url", "disabled", "priority", "weight", "excluded_models", "model_aliases", "models",
		"fingerprint_profile", "excluded-models", "model-aliases", "fingerprint-profile", "proxy-url"} {
		before := plugin(`{"token":"same","` + key + `":"old"}`)
		after := plugin(`{"token":"same","` + key + `":"new"}`)
		if CredentialsChanged(before, after) {
			t.Fatalf("an edit of the host-owned key %q counted as a plugin credential change", key)
		}
	}
	if !CredentialsChanged(plugin(`{"token":"a","prefix":"p"}`), plugin(`{"token":"b","prefix":"p"}`)) {
		t.Fatal("a rotated plugin token went unnoticed")
	}
}

// A Vertex request is addressed to the file's location (default us-central1), so another location is another endpoint.
func TestCredentialsChangedVertexLocation(t *testing.T) {
	vertex := func(location string) *Auth {
		metadata := map[string]any{"access_token": "same-access-token", "project_id": "project-a"}
		if location != "" {
			metadata["location"] = location
		}
		return &Auth{Provider: "vertex", Metadata: metadata}
	}
	if !CredentialsChanged(vertex("us-central1"), vertex("europe-west4")) {
		t.Fatal("another Vertex location did not count as a change")
	}
	if CredentialsChanged(vertex(""), vertex("us-central1")) || CredentialsChanged(vertex("us-central1"), vertex(" us-central1 ")) {
		t.Fatal("the default or a padded Vertex location counted as a change")
	}
}

// Plugin storage may identify the credential by a 64-bit number; float64 would make neighbours equal.
func TestCredentialsChangedStorageKeepsLargeIntegers(t *testing.T) {
	plugin := func(id string) *Auth {
		return &Auth{Provider: "plugin-provider", Storage: &rawJSONTestStorage{raw: []byte(`{"account":` + id + `}`)}}
	}
	if !CredentialsChanged(plugin("9007199254740992"), plugin("9007199254740993")) {
		t.Fatal("storage identities that differ in a 64-bit integer counted as equal")
	}
	if !CredentialsChanged(plugin("1"), plugin("1} x")) {
		t.Fatal("storage with trailing bytes after the JSON value counted as the same identity")
	}
}

// A 401 rejects the access token for the account it was sent for. The same token under
// another account must not stay marked as rejected.
func TestSnapshotForAccountSwitchDropsRejectedToken(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	const authID = "account-switch-rejected-token"
	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{"access_token": "same-token", "account_id": "account-a"}}); errRegister != nil {
		t.Fatal(errRegister)
	}
	current, _ := manager.GetByID(authID)
	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "codex", Model: "m", AccessToken: "same-token",
		CredentialVersion: current.CredentialVersion, RegistrationEpoch: current.RegistrationEpoch,
		Error: &Error{Message: "unauthorized", HTTPStatus: http.StatusUnauthorized}})
	rejected, _ := manager.GetByID(authID)
	if rejected.RejectedAccessToken != "same-token" {
		t.Fatalf("a 401 did not mark the token rejected: %q", rejected.RejectedAccessToken)
	}

	snapshot := bindTestSnapshot(t, authID, "codex", []byte(`{"type":"codex","access_token":"same-token","account_id":"account-b"}`), map[string]string{})
	snapshot.Metadata = map[string]any{"access_token": "same-token", "account_id": "account-b"}
	if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	got, _ := manager.GetByID(authID)
	if got.RejectedAccessToken != "" {
		t.Fatalf("the new account inherited the rejected token %q", got.RejectedAccessToken)
	}
	if blocked, _, _ := isAuthBlockedForModel(got, "m", time.Now()); blocked {
		t.Fatal("the rejected-token marker still blocks the new account")
	}
}

// With cooldown persistence on, dropping the old state must rewrite the store, or a
// restart restores it for the replacement.
func TestSnapshotReplacementRewritesPersistedCooldowns(t *testing.T) {
	for _, tc := range []struct {
		name     string
		disabled bool
	}{{"enabled", false}, {"disabled on arrival", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			store := &recordingCooldownStateStore{}
			manager.SetCooldownStateStore(store)
			const authID = "replacement-persisted-cooldown"
			if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "claude", Status: StatusActive}); errRegister != nil {
				t.Fatal(errRegister)
			}
			retryAfter := time.Hour
			manager.MarkResult(ctx, Result{AuthID: authID, Provider: "claude", Model: "m", RetryAfter: &retryAfter,
				Error: &Error{Message: "rate limited", HTTPStatus: http.StatusTooManyRequests}})
			hasRecord := func() bool {
				for _, record := range store.savedRecords() {
					if record.AuthID == authID {
						return true
					}
				}
				return false
			}
			if !hasRecord() {
				t.Fatal("the model cooldown was not persisted before the replacement")
			}

			snapshot := bindTestSnapshot(t, authID, "codex", []byte(`{"type":"codex"}`), map[string]string{})
			if tc.disabled {
				snapshot.Disabled = true
				snapshot.Status = StatusDisabled
			}
			if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
				t.Fatalf("snapshot rejected: %v", errUpdate)
			}
			if hasRecord() {
				t.Fatal("the replaced credential's cooldown is still in the persisted store")
			}
		})
	}
}

// A provider change under the same access token is a replacement too: a late result of
// the old provider must not land on the new one.
func TestResultFromPreviousProviderIsIgnoredAfterProviderChange(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	const authID = "provider-switch-inflight"
	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "claude", Status: StatusActive,
		Metadata: map[string]any{"access_token": "same-token"}}); errRegister != nil {
		t.Fatal(errRegister)
	}
	inflight, _ := manager.GetByID(authID)

	snapshot := bindTestSnapshot(t, authID, "codex", []byte(`{"type":"codex","access_token":"same-token"}`), map[string]string{})
	snapshot.Metadata = map[string]any{"access_token": "same-token"}
	if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "claude", Model: "m", AccessToken: "same-token",
		CredentialVersion: inflight.CredentialVersion, RegistrationEpoch: inflight.RegistrationEpoch,
		Error: &Error{Message: "rate limited", HTTPStatus: http.StatusTooManyRequests}})
	got, _ := manager.GetByID(authID)
	if got.Unavailable || got.LastError != nil || got.Quota.Exceeded {
		t.Fatalf("old-provider failure landed on the new provider: unavailable=%v last=%v quota=%+v", got.Unavailable, got.LastError, got.Quota)
	}
}

// A request sent to the previous target can finish after the file moved to another target under the same tokens: another base URL,
// another custom authentication header, another xAI request mode, another Vertex OpenAPI location.
func TestResultFromPreviousTargetIsIgnoredAfterRequestTargetChange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		before   func(*Auth)
		after    func(*Auth)
	}{
		{"base url", "xai",
			func(a *Auth) { a.Metadata["base_url"] = "https://a.example/v1" },
			func(a *Auth) { a.Metadata["base_url"] = "https://b.example/v1" }},
		{"authentication header", "claude",
			func(a *Auth) { a.Attributes["header:Authorization"] = "Bearer old" },
			func(a *Auth) { a.Attributes["header:Authorization"] = "Bearer new" }},
		{"xai request mode", "xai",
			func(a *Auth) { a.Metadata["using_api"] = false },
			func(a *Auth) { a.Metadata["using_api"] = true }},
		{"xai mode on the cli proxy", "xai",
			func(a *Auth) {
				a.Metadata["base_url"] = "https://cli-chat-proxy.grok.com/v1"
				a.Metadata["using_api"] = false
			},
			func(a *Auth) {
				a.Metadata["base_url"] = "https://cli-chat-proxy.grok.com/v1"
				a.Metadata["using_api"] = true
			}},
		{"devin default base url", "devin",
			func(a *Auth) { a.Metadata["base_url"] = "https://devin.example" },
			func(a *Auth) {}},
		{"claude auth kind", "claude",
			func(a *Auth) { a.Metadata["auth_kind"] = "oauth" },
			func(a *Auth) { a.Metadata["auth_kind"] = "apikey" }},
		{"vertex openapi location", "vertex",
			func(a *Auth) { a.Attributes["openapi_location"] = "us-central1" },
			func(a *Auth) { a.Attributes["openapi_location"] = "europe-west4" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			authID := "target-switch-inflight-" + strings.ReplaceAll(tc.name, " ", "-")
			registered := &Auth{ID: authID, Provider: tc.provider, Status: StatusActive, Attributes: map[string]string{},
				Metadata: map[string]any{"access_token": "same-token"}}
			tc.before(registered)
			if _, errRegister := manager.Register(ctx, registered); errRegister != nil {
				t.Fatal(errRegister)
			}
			inflight, _ := manager.GetByID(authID)

			snapshot := bindTestSnapshot(t, authID, tc.provider, []byte(`{"type":"`+tc.provider+`","access_token":"same-token"}`), map[string]string{})
			snapshot.Metadata = map[string]any{"access_token": "same-token"}
			tc.after(snapshot)
			if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
				t.Fatalf("snapshot rejected: %v", errUpdate)
			}
			switched, _ := manager.GetByID(authID)
			if switched.CredentialVersion <= inflight.CredentialVersion {
				t.Fatalf("the change did not advance the credential version: %d -> %d", inflight.CredentialVersion, switched.CredentialVersion)
			}
			manager.MarkResult(ctx, Result{AuthID: authID, Provider: tc.provider, Model: "m", AccessToken: "same-token",
				CredentialVersion: inflight.CredentialVersion, RegistrationEpoch: inflight.RegistrationEpoch,
				Error: &Error{Message: "rate limited", HTTPStatus: http.StatusTooManyRequests}})
			got, _ := manager.GetByID(authID)
			if got.Unavailable || got.LastError != nil || got.Quota.Exceeded {
				t.Fatalf("old-target failure landed on the new target: unavailable=%v last=%v quota=%+v", got.Unavailable, got.LastError, got.Quota)
			}
		})
	}
}

// Settings that do not change where or how a request is sent must not count: other attributes, header spelling and order, equal
// settings written once as an attribute and once as metadata.
func TestCredentialsChangedRequestTargetSettings(t *testing.T) {
	headers := func(pairs ...string) *Auth {
		a := &Auth{Provider: "claude", Attributes: map[string]string{}, Metadata: map[string]any{"access_token": "same-token"}}
		for i := 0; i+1 < len(pairs); i += 2 {
			a.Attributes["header:"+pairs[i]] = pairs[i+1]
		}
		return a
	}
	if !CredentialsChanged(headers("Authorization", "Bearer old"), headers("Authorization", "Bearer new")) {
		t.Fatal("a changed Authorization header did not count as a credential change")
	}
	if !CredentialsChanged(headers("X-Api-Key", "a"), headers()) || !CredentialsChanged(headers(), headers("X-Api-Key", "a")) {
		t.Fatal("a gained or lost custom header did not count as a credential change")
	}
	if CredentialsChanged(headers("authorization", "Bearer x ", "X-A", "1"), headers("X-A", "1", "Authorization", "Bearer x")) {
		t.Fatal("header name case, value padding or order counted as a credential change")
	}
	xai := func(attribute string, metadata any) *Auth {
		a := &Auth{Provider: "xai", Attributes: map[string]string{}, Metadata: map[string]any{"access_token": "same-token"}}
		if attribute != "" {
			a.Attributes["using_api"] = attribute
		}
		if metadata != nil {
			a.Metadata["using_api"] = metadata
		}
		return a
	}
	if !CredentialsChanged(xai("", false), xai("", true)) || !CredentialsChanged(xai("false", nil), xai("true", nil)) {
		t.Fatal("a toggled xAI using_api did not count as a credential change")
	}
	if CredentialsChanged(xai("true", nil), xai("", true)) || CredentialsChanged(xai("", "true"), xai("", true)) || CredentialsChanged(xai("", " false "), xai("", false)) {
		t.Fatal("the same xAI using_api written as attribute, bool or string counted as a credential change")
	}
	// With a custom base URL both modes use it as it is, so toggling the mode changes nothing.
	custom := func(using bool) *Auth {
		a := xai("", using)
		a.Metadata["base_url"] = "https://gateway.example/v1"
		return a
	}
	if CredentialsChanged(custom(false), custom(true)) {
		t.Fatal("a toggled xAI using_api on a custom base URL counted as a credential change")
	}
	// On the CLI chat proxy URL the mode still decides which headers are sent.
	cliProxy := func(using bool) *Auth {
		a := xai("", using)
		a.Metadata["base_url"] = "https://cli-chat-proxy.grok.com/v1/"
		return a
	}
	if !CredentialsChanged(cliProxy(false), cliProxy(true)) {
		t.Fatal("a toggled xAI using_api on the CLI chat proxy URL did not count as a credential change")
	}
	// The mode defaults from auth_kind: OAuth uses the CLI chat proxy, anything else the official API.
	kind := func(kind string, using any) *Auth {
		a := xai("", using)
		a.Attributes["auth_kind"] = kind
		return a
	}
	if CredentialsChanged(kind("oauth", nil), kind("oauth", false)) || CredentialsChanged(kind("api_key", nil), kind("api_key", true)) {
		t.Fatal("an omitted using_api against the explicit default of its auth_kind counted as a credential change")
	}
	if !CredentialsChanged(kind("oauth", nil), kind("oauth", true)) || !CredentialsChanged(kind("api_key", nil), kind("api_key", false)) {
		t.Fatal("a using_api that differs from the default of its auth_kind did not count as a credential change")
	}
	// Devin without a base_url sends to its default host: dropping a custom one moves the requests, spelling out the default does not.
	devin := func(baseURL string) *Auth {
		a := &Auth{Provider: "devin", Metadata: map[string]any{"session_token": "same-token"}}
		if baseURL != "" {
			a.Metadata["base_url"] = baseURL
		}
		return a
	}
	if !CredentialsChanged(devin("https://devin.example"), devin("")) || !CredentialsChanged(devin(""), devin("https://devin.example")) {
		t.Fatal("dropping or setting a custom Devin base URL did not count as a change")
	}
	if CredentialsChanged(devin("https://server.codeium.com/"), devin("")) || CredentialsChanged(devin(""), devin("HTTPS://Server.Codeium.com")) {
		t.Fatal("spelling out the default Devin base URL counted as a change")
	}
	// Claude sends Authorization: Bearer for OAuth and x-api-key for an API key, whatever the token string is.
	claude := func(kind string) *Auth {
		return &Auth{Provider: "claude", Metadata: map[string]any{"access_token": "same-token", "auth_kind": kind}}
	}
	if !CredentialsChanged(claude("oauth"), claude("api_key")) {
		t.Fatal("a Claude file moved from OAuth to API key under the same token did not count as a credential change")
	}
	if CredentialsChanged(claude("apikey"), claude("api-key")) {
		t.Fatal("two spellings of the same auth kind counted as a credential change")
	}
	vertexIn := func(location, openapi, openapiLocation string) *Auth {
		a := &Auth{Provider: "vertex", Attributes: map[string]string{}, Metadata: map[string]any{"access_token": "same-token", "project_id": "project-a"}}
		if location != "" {
			a.Metadata["location"] = location
		}
		if openapi != "" {
			a.Attributes["openapi"] = openapi
		}
		if openapiLocation != "" {
			a.Attributes["openapi_location"] = openapiLocation
		}
		return a
	}
	vertex := func(openapi, openapiLocation string) *Auth { return vertexIn("", openapi, openapiLocation) }
	if !CredentialsChanged(vertex("true", "us-central1"), vertex("true", "europe-west4")) {
		t.Fatal("another Vertex OpenAPI location did not count as a credential change")
	}
	if !CredentialsChanged(vertex("true", "us-central1"), vertex("false", "us-central1")) {
		t.Fatal("a toggled Vertex OpenAPI flag did not count as a credential change")
	}
	// With OpenAPI off the location is never used; unset leaves the choice to model rules, so there it still counts.
	if CredentialsChanged(vertex("false", "us-central1"), vertex("0", "europe-west4")) || CredentialsChanged(vertex("true", "us-central1"), vertex("1", "us-central1")) {
		t.Fatal("an inert openapi_location edit or another spelling of the same flag counted as a credential change")
	}
	if !CredentialsChanged(vertex("", "us-central1"), vertex("", "europe-west4")) {
		t.Fatal("an openapi_location edit with the flag unset did not count as a credential change")
	}
	if CredentialsChanged(vertex("true", "us-central1"), vertex("true", " us-central1 ")) {
		t.Fatal("a padded Vertex OpenAPI location counted as a credential change")
	}
	// Without openapi_location the OpenAPI requests go to the regular location (default us-central1): spelling it out is not a change.
	if CredentialsChanged(vertex("true", ""), vertex("true", "us-central1")) || CredentialsChanged(vertexIn("europe-west4", "true", ""), vertexIn("europe-west4", "true", "europe-west4")) {
		t.Fatal("an openapi_location equal to the regular location it falls back to counted as a credential change")
	}
	if !CredentialsChanged(vertexIn("europe-west4", "true", ""), vertexIn("europe-west4", "true", "us-central1")) {
		t.Fatal("an openapi_location that differs from the regular location it fell back to did not count as a credential change")
	}
}
