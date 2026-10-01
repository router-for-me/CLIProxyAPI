package claudemaster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func backendSessionStoreFixture(t *testing.T) ([]BackendCredential, []*coreauth.Auth) {
	t.Helper()
	var credentials []BackendCredential
	var auths []*coreauth.Auth
	for index, account := range []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"} {
		credential := BackendCredential{AuthDir: filepath.Join(canonicalTestTempDir(t), "auth"), Provider: "claude", AuthID: "selected.json"}
		credentials = append(credentials, credential)
		auths = append(auths, &coreauth.Auth{ID: []string{"runtime-a", "runtime-b"}[index], Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"account_uuid": account}})
	}
	return credentials, auths
}

func backendPersistedSelector(t *testing.T, credentials []BackendCredential, auths []*coreauth.Auth, key string) *backendSeriesSelector {
	t.Helper()
	selector := &backendSeriesSelector{provider: "claude"}
	for _, auth := range auths {
		if auth.AuthKind() == coreauth.AuthKindAPIKey {
			selector.backupAuthID = auth.ID
		} else {
			selector.authIDs = append(selector.authIDs, auth.ID)
		}
	}
	routes, err := newBackendSessionStore(credentials, auths, selector.backupAuthID, key)
	if err != nil {
		t.Fatal(err)
	}
	selector.routes = routes
	t.Cleanup(selector.Stop)
	return selector
}

func TestBackendSessionRoutesRestoreOriginAcrossRestartReorderAndCacheEviction(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	first := backendPersistedSelector(t, credentials, auths, "")
	reset := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	first.observeQuota(auths[0].ID, backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	first.observeQuota(auths[1].ID, backendWeeklyQuota{known: true, used: .2, resetsAt: reset.Add(time.Hour)})
	opts := backendAPIBackupTestOptions("durable-resume", false)
	requireBackendAPIBackupPick(t, first, opts, auths, auths[1].ID)
	first.Stop()
	// Both profile order and runtime IDs change on restart.
	auths[0], auths[1] = auths[1], auths[0]
	auths[0].ID, auths[1].ID = "restarted-b", "restarted-a"
	credentials[0], credentials[1] = credentials[1], credentials[0]
	restarted := backendPersistedSelector(t, credentials, auths, "")
	restarted.observeQuota("restarted-a", backendWeeklyQuota{known: true, used: .2, resetsAt: reset})
	restarted.observeQuota("restarted-b", backendWeeklyQuota{known: true, used: .2, resetsAt: reset.Add(time.Hour)})
	opaque := backendAPIBackupTestOptions("durable-resume", true)
	before := bytes.Clone(opaque.OriginalRequest)
	requireBackendAPIBackupPick(t, restarted, opaque, auths, "restarted-b")
	if !bytes.Equal(before, opaque.OriginalRequest) {
		t.Fatal("durable routing modified opaque payload")
	}
	identity, _ := backendSeriesSessionIDs(opaque)
	if !restarted.sessions.CompareAndDelete(identity, "restarted-b") {
		t.Fatal("setup did not evict in-memory binding")
	}
	requireBackendAPIBackupPick(t, restarted, opaque, auths, "restarted-b")
	child := opaque
	child.Headers = opaque.Headers.Clone()
	child.Headers.Set("X-Claude-Code-Agent-Id", "review-child")
	child.Headers.Set("X-Claude-Code-Parent-Agent-Id", "main")
	requireBackendAPIBackupPick(t, restarted, child, auths, "restarted-b")
}

func TestBackendSessionRoutesMissingOriginAndReloginNeverReuseOpaqueState(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	first := backendPersistedSelector(t, credentials, auths, "")
	opts := backendAPIBackupTestOptions("missing-origin", false)
	requireBackendAPIBackupPick(t, first, opts, auths, "runtime-a")
	first.Stop()
	// Only the other profile is configured; its replica still remembers A.
	onlyB := backendPersistedSelector(t, credentials[1:], auths[1:], "")
	if got, err := onlyB.Pick(t.Context(), "claude", backendAuthSelectionModel, backendAPIBackupTestOptions("missing-origin", true), auths[1:]); err == nil || got != nil {
		t.Fatal("missing origin moved opaque state to another configured account")
	}
	requireBackendAPIBackupPick(t, onlyB, opts, auths[1:], "runtime-b")
	onlyB.Stop()
	// A committed newer subset-pool route wins a stale replica on restart.
	full := backendPersistedSelector(t, credentials, auths, "")
	requireBackendAPIBackupPick(t, full, backendAPIBackupTestOptions("missing-origin", true), auths, "runtime-b")
	full.Stop()
	auths[1] = auths[1].Clone()
	auths[1].Metadata["account_uuid"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	relogged := backendPersistedSelector(t, credentials[1:], auths[1:], "")
	if got, err := relogged.Pick(t.Context(), "claude", backendAuthSelectionModel, backendAPIBackupTestOptions("missing-origin", true), auths[1:]); err == nil || got != nil {
		t.Fatal("a different account in the same profile reused previous opaque state")
	}
}

func TestBackendSessionRoutesAPIBackupSameKeyRestoresDifferentKeyRefuses(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	api := &coreauth.Auth{ID: "runtime-api", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey}}
	auths = append(auths, api)
	first := backendPersistedSelector(t, credentials, auths, "synthetic-key-never-persisted")
	reset := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range first.authIDs {
		first.observeQuota(id, backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	}
	requireBackendAPIBackupPick(t, first, backendAPIBackupTestOptions("api-resume", false), auths, api.ID)
	first.Stop()
	api.ID = "restarted-api"
	restarted := backendPersistedSelector(t, credentials, auths, "synthetic-key-never-persisted")
	requireBackendAPIBackupPick(t, restarted, backendAPIBackupTestOptions("api-resume", true), auths, api.ID)
	restarted.Stop()
	rotated := backendPersistedSelector(t, credentials, auths, "different-key")
	if got, err := rotated.Pick(t.Context(), "claude", backendAuthSelectionModel, backendAPIBackupTestOptions("api-resume", true), auths); err == nil || got != nil {
		t.Fatal("unverified API-key replacement reused opaque state")
	}
	for _, dir := range first.routes.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			raw, errRead := os.ReadFile(filepath.Join(dir, entry.Name()))
			if errRead != nil {
				t.Fatal(errRead)
			}
			if strings.Contains(string(raw), "synthetic-key") || strings.Contains(string(raw), "api-resume") || strings.Contains(string(raw), "account_uuid") || strings.Contains(entry.Name(), "api-resume") {
				t.Fatal("private routing record leaked credentials or raw session identity")
			}
			info, errStat := entry.Info()
			if errStat != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("routing file was not private")
			}
		}
	}
}

func TestBackendSessionRoutesPartialHandoffRefusesOpaqueAndRepairsBeforeDispatch(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	selector := backendPersistedSelector(t, credentials, auths, "")
	opts := backendAPIBackupTestOptions("partial-handoff", false)
	requireBackendAPIBackupPick(t, selector, opts, auths, "runtime-a")
	identity, _ := backendSeriesSessionIDs(opts)
	selector.routes.writeRoute = func(dir, name string, raw []byte) error {
		if dir == selector.routes.dirs[1] {
			return errors.New("synthetic replica write failure")
		}
		return writeBackendSessionRoute(dir, name, raw)
	}
	if err := selector.routes.bind(identity, auths[1]); err == nil {
		t.Fatal("injected partial write succeeded")
	}
	if got, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, backendAPIBackupTestOptions("partial-handoff", true), auths); err == nil || got != nil {
		t.Fatal("pending replica handoff dispatched opaque state")
	}
	selector.Stop()
	restarted := backendPersistedSelector(t, credentials, auths, "")
	// A clean request finishes all replicas before it can be dispatched.
	requireBackendAPIBackupPick(t, restarted, opts, auths, "runtime-b")
	route, found, coherent, ambiguous, err := restarted.routes.lookupReplicas(identity)
	if err != nil || !found || !coherent || ambiguous || !route.Committed || route.Revision != 2 {
		t.Fatalf("partial handoff was not repaired: route=%v found=%v coherent=%v ambiguous=%v error=%v", route, found, coherent, ambiguous, err)
	}
	restarted.Stop()
	onlyB := backendPersistedSelector(t, credentials[1:], auths[1:], "")
	requireBackendAPIBackupPick(t, onlyB, backendAPIBackupTestOptions("partial-handoff", true), auths[1:], "runtime-b")
}

func TestBackendSessionRoutesEqualRevisionConflictRefusesAndCamelCaseIdentityWorks(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	auths[0].Metadata["accountUuid"] = auths[0].Metadata["account_uuid"]
	delete(auths[0].Metadata, "account_uuid")
	selector := backendPersistedSelector(t, credentials, auths, "")
	opts := backendAPIBackupTestOptions("replica-conflict", false)
	requireBackendAPIBackupPick(t, selector, opts, auths, "runtime-a")
	identity, _ := backendSeriesSessionIDs(opts)
	raw, err := json.Marshal(backendSessionRoute{Origin: selector.routes.origin(auths[1]), Revision: 1, Committed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBackendSessionRoute(selector.routes.dirs[1], backendSessionHash(identity)+".json", raw); err != nil {
		t.Fatal(err)
	}
	if got, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, backendAPIBackupTestOptions("replica-conflict", true), auths); err == nil || got != nil {
		t.Fatal("equal-revision conflicting origins were guessed")
	}
}

func TestBackendSessionRoutesPendingAndPartialSuccessRemainSafeForSubset(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	// The first replica belongs to B, while quotas initially prefer A.
	credentials[0], credentials[1] = credentials[1], credentials[0]
	selector := backendPersistedSelector(t, credentials, auths, "")
	opts := backendAPIBackupTestOptions("accepted-handoff", false)
	requireBackendAPIBackupPick(t, selector, opts, auths, "runtime-a")
	reset := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	selector.observeQuota("runtime-a", backendWeeklyQuota{known: true, used: .95, resetsAt: reset})
	selector.observeQuota("runtime-b", backendWeeklyQuota{known: true, used: .2, resetsAt: reset.Add(time.Hour)})
	ctx := withBackendAttempt(t.Context())
	if picked, err := selector.Pick(ctx, "claude", backendAuthSelectionModel, opts, auths); err != nil || picked.ID != "runtime-b" {
		t.Fatalf("handoff selection failed: %v %v", picked, err)
	}
	identity, _ := backendSeriesSessionIDs(opts)
	route, found, coherent, ambiguous, err := selector.routes.lookupReplicas(identity)
	if err != nil || !found || !coherent || !ambiguous || route.Committed {
		t.Fatalf("selection alone certified dispatch: %v %v", route, err)
	}
	// Before any upstream success, even B's own subset replica must refuse an
	// A-origin opaque retry. There is no commit phase before dispatch now.
	onlyB := backendPersistedSelector(t, credentials[:1], auths[1:], "")
	if got, err := onlyB.Pick(t.Context(), "claude", backendAuthSelectionModel, backendAPIBackupTestOptions("accepted-handoff", true), auths[1:]); err == nil || got != nil {
		t.Fatal("subset trusted an unaccepted handoff")
	}
	onlyB.Stop()
	selector.routes.writeRoute = func(dir, name string, raw []byte) error {
		var record backendSessionRoute
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if record.Committed && dir == selector.routes.dirs[1] {
			return errors.New("synthetic commit-phase replica failure")
		}
		return writeBackendSessionRoute(dir, name, raw)
	}
	// Only a trusted accepted B response invokes this. Partial true replicas
	// therefore certify a real B origin, not a failed pre-dispatch intent.
	commitBackendRouteAttempt(ctx, "runtime-b")
	releaseBackendRouteAttempt(ctx)
	selector.Stop()
	onlyB = backendPersistedSelector(t, credentials[:1], auths[1:], "")
	requireBackendAPIBackupPick(t, onlyB, backendAPIBackupTestOptions("accepted-handoff", true), auths[1:], "runtime-b")
	onlyB.Stop()
	full := backendPersistedSelector(t, credentials, auths, "")
	requireBackendAPIBackupPick(t, full, backendAPIBackupTestOptions("accepted-handoff", true), auths, "runtime-b")
	route, _, coherent, ambiguous, err = full.routes.lookupReplicas(identity)
	if err != nil || !coherent || ambiguous || !route.Committed || route.Revision != 2 {
		t.Fatalf("accepted partial commit was not repaired: %v %v", route, err)
	}
}

func TestBackendSessionRoutesParentEpochProtectsDelayedOpaqueChild(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	selector := backendPersistedSelector(t, credentials, auths, "")
	parent := backendAPIBackupTestOptions("parent-epoch", false)
	requireBackendAPIBackupPick(t, selector, parent, auths, "runtime-a")
	child := backendAPIBackupTestOptions("parent-epoch", true)
	child.Headers.Set("X-Claude-Code-Agent-Id", "already-bound")
	child.Headers.Set("X-Claude-Code-Parent-Agent-Id", "main")
	requireBackendAPIBackupPick(t, selector, child, auths, "runtime-a")
	reset := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	selector.observeQuota("runtime-a", backendWeeklyQuota{known: true, used: .95, resetsAt: reset})
	selector.observeQuota("runtime-b", backendWeeklyQuota{known: true, used: .2, resetsAt: reset.Add(time.Hour)})
	requireBackendAPIBackupPick(t, selector, parent, auths, "runtime-b")
	requireBackendAPIBackupPick(t, selector, child, auths, "runtime-a")
	selector.Stop()
	restarted := backendPersistedSelector(t, credentials, auths, "")
	delayed := backendAPIBackupTestOptions("parent-epoch", true)
	delayed.Headers.Set("X-Claude-Code-Agent-Id", "delayed-first-request")
	delayed.Headers.Set("X-Claude-Code-Parent-Agent-Id", "main")
	if got, err := restarted.Pick(t.Context(), "claude", backendAuthSelectionModel, delayed, auths); err == nil || got != nil || !strings.Contains(err.Error(), "parent Claude conversation changed accounts") {
		t.Fatalf("delayed child guessed changed parent: %v %v", got, err)
	}
	cleanChild := backendAPIBackupTestOptions("parent-epoch", false)
	cleanChild.Headers = delayed.Headers.Clone()
	restarted.observeQuota("runtime-a", backendWeeklyQuota{known: true, used: .95, resetsAt: reset})
	restarted.observeQuota("runtime-b", backendWeeklyQuota{known: true, used: .2, resetsAt: reset.Add(time.Hour)})
	requireBackendAPIBackupPick(t, restarted, cleanChild, auths, "runtime-b")
	requireBackendAPIBackupPick(t, restarted, delayed, auths, "runtime-b")
}

func TestBackendSessionRoutesCountDoesNotRebindAndActiveGenerationBlocksHandoff(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	selector := backendPersistedSelector(t, credentials, auths, "")
	opts := backendAPIBackupTestOptions("active-origin", false)
	ctx := withBackendAttempt(t.Context())
	if picked, err := selector.Pick(ctx, "claude", backendAuthSelectionModel, opts, auths); err != nil || picked.ID != "runtime-a" {
		t.Fatalf("initial selection failed: %v %v", picked, err)
	}
	commitBackendRouteAttempt(ctx, "runtime-a")
	reset := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	selector.observeQuota("runtime-a", backendWeeklyQuota{known: true, used: .95, resetsAt: reset})
	selector.observeQuota("runtime-b", backendWeeklyQuota{known: true, used: .2, resetsAt: reset.Add(time.Hour)})
	countCtx := withBackendAttempt(t.Context())
	backendSetCountRequest(countCtx)
	if picked, err := selector.Pick(countCtx, "claude", backendAuthSelectionModel, opts, auths); err != nil || picked.ID != "runtime-b" {
		t.Fatalf("token count failed: %v %v", picked, err)
	}
	commitBackendRouteAttempt(countCtx, "runtime-b")
	identity, _ := backendSeriesSessionIDs(opts)
	route, _, err := selector.routes.lookup(identity)
	if err != nil || route.Origin != selector.routes.origin(auths[0]) || !route.Committed {
		t.Fatal("token counting changed the generation origin")
	}
	if picked, err := selector.Pick(withBackendAttempt(t.Context()), "claude", backendAuthSelectionModel, opts, auths); err == nil || picked != nil || !strings.Contains(err.Error(), "active request") {
		t.Fatalf("concurrent clean handoff moved a live generation: %v %v", picked, err)
	}
	releaseBackendRouteAttempt(ctx)
	requireBackendAPIBackupPick(t, selector, opts, auths, "runtime-b")
	selector.mu.Lock()
	active := len(selector.activeRoutes)
	selector.mu.Unlock()
	if active != 0 {
		t.Fatal("completed generation left an active-origin guard")
	}
}

func TestBackendSessionRoutesSameOriginRepairRetainsProofAfterRejectedAttempt(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	onlyB := backendPersistedSelector(t, credentials[1:], auths[1:], "")
	opts := backendAPIBackupTestOptions("known-origin-repair", false)
	requireBackendAPIBackupPick(t, onlyB, opts, auths[1:], "runtime-b")
	onlyB.Stop()
	full := backendPersistedSelector(t, credentials, auths, "")
	opaque := backendAPIBackupTestOptions("known-origin-repair", true)
	ctx := withBackendAttempt(t.Context())
	if picked, err := full.Pick(ctx, "claude", backendAuthSelectionModel, opaque, auths); err != nil || picked.ID != "runtime-b" {
		t.Fatalf("known origin failed to repair: %v %v", picked, err)
	}
	// This same-account request is rejected: no success notification occurs.
	releaseBackendRouteAttempt(ctx)
	identity, _ := backendSeriesSessionIDs(opts)
	route, _, coherent, ambiguous, err := full.routes.lookupReplicas(identity)
	if err != nil || !route.Committed || !coherent || ambiguous {
		t.Fatalf("same-origin rejected repair lost proof: %v %v", route, err)
	}
	full.Stop()
	restarted := backendPersistedSelector(t, credentials, auths, "")
	requireBackendAPIBackupPick(t, restarted, opaque, auths, "runtime-b")
}

func TestBackendSessionRoutesLateOlderSuccessCannotCommitNewEpoch(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	selector := backendPersistedSelector(t, credentials, auths, "")
	opts := backendAPIBackupTestOptions("late-success", false)
	requireBackendAPIBackupPick(t, selector, opts, auths, "runtime-a")
	identity, _ := backendSeriesSessionIDs(opts)
	if err := selector.routes.bind(identity, auths[1]); err != nil {
		t.Fatal(err)
	}
	oldB, _, err := selector.routes.lookup(identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := selector.routes.bind(identity, auths[0]); err != nil {
		t.Fatal(err)
	}
	if err := selector.routes.bind(identity, auths[1]); err != nil {
		t.Fatal(err)
	}
	if err := selector.routes.commit(identity, oldB); err != nil {
		t.Fatal(err)
	}
	latest, _, err := selector.routes.lookup(identity)
	if err != nil || latest.Revision != 4 || latest.Committed {
		t.Fatalf("late old response certified new epoch: %v %v", latest, err)
	}
}

func TestBackendSessionRoutesInheritedOpaqueChildKeepsParentProofAfterFailure(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	selector := backendPersistedSelector(t, credentials, auths, "")
	requireBackendAPIBackupPick(t, selector, backendAPIBackupTestOptions("inherited-proof", false), auths, "runtime-a")
	child := backendAPIBackupTestOptions("inherited-proof", true)
	child.Headers.Set("X-Claude-Code-Agent-Id", "first-opaque-child")
	child.Headers.Set("X-Claude-Code-Parent-Agent-Id", "main")
	ctx := withBackendAttempt(t.Context())
	if picked, err := selector.Pick(ctx, "claude", backendAuthSelectionModel, child, auths); err != nil || picked.ID != "runtime-a" {
		t.Fatalf("unchanged parent inheritance failed: %v %v", picked, err)
	}
	// No successful child response: inherited origin is still proven by the
	// unchanged committed parent, so a rejected first attempt must not erase it.
	releaseFailedBackendRouteAttempt(ctx, "runtime-a")
	identity, _ := backendSeriesSessionIDs(child)
	route, _, err := selector.routes.lookup(identity)
	if err != nil || !route.Committed || route.Revision != 1 {
		t.Fatalf("inherited origin lost parent proof: %v %v", route, err)
	}
	selector.Stop()
	restarted := backendPersistedSelector(t, credentials, auths, "")
	requireBackendAPIBackupPick(t, restarted, child, auths, "runtime-a")
}

func TestBackendSessionRoutesQuotaRetryReleasesOnlyFailedAttempt(t *testing.T) {
	credentials, auths := backendSessionStoreFixture(t)
	selector := backendPersistedSelector(t, credentials, auths, "")
	manager := coreauth.NewManager(nil, selector, nil)
	setBackendResultPolicy(manager, selector)
	opts := backendAPIBackupTestOptions("retry-active-release", false)
	ctx := withBackendAttempt(t.Context())
	if picked, err := selector.Pick(ctx, "claude", backendAuthSelectionModel, opts, auths); err != nil || picked.ID != "runtime-a" {
		t.Fatalf("first attempt failed: %v %v", picked, err)
	}
	result := backendSeriesQuotaResult("runtime-a")
	result.Options = opts
	manager.ResultPolicy().ApplyResultPolicy(ctx, result)
	if picked, err := selector.Pick(ctx, "claude", backendAuthSelectionModel, opts, auths); err != nil || picked.ID != "runtime-b" {
		t.Fatalf("failed attempt guard prevented quota retry: %v %v", picked, err)
	}
	// A delayed observer of A must not release the new B attempt.
	releaseFailedBackendRouteAttempt(ctx, "runtime-a")
	identity, _ := backendSeriesSessionIDs(opts)
	selector.mu.Lock()
	activeB := selector.activeRoutes[identity][selector.routes.origin(auths[1])]
	selector.mu.Unlock()
	if activeB != 1 {
		t.Fatal("old-auth result released the new retry guard")
	}
	commitBackendRouteAttempt(ctx, "runtime-b")
	// After accepted HTTP headers, a later stream error cannot release the
	// active-generation guard until the owning handler finishes/cancels.
	releaseFailedBackendRouteAttempt(ctx, "runtime-b")
	selector.mu.Lock()
	activeB = selector.activeRoutes[identity][selector.routes.origin(auths[1])]
	selector.mu.Unlock()
	if activeB != 1 {
		t.Fatal("late accepted-stream failure released the live guard")
	}
	releaseBackendRouteAttempt(ctx)
	route, _, err := selector.routes.lookup(identity)
	if err != nil || !route.Committed || route.Origin != selector.routes.origin(auths[1]) {
		t.Fatalf("successful retry origin was not committed: %v %v", route, err)
	}
}

func TestBackendSessionRoutesDuplicateCamelCaseAccountIsRejected(t *testing.T) {
	const account = "11111111-1111-4111-8111-111111111111"
	first := writeSyntheticBackendCredential(t, canonicalTestTempDir(t), "first.json", map[string]any{"type": "claude", "accountUuid": account})
	second := writeSyntheticBackendCredential(t, canonicalTestTempDir(t), "second.json", map[string]any{"type": "claude", "account_uuid": account})
	backend, err := NewBackendSeries(t.Context(), BackendSeriesOptions{Credentials: []BackendCredential{
		{AuthDir: first.AuthDir, Provider: "claude", AuthID: first.AuthID},
		{AuthDir: second.AuthDir, Provider: "claude", AuthID: second.AuthID},
	}})
	if backend != nil {
		_ = backend.Close()
	}
	if err == nil || backend != nil || !strings.Contains(err.Error(), "distinct Claude accounts") {
		t.Fatalf("duplicate snake/camel account accepted: %v", err)
	}
}

func TestBackendNativeSingletonRecordsOriginWithoutQuotaPollingAndPoolResumes(t *testing.T) {
	first := writeSyntheticBackendCredential(t, t.TempDir(), "first.json", map[string]any{"type": "claude", "access_token": integrationClaudeToken, "account_uuid": integrationClaudeAccount, "claude_device_ids": []string{integrationClaudeDevice}})
	first.Provider, first.Model, first.UseRequestModel = "claude", "", true
	backend, err := NewBackend(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if backend.seriesSelector == nil || backend.seriesSelector.routes == nil || backend.quotaPollDone != nil {
		t.Fatal("native singleton did not record origins without quota polling")
	}
	backend.manager.RegisterExecutor(&backendSeriesCapture{})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"start"}]}`))
	request.Header.Set("X-Claude-Code-Session-Id", "singleton-to-pool")
	writer := httptest.NewRecorder()
	backend.Handler().ServeHTTP(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("singleton request failed: %d %s", writer.Code, writer.Body.String())
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	second := writeSyntheticBackendCredential(t, t.TempDir(), "second.json", map[string]any{"type": "claude", "account_uuid": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"})
	pool, err := NewBackendSeries(t.Context(), BackendSeriesOptions{Credentials: []BackendCredential{{AuthDir: second.AuthDir, Provider: "claude", AuthID: second.AuthID}, {AuthDir: first.AuthDir, Provider: "claude", AuthID: first.AuthID}}, QuotaRequest: func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"seven_day":{"utilization":20,"resets_at":"2099-01-01T00:00:00Z"}}`))}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	auths := make([]*coreauth.Auth, 0, len(pool.authIDs))
	for _, id := range pool.authIDs {
		auth, _ := pool.manager.GetByID(id)
		auths = append(auths, auth)
	}
	requireBackendAPIBackupPick(t, pool.seriesSelector, backendAPIBackupTestOptions("singleton-to-pool", true), auths, pool.authIDs[1])
}
