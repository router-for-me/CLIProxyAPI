package util

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// CookieSnapshotPath derives the cookie snapshot file path from the main token JSON path.
// It replaces the .json suffix with .cookies, or appends .cookies if missing.
func CookieSnapshotPath(mainPath string) string {
	if strings.HasSuffix(mainPath, ".json") {
		return strings.TrimSuffix(mainPath, ".json") + ".cookies"
	}
	return mainPath + ".cookies"
}

// IsRegularFile reports whether the given path exists and is a regular file.
func IsRegularFile(path string) bool {
	if path == "" {
		return false
	}
	if st, err := os.Stat(path); err == nil && !st.IsDir() {
		return true
	}
	return false
}

// ReadJSON reads and unmarshals a JSON file into v.
// Returns os.ErrNotExist if the file does not exist.
func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// WriteJSON marshals v as JSON and writes to path, creating parent directories as needed.
func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	return enc.Encode(v)
}

// RemoveFile removes the file if it exists.
func RemoveFile(path string) error {
	if IsRegularFile(path) {
		return os.Remove(path)
	}
	return nil
}

// TryReadCookieSnapshotInto tries to read a cookie snapshot into v.
// It attempts the .cookies suffix; returns (true, nil) when found and decoded,
// or (false, nil) when none exists.
func TryReadCookieSnapshotInto(mainPath string, v any) (bool, error) {
	snap := CookieSnapshotPath(mainPath)
	if err := ReadJSON(snap, v); err != nil {
		if err == os.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// WriteCookieSnapshot writes v to the snapshot path derived from mainPath using the .cookies suffix.
func WriteCookieSnapshot(mainPath string, v any) error {
	return WriteJSON(CookieSnapshotPath(mainPath), v)
}

// RemoveCookieSnapshots removes both modern and legacy snapshot files.
func RemoveCookieSnapshots(mainPath string) { _ = RemoveFile(CookieSnapshotPath(mainPath)) }

// Hooks provide customization points for snapshot lifecycle operations.
type Hooks[T any] struct {
	// Apply merges snapshot data into the in-memory store during Apply().
	// Defaults to overwriting the store with the snapshot contents.
	Apply func(store *T, snapshot *T)

	// Snapshot prepares the payload to persist during Persist().
	// Defaults to cloning the store value.
	Snapshot func(store *T) *T

	// Merge chooses which data to flush when a snapshot exists.
	// Defaults to using the snapshot payload as-is.
	Merge func(store *T, snapshot *T) *T

	// WriteMain persists the merged payload into the canonical token path.
	// Defaults to WriteJSON.
	WriteMain func(path string, data *T) error
}

// Manager orchestrates cookie snapshot lifecycle for token storages.
type Manager[T any] struct {
	mainPath string
	store    *T
	hooks    Hooks[T]
}

// NewManager constructs a Manager bound to mainPath and store.
func NewManager[T any](mainPath string, store *T, hooks Hooks[T]) *Manager[T] {
	return &Manager[T]{
		mainPath: mainPath,
		store:    store,
		hooks:    hooks,
	}
}

// Apply loads snapshot data into the in-memory store if available.
// Returns true when a snapshot was applied.
func (m *Manager[T]) Apply() (bool, error) {
	if m == nil || m.store == nil || m.mainPath == "" {
		return false, nil
	}
	var snapshot T
	ok, err := TryReadCookieSnapshotInto(m.mainPath, &snapshot)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if m.hooks.Apply != nil {
		m.hooks.Apply(m.store, &snapshot)
	} else {
		*m.store = snapshot
	}
	return true, nil
}

// Persist writes the current store state to the snapshot file.
func (m *Manager[T]) Persist() error {
	if m == nil || m.store == nil || m.mainPath == "" {
		return nil
	}
	var payload *T
	if m.hooks.Snapshot != nil {
		payload = m.hooks.Snapshot(m.store)
	} else {
		clone := new(T)
		*clone = *m.store
		payload = clone
	}
	return WriteCookieSnapshot(m.mainPath, payload)
}

// FlushOptions configure Flush behaviour.
type FlushOptions[T any] struct {
	Fallback func() *T
	Mutate   func(*T)
}

// FlushOption mutates FlushOptions.
type FlushOption[T any] func(*FlushOptions[T])

// WithFallback provides fallback payload when no snapshot exists.
func WithFallback[T any](fn func() *T) FlushOption[T] {
	return func(opts *FlushOptions[T]) { opts.Fallback = fn }
}

// WithMutate allows last-minute mutation of the payload before writing main file.
func WithMutate[T any](fn func(*T)) FlushOption[T] {
	return func(opts *FlushOptions[T]) { opts.Mutate = fn }
}

// Flush commits snapshot (or fallback) into the main token file and removes the snapshot.
func (m *Manager[T]) Flush(options ...FlushOption[T]) error {
	if m == nil || m.mainPath == "" {
		return nil
	}
	cfg := FlushOptions[T]{}
	for _, opt := range options {
		if opt != nil {
			opt(&cfg)
		}
	}
	var snapshot T
	ok, err := TryReadCookieSnapshotInto(m.mainPath, &snapshot)
	if err != nil {
		return err
	}
	var payload *T
	if ok {
		if m.hooks.Merge != nil {
			payload = m.hooks.Merge(m.store, &snapshot)
		} else {
			payload = &snapshot
		}
	} else if cfg.Fallback != nil {
		payload = cfg.Fallback()
	} else if m.store != nil {
		payload = m.store
	}
	if payload == nil {
		return RemoveFile(CookieSnapshotPath(m.mainPath))
	}
	if cfg.Mutate != nil {
		cfg.Mutate(payload)
	}
	if m.hooks.WriteMain != nil {
		if err := m.hooks.WriteMain(m.mainPath, payload); err != nil {
			return err
		}
	} else {
		if err := WriteJSON(m.mainPath, payload); err != nil {
			return err
		}
	}
	RemoveCookieSnapshots(m.mainPath)
	return nil
}
