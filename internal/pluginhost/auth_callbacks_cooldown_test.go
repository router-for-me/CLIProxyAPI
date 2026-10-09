package pluginhost

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// cooldownTestAuths returns a credential with model-scoped cooldowns, a
// credential with a credential-wide quota cooldown, and a healthy credential.
// Deadlines are hours away so the assertions do not depend on wall-clock timing.
func cooldownTestAuths(now time.Time) (modelScoped, credentialScoped, healthy *coreauth.Auth) {
	modelScoped = &coreauth.Auth{
		ID:         "cool-model.json",
		Provider:   "demo",
		FileName:   "cool-model.json",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"runtime_only": "true"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-a": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(time.Hour),
				LastError:      &coreauth.Error{HTTPStatus: 503},
			},
			"model-b": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(2 * time.Hour),
				Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(2 * time.Hour), BackoffLevel: 2},
				LastError:      &coreauth.Error{HTTPStatus: 429},
			},
			"model-ready": {Status: coreauth.StatusActive},
		},
	}
	credentialScoped = &coreauth.Auth{
		ID:             "cool-credential.json",
		Provider:       "demo",
		FileName:       "cool-credential.json",
		Status:         coreauth.StatusError,
		Unavailable:    true,
		NextRetryAfter: now.Add(5 * time.Hour),
		Quota:          coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(5 * time.Hour)},
		Attributes:     map[string]string{"runtime_only": "true"},
	}
	healthy = &coreauth.Auth{
		ID:         "cool-healthy.json",
		Provider:   "demo",
		FileName:   "cool-healthy.json",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"runtime_only": "true"},
	}
	for _, auth := range []*coreauth.Auth{modelScoped, credentialScoped, healthy} {
		auth.EnsureIndex()
	}
	return modelScoped, credentialScoped, healthy
}

func newCooldownTestHost(t *testing.T, homeEnabled bool, auths ...*coreauth.Auth) *Host {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	if homeEnabled {
		manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	}
	host := New()
	host.SetAuthManager(manager)
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	return host
}

func callHostAuthGetRuntimeForTest(t *testing.T, host *Host, authIndex string) pluginapi.HostAuthFileEntry {
	t.Helper()
	req, errMarshal := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGetRuntime, req)
	if errCall != nil {
		t.Fatalf("callFromPlugin(get_runtime) error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthGetRuntimeResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode get_runtime response: %v", errDecode)
	}
	return resp.Auth
}

func callHostAuthListForTest(t *testing.T, host *Host) []byte {
	t.Helper()
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthList, nil)
	if errCall != nil {
		t.Fatalf("callFromPlugin(list) error = %v", errCall)
	}
	return rawResp
}

// ceilSeconds rounds up like the host's cooldown view does.
func ceilSeconds(d time.Duration) int64 {
	seconds := int64(d / time.Second)
	if d%time.Second != 0 {
		seconds++
	}
	return seconds
}

// assertHostCooldowns checks cooldowns read by a callback that ran between before and after,
// so RemainingSeconds is bounded by those two clock reads rather than a later one.
func assertHostCooldowns(t *testing.T, label string, before, after time.Time, got []pluginapi.HostAuthCooldown, want []pluginapi.HostAuthCooldown) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: cooldowns = %+v, want %d entries", label, got, len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Scope != w.Scope || g.ModelKey != w.ModelKey || g.Reason != w.Reason || g.HTTPStatus != w.HTTPStatus {
			t.Fatalf("%s: cooldowns[%d] = %+v, want %+v", label, i, g, w)
		}
		if !reflect.DeepEqual(g.BackoffLevel, w.BackoffLevel) {
			t.Fatalf("%s: cooldowns[%d].BackoffLevel = %v, want %v", label, i, g.BackoffLevel, w.BackoffLevel)
		}
		if !g.RetryAt.Equal(w.RetryAt) {
			t.Fatalf("%s: cooldowns[%d].RetryAt = %v, want %v", label, i, g.RetryAt, w.RetryAt)
		}
		minRemaining, maxRemaining := ceilSeconds(w.RetryAt.Sub(after)), ceilSeconds(w.RetryAt.Sub(before))
		if g.RemainingSeconds < minRemaining || g.RemainingSeconds > maxRemaining {
			t.Fatalf("%s: cooldowns[%d].RemainingSeconds = %d, want in [%d, %d]", label, i, g.RemainingSeconds, minRemaining, maxRemaining)
		}
	}
}

func TestHostAuthCallbacksReturnCooldowns(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	modelScoped, credentialScoped, healthy := cooldownTestAuths(now)
	host := newCooldownTestHost(t, false, modelScoped, credentialScoped, healthy)

	backoff := 2
	wantModel := []pluginapi.HostAuthCooldown{
		{Scope: "model", ModelKey: "model-a", Reason: "transient_error", RetryAt: now.Add(time.Hour), HTTPStatus: 503},
		{Scope: "model", ModelKey: "model-b", Reason: "quota", RetryAt: now.Add(2 * time.Hour), BackoffLevel: &backoff, HTTPStatus: 429},
	}
	wantCredential := []pluginapi.HostAuthCooldown{
		{Scope: "credential", Reason: "credential_quota", RetryAt: now.Add(5 * time.Hour)},
	}

	before := time.Now()
	gotModel := callHostAuthGetRuntimeForTest(t, host, modelScoped.Index).Cooldowns
	assertHostCooldowns(t, "get_runtime model", before, time.Now(), gotModel, wantModel)
	before = time.Now()
	gotCredential := callHostAuthGetRuntimeForTest(t, host, credentialScoped.Index).Cooldowns
	assertHostCooldowns(t, "get_runtime credential", before, time.Now(), gotCredential, wantCredential)
	if got := callHostAuthGetRuntimeForTest(t, host, healthy.Index).Cooldowns; got != nil {
		t.Fatalf("get_runtime healthy: cooldowns = %+v, want nil", got)
	}

	before = time.Now()
	rawList := callHostAuthListForTest(t, host)
	after := time.Now()
	list, errDecode := decodeRPCEnvelope[rpcHostAuthListResponse](rawList)
	if errDecode != nil {
		t.Fatalf("decode list response: %v", errDecode)
	}
	byIndex := make(map[string]pluginapi.HostAuthFileEntry, len(list.Files))
	for _, entry := range list.Files {
		byIndex[entry.AuthIndex] = entry
	}
	assertHostCooldowns(t, "list model", before, after, byIndex[modelScoped.Index].Cooldowns, wantModel)
	assertHostCooldowns(t, "list credential", before, after, byIndex[credentialScoped.Index].Cooldowns, wantCredential)

	// Wire format: the field is "cooldowns" and is omitted when empty, so
	// existing entries keep their previous shape.
	rawFiles, errRaw := decodeRPCEnvelope[struct {
		Files []map[string]json.RawMessage `json:"files"`
	}](rawList)
	if errRaw != nil {
		t.Fatalf("decode raw list response: %v", errRaw)
	}
	for _, file := range rawFiles.Files {
		var authIndex string
		if errUnmarshal := json.Unmarshal(file["auth_index"], &authIndex); errUnmarshal != nil {
			t.Fatalf("decode auth_index: %v", errUnmarshal)
		}
		_, hasCooldowns := file["cooldowns"]
		if wantKey := authIndex != healthy.Index; hasCooldowns != wantKey {
			t.Fatalf("list entry %s: has cooldowns key = %v, want %v", authIndex, hasCooldowns, wantKey)
		}
	}
}

func TestHostAuthCallbacksOmitCooldownsInHomeMode(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	// Home mode clears most local timers on registration, but an unauthorized
	// credential keeps its model timers, so the local view is still non-empty.
	auth := &coreauth.Auth{
		ID:          "cool-home.json",
		Provider:    "demo",
		FileName:    "cool-home.json",
		Status:      coreauth.StatusError,
		Unavailable: true,
		LastError:   &coreauth.Error{HTTPStatus: 401},
		Attributes:  map[string]string{"runtime_only": "true"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-a": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour), LastError: &coreauth.Error{HTTPStatus: 503}},
			"model-b": {Status: coreauth.StatusActive},
		},
	}
	auth.EnsureIndex()
	host := newCooldownTestHost(t, true, auth)
	registered, ok := host.currentAuthManager().GetByID(auth.ID)
	if !ok {
		t.Fatal("registered auth not found")
	}
	if views := coreauth.CooldownSnapshotForAuth(registered, time.Now()); len(views) == 0 {
		t.Fatal("precondition: expected a non-empty local cooldown view in home mode")
	}

	if got := callHostAuthGetRuntimeForTest(t, host, auth.Index).Cooldowns; got != nil {
		t.Fatalf("get_runtime in home mode: cooldowns = %+v, want nil", got)
	}
	list, errDecode := decodeRPCEnvelope[rpcHostAuthListResponse](callHostAuthListForTest(t, host))
	if errDecode != nil {
		t.Fatalf("decode list response: %v", errDecode)
	}
	if len(list.Files) != 1 {
		t.Fatalf("list files = %+v, want 1 entry", list.Files)
	}
	for _, entry := range list.Files {
		if entry.Cooldowns != nil {
			t.Fatalf("list %s in home mode: cooldowns = %+v, want nil", entry.ID, entry.Cooldowns)
		}
	}
}

// TestHostAuthCooldownMirrorsCooldownView guards the pluginapi mirror type
// against drifting from the Management API cooldown view.
func TestHostAuthCooldownMirrorsCooldownView(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	auth := &coreauth.Auth{ModelStates: map[string]*coreauth.ModelState{
		"model-b": {
			Unavailable:    true,
			NextRetryAfter: now.Add(time.Minute),
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute), BackoffLevel: 4},
			LastError:      &coreauth.Error{HTTPStatus: 429},
		},
	}}
	views := coreauth.CooldownSnapshotForAuth(auth, now)
	if len(views) != 1 || views[0].BackoffLevel == nil || views[0].HTTPStatus == 0 {
		t.Fatalf("views = %+v, want one fully populated view", views)
	}
	wantJSON, errWant := json.Marshal(views)
	if errWant != nil {
		t.Fatalf("marshal views: %v", errWant)
	}
	gotJSON, errGot := json.Marshal(hostAuthCooldowns(auth, now))
	if errGot != nil {
		t.Fatalf("marshal host cooldowns: %v", errGot)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("host cooldowns JSON = %s, want %s", gotJSON, wantJSON)
	}
}
