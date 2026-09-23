package authsource_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/authsource"
	log "github.com/sirupsen/logrus"
)

type sliceSource struct {
	auths []*coreauth.Auth
	err   error
}

func (s sliceSource) List(context.Context) ([]*coreauth.Auth, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.auths, nil
}

type fakeTarget struct {
	auths   map[string]*coreauth.Auth
	updated []*coreauth.Auth
	added   []string
	removed []string
	failReg map[string]error
	failUpd map[string]error
}

func newFakeTarget(auths ...*coreauth.Auth) *fakeTarget {
	target := &fakeTarget{auths: map[string]*coreauth.Auth{}}
	for _, auth := range auths {
		if auth == nil || auth.ID == "" {
			continue
		}
		target.auths[auth.ID] = auth
	}
	return target
}

func (f *fakeTarget) List() []*coreauth.Auth {
	out := make([]*coreauth.Auth, 0, len(f.auths))
	for _, auth := range f.auths {
		out = append(out, auth)
	}
	return out
}

func (f *fakeTarget) Register(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil {
		return nil, nil
	}
	if err := f.failReg[auth.ID]; err != nil {
		return nil, err
	}
	f.auths[auth.ID] = auth
	f.added = append(f.added, auth.ID)
	return auth, nil
}

func (f *fakeTarget) Update(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil {
		return nil, nil
	}
	if err := f.failUpd[auth.ID]; err != nil {
		return nil, err
	}
	f.auths[auth.ID] = auth
	f.updated = append(f.updated, auth)
	return auth, nil
}

func (f *fakeTarget) Remove(_ context.Context, id string) {
	delete(f.auths, id)
	f.removed = append(f.removed, id)
}

func catalogAuth(id, label, provider, apiKey string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		Label:    label,
		Provider: provider,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey,
			coreauth.AttributeAPIKey:   apiKey,
			"pc_provider":              provider,
			"base_url":                 "https://example.invalid/" + provider,
		},
		Metadata: map[string]any{"access_token": "live-" + id},
	}
}

func TestNewWatcherSyncNilIsNoop(t *testing.T) {
	res, err := authsource.NewWatcher(nil, nil).Sync(context.Background())
	if err != nil || res != (authsource.Result{}) {
		t.Fatalf("nil watcher Sync = %+v, %v", res, err)
	}
	res, err = (*authsource.Watcher)(nil).Sync(context.Background())
	if err != nil || res != (authsource.Result{}) {
		t.Fatalf("nil receiver Sync = %+v, %v", res, err)
	}
}

func TestNewWatcherSyncReconcilesOwnedAuthsWithoutClobberingTokens(t *testing.T) {
	keep := catalogAuth("pc-cred-1", "same", "zhipu", "k1")
	stale := catalogAuth("pc-cred-2", "old", "minimax", "k2")
	gone := catalogAuth("pc-cred-3", "gone", "ollama", "k3")
	foreign := &coreauth.Auth{
		ID:       "file-oauth",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
			coreauth.AttributeSource:   coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{"access_token": "foreign-live"},
	}
	target := newFakeTarget(keep, stale, gone, foreign, nil)

	freshKeep := catalogAuth("pc-cred-1", "same", "zhipu", "k1")
	freshKeep.Metadata["access_token"] = "db-stale"
	freshStale := catalogAuth("pc-cred-2", "new-label", "minimax", "k2-rotated")
	freshStale.Metadata["access_token"] = "db-stale-2"
	freshStale.Attributes["header:X-Trace"] = "t-1"
	freshStale.Attributes["secret_blob"] = "s"
	freshStale.Attributes["pc_endpoint_url:openai_images"] = "https://new.example/images"
	freshStale.Attributes["pc_subs_active"] = "true"
	added := catalogAuth("pc-cred-4", "added", "anthropic", "k4")

	// Runtime attribute on the live auth must survive an operator update.
	stale.Attributes["runtime_only"] = "keep"
	stale.Attributes["header:X-Old"] = "retire"
	stale.Attributes["pc_subs_active"] = "true"

	res, err := authsource.NewWatcher(sliceSource{auths: []*coreauth.Auth{
		nil, {ID: "  "}, freshKeep, freshStale, added,
	}}, target).Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Added != 1 || res.Updated != 1 || res.Removed != 1 || res.Unchanged != 1 {
		t.Fatalf("result = %+v, want added/updated/removed/unchanged = 1", res)
	}
	if _, ok := target.auths["pc-cred-3"]; ok {
		t.Fatal("missing catalog auth was not removed")
	}
	if _, ok := target.auths["pc-cred-4"]; !ok {
		t.Fatal("new catalog auth was not registered")
	}
	if _, ok := target.auths["file-oauth"]; !ok {
		t.Fatal("foreign auth was removed")
	}
	if got := target.auths["pc-cred-1"].Metadata["access_token"]; got != "live-pc-cred-1" {
		t.Fatalf("unchanged auth token = %v, want live token (no Update)", got)
	}
	updated := target.auths["pc-cred-2"]
	if updated.Label != "new-label" {
		t.Fatalf("label = %q, want new-label", updated.Label)
	}
	if got := updated.Metadata["access_token"]; got != "live-pc-cred-2" {
		t.Fatalf("updated auth token = %v, want live token", got)
	}
	if updated.Attributes["header:X-Trace"] != "t-1" || updated.Attributes["secret_blob"] != "s" {
		t.Fatalf("operator prefixes were not copied: %#v", updated.Attributes)
	}
	if _, ok := updated.Attributes["header:X-Old"]; ok {
		t.Fatal("stale header attribute survived")
	}
	if updated.Attributes["pc_endpoint_url:openai_images"] != "https://new.example/images" {
		t.Fatalf("endpoint attribute = %q", updated.Attributes["pc_endpoint_url:openai_images"])
	}
	if updated.Attributes["runtime_only"] != "keep" {
		t.Fatal("non-operator attribute was dropped")
	}
	if len(target.updated) != 1 || target.updated[0].ID != "pc-cred-2" {
		t.Fatalf("updates = %v, want only pc-cred-2", target.updated)
	}
}

func TestNewWatcherSyncRotationCopiesTokensAndClearsFailure(t *testing.T) {
	live := catalogAuth("pc-cred-9", "x", "xai", "k")
	live.Metadata["access_token"] = "EXPIRED_ACCESS"
	live.Metadata["refresh_token"] = "OLD_REFRESH"
	live.Metadata["expired"] = "2020-01-01T00:00:00Z"
	live.Metadata["expires_at"] = "2020-01-01T00:00:00Z"
	live.LastError = &coreauth.Error{Code: "unauthorized", HTTPStatus: 401, Message: "expired"}
	live.Unavailable = true
	live.Status = coreauth.StatusError
	live.StatusMessage = "unauthorized"
	live.NextRefreshAfter = time.Now().Add(time.Hour)
	live.NextRetryAfter = time.Now().Add(time.Hour)
	live.ModelStates = map[string]*coreauth.ModelState{
		"grok-4":       {Unavailable: true, Status: coreauth.StatusError, LastError: &coreauth.Error{Message: "down"}},
		"grok-4-quota": {Unavailable: true, Status: coreauth.StatusError, Quota: coreauth.QuotaState{Exceeded: true}},
	}
	fresh := catalogAuth("pc-cred-9", "x", "xai", "k")
	fresh.Attributes[authsource.AttrTokenRotatedAt] = "2026-08-17T20:00:00Z"
	fresh.Metadata = map[string]any{
		"access_token":  "NEW_ACCESS",
		"refresh_token": "NEW_REFRESH",
		"expires_at":    "2026-08-18T05:20:48Z",
		"project_id":    "proj-new",
	}

	target := newFakeTarget(live)
	res, err := authsource.NewWatcher(sliceSource{auths: []*coreauth.Auth{fresh}}, target).Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Updated != 1 || res.Unchanged != 0 {
		t.Fatalf("result = %+v, want one update", res)
	}
	got := target.auths["pc-cred-9"]
	if got.Metadata["access_token"] != "NEW_ACCESS" || got.Metadata["refresh_token"] != "NEW_REFRESH" {
		t.Fatalf("rotated tokens = %#v", got.Metadata)
	}
	if got.Metadata["expires_at"] != "2026-08-18T05:20:48Z" {
		t.Fatalf("expires_at = %v", got.Metadata["expires_at"])
	}
	if _, ok := got.Metadata["expired"]; ok {
		t.Fatal("stale expired spelling survived rotation")
	}
	if got.Metadata["project_id"] != "proj-new" {
		t.Fatalf("project_id = %v", got.Metadata["project_id"])
	}
	if got.LastError != nil || got.Unavailable || got.Status != coreauth.StatusActive || got.StatusMessage != "" {
		t.Fatalf("failure state not cleared: err=%v unavailable=%v status=%s msg=%q", got.LastError, got.Unavailable, got.Status, got.StatusMessage)
	}
	if !got.NextRefreshAfter.IsZero() || !got.NextRetryAfter.IsZero() {
		t.Fatal("retry clocks were not cleared")
	}
	if st := got.ModelStates["grok-4"]; st == nil || st.Unavailable || st.Status != coreauth.StatusActive || st.LastError != nil {
		t.Fatalf("error model state = %+v, want cleared", st)
	}
	if st := got.ModelStates["grok-4-quota"]; st == nil || !st.Unavailable || !st.Quota.Exceeded {
		t.Fatalf("quota model state = %+v, want preserved", st)
	}
	if live.Metadata["access_token"] != "EXPIRED_ACCESS" {
		t.Fatal("Sync mutated the live auth instead of a clone")
	}
}

func TestNewWatcherSyncSameRotationMarkerKeepsLiveTokens(t *testing.T) {
	live := catalogAuth("pc-cred-8", "x", "xai", "k")
	live.Attributes[authsource.AttrTokenRotatedAt] = "gen-1"
	live.Metadata["access_token"] = "LIVE"
	fresh := catalogAuth("pc-cred-8", "renamed", "xai", "k")
	fresh.Attributes[authsource.AttrTokenRotatedAt] = "gen-1"
	fresh.Metadata["access_token"] = "DB"

	target := newFakeTarget(live)
	if _, err := authsource.NewWatcher(sliceSource{auths: []*coreauth.Auth{fresh}}, target).Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := target.auths["pc-cred-8"].Metadata["access_token"]; got != "LIVE" {
		t.Fatalf("access_token = %v, want LIVE", got)
	}
	if target.auths["pc-cred-8"].Label != "renamed" {
		t.Fatal("label update did not apply")
	}
}

func TestNewWatcherSyncUpdateErrorDoesNotStopLaterAuths(t *testing.T) {
	a := catalogAuth("pc-cred-1", "old", "zhipu", "k")
	b := catalogAuth("pc-cred-2", "old", "minimax", "k")
	target := newFakeTarget(a, b)
	target.failUpd = map[string]error{"pc-cred-1": errors.New("stale epoch")}
	freshA := catalogAuth("pc-cred-1", "new", "zhipu", "k")
	freshB := catalogAuth("pc-cred-2", "new", "minimax", "k")

	res, err := authsource.NewWatcher(sliceSource{auths: []*coreauth.Auth{freshA, freshB}}, target).Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pc-cred-1") {
		t.Fatalf("err = %v, want pc-cred-1", err)
	}
	if res.Updated != 1 {
		t.Fatalf("Updated = %d, want 1; res=%+v", res.Updated, res)
	}
	if target.auths["pc-cred-2"].Label != "new" {
		t.Fatal("the other credential was not updated")
	}
	if target.auths["pc-cred-1"].Label != "old" {
		t.Fatal("failed update was applied")
	}
}

func TestNewWatcherSyncRegisterErrorStops(t *testing.T) {
	target := newFakeTarget()
	target.failReg = map[string]error{"pc-cred-1": errors.New("boom")}
	_, err := authsource.NewWatcher(sliceSource{auths: []*coreauth.Auth{
		catalogAuth("pc-cred-1", "a", "zhipu", "k"),
	}}, target).Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "register pc-cred-1") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := target.auths["pc-cred-1"]; ok {
		t.Fatal("failed register was stored")
	}
}

func TestNewWatcherSyncListError(t *testing.T) {
	target := newFakeTarget(catalogAuth("pc-cred-1", "a", "zhipu", "k"))
	_, err := authsource.NewWatcher(sliceSource{err: errors.New("db down")}, target).Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list") {
		t.Fatalf("err = %v", err)
	}
	if len(target.removed)+len(target.added)+len(target.updated) != 0 {
		t.Fatal("list error mutated the target")
	}
}

func configShadow(id, provider, apiKey, base string) *coreauth.Auth {
	attrs := map[string]string{
		coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey,
		coreauth.AttributeSource:   "config:" + provider + "[test]",
		"base_url":                 base,
		"compat_name":              provider,
		"provider_key":             "openai-compatible-" + provider,
	}
	if apiKey != "" {
		attrs[coreauth.AttributeAPIKey] = apiKey
	}
	return &coreauth.Auth{
		ID:         id,
		Provider:   "openai-compatible-" + provider,
		Status:     coreauth.StatusActive,
		Attributes: attrs,
	}
}

func identified(id string, credID int64, provider, apiKey, base string) *coreauth.Auth {
	attrs := map[string]string{
		coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey,
		coreauth.AttributeAPIKey:   apiKey,
		"pc_provider":              provider,
		"pc_credential_id":         strconv.FormatInt(credID, 10),
		"pc_credential_type":       "api_key",
		"compat_name":              provider,
	}
	if base != "" {
		attrs["base_url"] = base
	}
	return &coreauth.Auth{ID: id, Provider: "openai-compatible-" + provider, Status: coreauth.StatusActive, Attributes: attrs}
}

func TestNewWatcherSyncRemovesConfigShadowWithTwin(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.StandardLogger().Out
	oldLevel := log.GetLevel()
	log.SetOutput(&buf)
	log.SetLevel(log.WarnLevel)
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetLevel(oldLevel)
	})

	id := identified("pc-cred-4", 4, "zhipu", "K-twin", "https://api.z.ai/v4")
	shadow := configShadow("openai-compatibility:zhipu:deadbeef", "zhipu", "K-twin", "https://example.invalid/zhipu")
	stale := configShadow("sdk-shadow-stale", "minimax", "old-key", "https://api.minimax.chat/v1")
	mini := identified("pc-cred-7", 7, "minimax", "new-key", "https://api.minimax.chat/v1")
	unknown := configShadow("sdk-shadow-foreign", "not-in-catalog", "orphan", "https://example.invalid/foreign")
	keyless := &coreauth.Auth{
		ID:       "openai-compatibility:ollama:deadbeef",
		Provider: "openai-compatible-ollama",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeSource: "config:ollama[test]",
			"base_url":               "https://ollama.example/v1",
			"compat_name":            "ollama",
			"provider_key":           "openai-compatible-ollama",
		},
	}
	ollama := identified("pc-cred-8", 8, "ollama", "k", "https://ollama.example/v1")
	if coreauth.IsConfigAPIKeyAuth(keyless) {
		t.Fatal("fixture must be keyless")
	}
	noBase := identified("pc-cred-5", 5, "moonshot", "k", "")
	baseHolder := configShadow("sdk-shadow-moonshot", "moonshot", "k", "https://moonshot.example/v1")
	native := &coreauth.Auth{
		ID: "pc-cred-11", Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey,
			coreauth.AttributeAPIKey:   "sk-ant",
			"pc_provider":              "anthropic",
			"base_url":                 "https://api.anthropic.com",
		},
	}
	nativeShadow := &coreauth.Auth{
		ID: "sdk-shadow-claude", Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey,
			coreauth.AttributeSource:   "config:claude[test]",
			"base_url":                 "https://api.anthropic.com",
		},
	}
	fileKey := &coreauth.Auth{
		ID: "file-api-key", Provider: "minimax", Status: coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey,
			coreauth.AttributeAPIKey:   "file-key",
			coreauth.AttributeSource:   coreauth.AuthSourceFile,
			"compat_name":              "minimax",
		},
	}

	target := newFakeTarget(id, shadow, stale, mini, unknown, keyless, ollama, noBase, baseHolder, native, nativeShadow, fileKey)
	res, err := authsource.NewWatcher(sliceSource{auths: []*coreauth.Auth{id, mini, ollama, noBase, native}}, target).Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.ShadowRemoved != 4 {
		t.Fatalf("ShadowRemoved=%d, want 4 (twin, stale key, keyless, native); res=%+v", res.ShadowRemoved, res)
	}
	if res.ShadowUnresolved != 2 {
		t.Fatalf("ShadowUnresolved=%d, want 2 (unknown provider + base gap)", res.ShadowUnresolved)
	}
	for _, gone := range []string{"openai-compatibility:zhipu:deadbeef", "sdk-shadow-stale", "openai-compatibility:ollama:deadbeef", "sdk-shadow-claude"} {
		if _, ok := target.auths[gone]; ok {
			t.Fatalf("shadow %s still present", gone)
		}
	}
	for _, kept := range []string{"pc-cred-4", "pc-cred-7", "pc-cred-8", "pc-cred-5", "pc-cred-11", "sdk-shadow-foreign", "sdk-shadow-moonshot", "file-api-key"} {
		if _, ok := target.auths[kept]; !ok {
			t.Fatalf("auth %s missing", kept)
		}
	}
	logs := buf.String()
	for _, needle := range []string{
		"cred_dual_registration",
		"openai-compatibility:zhipu:deadbeef",
		authsource.OwnedIDPrefix,
		"provider=zhipu",
	} {
		if !strings.Contains(logs, needle) {
			t.Errorf("log missing %q:\n%s", needle, logs)
		}
	}
}
