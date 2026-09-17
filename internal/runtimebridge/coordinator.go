package runtimebridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util/atomicfile"
)

// Plan is the in-memory bridge between the configsnapshot planner output
// and the file path. The production snapshot exposes MarshalYAML and
// configvalidation, so the bridge wires them through the same atomic-file
// helper used elsewhere in the repo.
type Plan struct {
	Marshal     func() ([]byte, error)
	ValidateCfg func() error
}

// MarshalYAML satisfies snapshotRenderer.
func (p *Plan) MarshalYAML() ([]byte, error) {
	if p == nil || p.Marshal == nil {
		return nil, errors.New("runtimebridge: nil plan")
	}
	return p.Marshal()
}

// Validate satisfies snapshotRenderer.
func (p *Plan) Validate() error {
	if p == nil || p.ValidateCfg == nil {
		return errors.New("runtimebridge: nil plan")
	}
	return p.ValidateCfg()
}

// AtomicWrite satisfies snapshotRenderer by delegating to the shared
// util/atomicfile helper. The bridge therefore never depends on a
// per-package atomic-write copy.
func (p *Plan) AtomicWrite(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}

// SnapshotRenderer is the contract Coordinator.ReloadLatest relies on to
// re-render bridge content from a fresh snapshot. The production snapshot
// exposes MarshalYAML via the Plan adapter; tests provide a fake.
//
// Exported because cmd/server/main.go wires the snapshot source callback at
// registration time and needs the named type.
type SnapshotRenderer interface {
	MarshalYAML() ([]byte, error)
	Validate() error
	AtomicWrite(path string, data []byte, mode os.FileMode) error
}

// renderTo renders the snapshot, validates it, and atomically writes it.
func renderTo(snap SnapshotRenderer, path string, mode os.FileMode) error {
	data, err := snap.MarshalYAML()
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	if err := snap.Validate(); err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}
	return snap.AtomicWrite(path, data, mode)
}

// Coordinator is the long-lived reloader the management API calls after
// each runtime_config commit. It holds a reference to the live bridge
// (when one exists) and re-renders the latest snapshot on demand.
//
// Phase 2 keeps the coordinator single-node: the management API runs
// in-process, so calling ReloadLatest after a successful commit is
// enough to refresh the bridge file. Multi-instance fan-out via
// LISTEN/NOTIFY is explicitly deferred.
type Coordinator struct {
	mu sync.Mutex
	// bridge is the live ephemeral bridge. ReloadLatest swaps the active
	// file in place when set; nil disables re-renders.
	bridge *Bridge
	// src produces a renderer per call. Invoked lazily so a re-import
	// stays transparent.
	src func(context.Context) (SnapshotRenderer, error)

	// Reload outcome state, recorded by ReloadLatest. The management API
	// surfaces this in the response so the dashboard can distinguish
	// "applied", "pending" (reload errored but revision committed), and
	// "skipped" (no bridge bound).
	lastReloadRevision int64
	lastReloadErr      error
	lastReloadAt       time.Time
}

// NewCoordinator returns a coordinator that re-renders using src. The
// src callback is the only thing the coordinator depends on at runtime:
// it is invoked per-call so a re-import or a re-resolve stays transparent.
func NewCoordinator(src func(context.Context) (SnapshotRenderer, error)) *Coordinator {
	return &Coordinator{src: src}
}

// Bind attaches the live bridge so ReloadLatest can swap the active file
// in place. Calling Bind(nil) detaches the bridge.
func (c *Coordinator) Bind(b *Bridge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bridge = b
}

// ReloadLatest re-renders the latest snapshot and atomically replaces the
// active bridge file. When no bridge is bound, ReloadLatest is a no-op.
//
// On success or failure the outcome (revision + err + time) is recorded
// for the management response shape; callers should read it via
// LastReloadStatus.
func (c *Coordinator) ReloadLatest(ctx context.Context) error {
	c.mu.Lock()
	b := c.bridge
	src := c.src
	c.mu.Unlock()
	if b == nil {
		// Mark as "skipped": no bridge, no error, no time recorded.
		c.recordReload(0, nil)
		return nil
	}
	if src == nil {
		err := errors.New("runtimebridge: ReloadLatest: no snapshot source")
		c.recordReload(0, err)
		return err
	}
	renderer, err := src(ctx)
	if err != nil {
		wrapped := fmt.Errorf("runtimebridge: load snapshot: %w", err)
		c.recordReload(0, wrapped)
		return wrapped
	}
	if renderer == nil {
		err := errors.New("runtimebridge: nil snapshot")
		c.recordReload(0, err)
		return err
	}
	renderErr := renderTo(renderer, b.configPath, 0o600)
	c.recordReload(0, renderErr)
	return renderErr
}

// recordReload stores the outcome of the most recent ReloadLatest call.
// revision is reserved for a future caller that knows which revision the
// reload was meant to apply; today the bridge re-renders from the latest
// snapshot, so callers should treat the timestamp as the source of truth
// for "did the reload fire?".
func (c *Coordinator) recordReload(revision int64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastReloadRevision = revision
	c.lastReloadErr = err
	c.lastReloadAt = time.Now()
}

// LastReloadStatus returns the outcome of the most recent ReloadLatest
// call. revision is 0 when no reload has fired yet; err is non-nil when
// the most recent reload failed (the committed revision is still in
// effect — the dashboard surfaces this as "pending"). at is the zero
// time when no reload has fired.
func (c *Coordinator) LastReloadStatus() (revision int64, err error, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastReloadRevision, c.lastReloadErr, c.lastReloadAt
}
