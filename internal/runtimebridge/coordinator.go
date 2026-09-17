package runtimebridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

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
	mu     sync.Mutex
	bridge *Bridge
	src    func(context.Context) (SnapshotRenderer, error)
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
func (c *Coordinator) ReloadLatest(ctx context.Context) error {
	c.mu.Lock()
	b := c.bridge
	src := c.src
	c.mu.Unlock()
	if b == nil {
		return nil
	}
	if src == nil {
		return errors.New("runtimebridge: ReloadLatest: no snapshot source")
	}
	renderer, err := src(ctx)
	if err != nil {
		return fmt.Errorf("runtimebridge: load snapshot: %w", err)
	}
	if renderer == nil {
		return errors.New("runtimebridge: nil snapshot")
	}
	return renderTo(renderer, b.configPath, 0o600)
}
