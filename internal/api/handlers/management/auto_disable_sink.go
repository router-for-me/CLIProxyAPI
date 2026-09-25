package management

import (
	"context"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// autoDisablePersister is the minimal persistence surface the auto-disable
// sink needs — a subset of store.UpstreamProviderStore. Narrowing the adapter
// to this interface keeps its unit tests hermetic (they inject a two-method
// stub instead of a full store fake) and documents that the sink touches only
// this one column family.
type autoDisablePersister interface {
	SetEntryAutoDisabled(ctx context.Context, entryID int64, code string) (bool, error)
}

// renderUpstreamProviders is the re-render step invoked after a successful
// persistence. In production it is Handler.applyUpstreamProviders
// (re-renders config from PG + swaps the live config + reloads); tests inject
// a counter. It must NOT be called while the Handler's mutex is held —
// applyUpstreamProviders itself documents "Caller must NOT hold h.mu".
type renderUpstreamProviders func()

// newAutoDisableSink builds the coreauth.AutoDisableSink adapter. It is
// fire-and-forget: the caller (the auth conductor) invokes the returned
// function from the classification path, and every persistence/render step
// runs in its own goroutine with a recovered panic and a bounded 5s DB
// context, so a slow or failing store can never block request classification.
//
// A positive EntryID keys the UPDATE (entry IDs are globally unique across
// providers); EntryID<=0 events cannot be attributed to a PG row and are
// dropped with a warning. After a successful write the render fn runs only
// when the store reported a change — an already-auto-disabled or manually-
// disabled entry needs no re-render.
func newAutoDisableSink(persist autoDisablePersister, render renderUpstreamProviders) coreauth.AutoDisableSink {
	return func(_ context.Context, ev coreauth.AutoDisableEvent) {
		if ev.EntryID <= 0 {
			log.WithField("entry_id", ev.EntryID).Warn("auto-disable: unattributable entry (no PG id), skipping")
			return
		}
		go func() {
			defer func() { _ = recover() }()
			wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			changed, err := persist.SetEntryAutoDisabled(wctx, ev.EntryID, ev.Code)
			if err != nil {
				log.WithError(err).WithField("entry_id", ev.EntryID).Warn("auto-disable: persist failed")
				return
			}
			if !changed {
				// Already auto-disabled, or manually disabled (never clobber) —
				// the rendered config already skips this entry, no re-render.
				return
			}
			render()
		}()
	}
}

// AutoDisableSink returns the coreauth.AutoDisableSink adapter that persists
// the conductor's auto-disable decisions into PG (auto_disabled=true + reason)
// and re-renders the config so the entry drops out of selection. It returns
// nil when no PG upstream-provider store is configured, so the auth manager's
// SetAutoDisableSink is a no-op (mirroring SyncLogSink's contract).
func (h *Handler) AutoDisableSink() coreauth.AutoDisableSink {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	pg := h.pgUpstreamProviders
	h.mu.Unlock()
	if pg == nil {
		return nil // PG not configured: manager.SetAutoDisableSink(nil) no-op
	}
	upstream, ok := pg.(autoDisablePersister)
	if !ok {
		log.Warn("auto-disable: upstream store does not support entry flag persistence")
		return nil
	}
	return newAutoDisableSink(upstream, func() {
		// Fire-and-forget beyond the write: the re-render takes its own path
		// and must NOT hold h.mu (applyUpstreamProviders re-acquires it).
		h.applyUpstreamProviders(context.Background())
	})
}
