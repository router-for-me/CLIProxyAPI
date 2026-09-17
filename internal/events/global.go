package events

import (
	"sync/atomic"
)

// global is the process-wide Recorder. Set by SetGlobal during init,
// read by Global() at emission sites.
var global atomic.Pointer[Ring]

// SetGlobal installs the ring as the global recorder. Thread-safe.
// Subsequent calls REPLACE the previous ring; events buffered in the
// previous ring remain accessible via the old *Ring handle but are no
// longer visible to events.Global().
func SetGlobal(r *Ring) { global.Store(r) }

// Global returns the installed ring, or a no-op stub if SetGlobal hasn't
// been called. The no-op stub has Capacity() == 0 so callers can detect
// the uninitialized state if they care.
func Global() *Ring {
	if r := global.Load(); r != nil {
		return r
	}
	return &Ring{} // zero-value ring with cap=0; Record is a no-op
}

// Recorder is the interface emission sites depend on. Ring implements it.
type Recorder interface {
	Record(Event)
	Dropped() int64
	Capacity() int
	Snapshot() []Event
}

// Compile-time check that *Ring satisfies Recorder.
var _ Recorder = (*Ring)(nil)
