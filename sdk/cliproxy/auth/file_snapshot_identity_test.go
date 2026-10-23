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

// The host reads prefix, proxy_url, disabled, priority and weight from the auth file itself and keeps them off Auth.Metadata, so
// an edit of one of them changes the plugin's raw storage JSON without replacing the credential.
func TestCredentialsChangedStorageIgnoresHostOwnedFileKeys(t *testing.T) {
	plugin := func(raw string) *Auth {
		return &Auth{Provider: "plugin-provider", Storage: &rawJSONTestStorage{raw: []byte(raw)}}
	}
	for _, key := range []string{"prefix", "proxy_url", "disabled", "priority", "weight"} {
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
