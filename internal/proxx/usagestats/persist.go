package usagestats

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const snapshotVersion = 1

type snapshot struct {
	Version int          `json:"version"`
	Buckets []Bucket     `json:"buckets"`
	Recent  []RecentItem `json:"recent"`
}

// Save writes the store to path atomically when it changed since the last save
// (or always when force is true).
func (s *Store) Save(path string, force bool) error {
	if path == "" {
		return nil
	}
	s.mu.Lock()
	if !s.dirty && !force {
		s.mu.Unlock()
		return nil
	}
	snap := snapshot{Version: snapshotVersion, Buckets: make([]Bucket, 0, len(s.buckets)), Recent: s.recentLocked()}
	for k, t := range s.buckets {
		snap.Buckets = append(snap.Buckets, Bucket{Day: k.day, Model: k.model, Provider: k.provider, APIKey: k.apiKey, Totals: *t})
	}
	s.dirty = false
	s.mu.Unlock()

	data, err := json.Marshal(snap)
	if err != nil {
		s.markDirty()
		return fmt.Errorf("encode usage snapshot: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.markDirty()
		return fmt.Errorf("create usage snapshot dir: %w", err)
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, data, 0o600); err != nil {
		s.markDirty()
		return fmt.Errorf("write usage snapshot: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		s.markDirty()
		return fmt.Errorf("replace usage snapshot: %w", err)
	}
	return nil
}

func (s *Store) markDirty() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

// Load merges a snapshot file into the store. A missing file is not an error.
func (s *Store) Load(path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read usage snapshot: %w", err)
	}
	var snap snapshot
	if err = json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("decode usage snapshot: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range snap.Buckets {
		key := bucketKey{day: b.Day, model: b.Model, provider: b.Provider, apiKey: b.APIKey}
		t := s.buckets[key]
		if t == nil {
			t = &Totals{}
			s.buckets[key] = t
		}
		t.add(b.Totals)
	}
	merged := append(snap.Recent, s.recentLocked()...)
	if len(merged) > s.recentLimit {
		merged = merged[len(merged)-s.recentLimit:]
	}
	s.recent = merged
	s.head = 0
	return nil
}
