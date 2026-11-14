package auth

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestBackgroundRefreshDoesNotTransferSaveMarkerToReplacement(t *testing.T) {
	for _, operation := range []string{"update", "modify"} {
		t.Run(operation, func(t *testing.T) {
			for _, mode := range []string{"due_replacement", "unchanged", "replacement_backoff", "equal_replacement_backoff"} {
				t.Run(mode, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						runReplacementSaveMarker(t, operation, mode)
					})
				})
			}
		})
	}
}

func runReplacementSaveMarker(t *testing.T, operation, mode string) {
	t.Helper()
	f := newObsoleteRefreshFixture(t, true, false)
	defer f.stop(t)
	store := &obsoleteRefreshSaveStore{entered: make(chan struct{}), unblock: make(chan struct{})}
	defer store.release()
	f.manager.SetStore(store)
	before := f.current(t)
	wantBackoff := time.Time{}
	switch mode {
	case "replacement_backoff":
		wantBackoff = time.Now().Add(2 * refreshPendingBackoff)
	case "equal_replacement_backoff":
		// Equal timestamps do not make a replacement's deliberate backoff
		// owned by the obsolete job. No synthetic time elapses before enqueue.
		wantBackoff = time.Now().Add(refreshPendingBackoff)
	}
	edit := func(auth *Auth) error {
		auth.Metadata["display_note"] = "synthetic replacement marker control"
		if mode != "unchanged" {
			auth.Metadata["access_token"] = "synthetic-obsolete-c"
			auth.Metadata["refresh_token"] = "synthetic-obsolete-grant-c"
			auth.Metadata["expired"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
			auth.NextRefreshAfter = wantBackoff
		}
		return nil
	}
	type updateResult struct {
		auth      *Auth
		committed bool
		err       error
	}
	result := make(chan updateResult, 1)
	f.foregroundDone = make(chan struct{})
	// Cleanup is already registered before this worker. It releases Store.Save
	// before the existing bounded foreground/refresh-worker joins.
	go func() {
		defer close(f.foregroundDone)
		if operation == "modify" {
			auth, committed, err := f.manager.Modify(context.Background(), f.id, before.RegistrationEpoch, edit)
			result <- updateResult{auth: auth, committed: committed, err: err}
			return
		}
		candidate := before.Clone()
		_ = edit(candidate)
		auth, err := f.manager.Update(context.Background(), candidate)
		result <- updateResult{auth: auth, committed: auth != nil && err == nil, err: err}
	}()
	awaitObsoleteRefresh(t, store.entered)
	// The mutation gate remains held, but Store.Save releases manager.mu.
	// A real scheduler decision now queues the still-current old credential.
	f.queueDue(t)
	f.manager.mu.RLock()
	job := f.manager.refreshJobs[f.id]
	pending := f.manager.auths[f.id].NextRefreshAfter
	queuedOld := job != nil && !job.running && job.credentialVersion == before.CredentialVersion
	f.manager.mu.RUnlock()
	if !queuedOld || pending.IsZero() || !store.outcome.Equal(wantBackoff) {
		t.Fatal("obsolete replacement save ordering")
	}
	if mode == "equal_replacement_backoff" && !wantBackoff.Equal(pending) {
		t.Fatal("obsolete replacement equal timestamp precondition")
	}
	if mode == "replacement_backoff" && wantBackoff.Equal(pending) {
		t.Fatal("obsolete replacement distinct timestamp precondition")
	}
	store.release()
	awaitObsoleteRefresh(t, f.foregroundDone)
	updated := <-result
	current := f.current(t)
	changed := mode != "unchanged"
	if updated.err != nil || !updated.committed || updated.auth == nil ||
		current.RegistrationEpoch != before.RegistrationEpoch ||
		(current.CredentialVersion > before.CredentialVersion) != changed ||
		current.Metadata["display_note"] != "synthetic replacement marker control" {
		t.Fatal("obsolete replacement publication")
	}
	wantToken, wantGrant := "synthetic-obsolete-a", "synthetic-obsolete-grant-a"
	if changed {
		wantToken, wantGrant = "synthetic-obsolete-c", "synthetic-obsolete-grant-c"
	}
	if authAccessToken(current) != wantToken || authRefreshToken(current) != wantGrant || current.HasValidAccessToken(time.Now()) {
		t.Fatal("obsolete replacement credential precondition")
	}
	if !changed && (!current.NextRefreshAfter.Equal(pending) || f.manager.shouldRefresh(current, time.Now())) {
		t.Fatal("obsolete replacement unchanged marker lost")
	}
	if changed && !current.NextRefreshAfter.Equal(wantBackoff) {
		t.Error("obsolete replacement inherited save marker")
	}
	f.startWorker(t)
	synctest.Wait()
	requireObsoleteJobGone(t, f)
	current = f.current(t)
	if !changed {
		f.requireFinished(t)
		if f.exec.oldRefresh.Load() != 1 || f.exec.freshRefresh.Load() != 0 {
			t.Fatal("obsolete replacement unchanged dispatch")
		}
		return
	}
	if f.exec.oldRefresh.Load() != 0 || f.exec.freshRefresh.Load() != 0 {
		t.Fatal("obsolete replacement stale job dispatch")
	}
	if !current.NextRefreshAfter.Equal(wantBackoff) {
		t.Error("obsolete replacement outcome changed")
	}
	if !wantBackoff.IsZero() {
		if f.manager.shouldRefresh(current, time.Now()) {
			t.Fatal("obsolete replacement backoff ignored")
		}
		return
	}
	// Finishing the obsolete job must leave the expired replacement eligible
	// now, without advancing the clock through its predecessor's one-minute gate.
	if !f.manager.shouldRefresh(current, time.Now()) {
		t.Error("obsolete replacement prompt eligibility")
		return
	}
	f.loop.handleDueAuth(context.Background(), time.Now(), f.id)
	synctest.Wait()
	f.requireFinished(t)
	if f.exec.oldRefresh.Load() != 0 || f.exec.freshRefresh.Load() != 1 {
		t.Fatal("obsolete replacement current job dispatch")
	}
}
