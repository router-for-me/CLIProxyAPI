package configsnapshot

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// TestNewEmptyDefaults asserts NewEmpty returns a Snapshot with non-nil
// maps, empty ResourceRefs, Revision 0, UpdatedSource "system", zero
// UpdatedAt, and empty UpdatedBy. The values must be safe to range over
// and pass to JSON encoders without special-casing nil.
func TestNewEmptyDefaults(t *testing.T) {
	s := NewEmpty()

	if s.Settings == nil {
		t.Fatal("Settings must be non-nil for callers that range over it")
	}
	if s.Extra == nil {
		t.Fatal("Extra must be non-nil for callers that range over it")
	}
	if s.ResourceRefs == nil {
		t.Fatal("ResourceRefs must be non-nil so json.Marshal emits [] not null")
	}
	if got := len(s.ResourceRefs); got != 0 {
		t.Fatalf("ResourceRefs length: got %d, want 0", got)
	}
	if s.Revision != 0 {
		t.Fatalf("Revision: got %d, want 0", s.Revision)
	}
	if s.UpdatedSource != "system" {
		t.Fatalf("UpdatedSource: got %q, want %q", s.UpdatedSource, "system")
	}
	if !s.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt: got %v, want zero time", s.UpdatedAt)
	}
	if s.UpdatedBy != "" {
		t.Fatalf("UpdatedBy: got %q, want empty string", s.UpdatedBy)
	}

	// Mutate returned maps and confirm the next call still produces
	// independent defaults.
	s.Settings["k"] = "v"
	s.Extra["k"] = "v"
	again := NewEmpty()
	if _, ok := again.Settings["k"]; ok {
		t.Fatal("NewEmpty must return independent maps; mutation leaked")
	}
	if _, ok := again.Extra["k"]; ok {
		t.Fatal("NewEmpty must return independent maps; mutation leaked")
	}
}

// TestEmptyChecksumDeterminism asserts Checksum is a 64-char hex SHA-256
// string and that two NewEmpty snapshots produce the same digest.
func TestEmptyChecksumDeterminism(t *testing.T) {
	a := NewEmpty()
	b := NewEmpty()

	got := a.Checksum()
	if len(got) != 64 {
		t.Fatalf("Checksum length: got %d, want 64 (SHA-256 hex)", len(got))
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("Checksum is not valid hex: %v", err)
	}
	if strings.ToLower(got) != got {
		t.Fatalf("Checksum should be lowercase hex: %q", got)
	}
	if a.Checksum() != b.Checksum() {
		t.Fatalf("two NewEmpty snapshots must have identical checksums: %s vs %s", a.Checksum(), b.Checksum())
	}
}

// TestChecksumKeyOrderIndependence asserts that inserting Settings keys in
// different orders yields the same digest. This is the core property the
// snapshot relies on for revision dedup and conflict detection.
func TestChecksumKeyOrderIndependence(t *testing.T) {
	a := NewEmpty()
	a.Settings["host"] = "0.0.0.0"
	a.Settings["port"] = 8080
	a.Settings["debug"] = true

	b := NewEmpty()
	b.Settings["debug"] = true
	b.Settings["port"] = 8080
	b.Settings["host"] = "0.0.0.0"

	if a.Checksum() != b.Checksum() {
		t.Fatalf("top-level key order changed digest: %s vs %s", a.Checksum(), b.Checksum())
	}

	// Nested-map independence: Extra keys also sort.
	c := NewEmpty()
	c.Extra["outer1"] = map[string]any{"z": 1, "a": 2, "m": 3}
	d := NewEmpty()
	d.Extra["outer1"] = map[string]any{"m": 3, "a": 2, "z": 1}

	if c.Checksum() != d.Checksum() {
		t.Fatalf("nested map key order changed digest: %s vs %s", c.Checksum(), d.Checksum())
	}
}

// TestChecksumDeeplyNestedKeyOrderIndependence exercises a map with maps
// inside maps inside maps, confirming recursive sorting survives depth.
func TestChecksumDeeplyNestedKeyOrderIndependence(t *testing.T) {
	build := func() Snapshot {
		s := NewEmpty()
		s.Settings["server"] = map[string]any{
			"tls": map[string]any{
				"cert": "/etc/cert.pem",
				"key":  "/etc/key.pem",
				"min":  "1.2",
			},
			"bind": map[string]any{
				"host": "0.0.0.0",
				"port": 8080,
			},
		}
		return s
	}
	a := build()
	b := build()
	if a.Checksum() != b.Checksum() {
		t.Fatalf("identical input should match: %s vs %s", a.Checksum(), b.Checksum())
	}
}

// TestChecksumSliceOrderSensitivity asserts ResourceRefs order affects the
// digest. Slices are semantically ordered (resource identity), so
// reordering them must change the checksum.
func TestChecksumSliceOrderSensitivity(t *testing.T) {
	a := NewEmpty()
	a.ResourceRefs = []ResourceRef{
		{Kind: "upstream_provider", ID: "1", StableKey: "alpha"},
		{Kind: "upstream_provider", ID: "2", StableKey: "beta"},
	}

	b := NewEmpty()
	b.ResourceRefs = []ResourceRef{
		{Kind: "upstream_provider", ID: "2", StableKey: "beta"},
		{Kind: "upstream_provider", ID: "1", StableKey: "alpha"},
	}

	if a.Checksum() == b.Checksum() {
		t.Fatalf("slice reorder must change digest, both: %s", a.Checksum())
	}
}

// TestChecksumProjectionSliceOrderSensitivity asserts that the slice inside
// a ResourceRef.Projection also preserves order. This protects future
// projections that carry ordered fields (e.g. weighted entries).
func TestChecksumProjectionSliceOrderSensitivity(t *testing.T) {
	a := NewEmpty()
	a.ResourceRefs = []ResourceRef{{
		Kind:      "proxy_pool",
		ID:        "p1",
		StableKey: "p1",
		Projection: map[string]any{
			"priorities": []any{1, 2, 3},
		},
	}}
	b := NewEmpty()
	b.ResourceRefs = []ResourceRef{{
		Kind:      "proxy_pool",
		ID:        "p1",
		StableKey: "p1",
		Projection: map[string]any{
			"priorities": []any{3, 2, 1},
		},
	}}

	if a.Checksum() == b.Checksum() {
		t.Fatalf("projection slice reorder must change digest, both: %s", a.Checksum())
	}
}

// TestChecksumProjectionKeyOrderIndependence asserts that within a
// ResourceRef.Projection, map keys are sorted just like top-level keys.
func TestChecksumProjectionKeyOrderIndependence(t *testing.T) {
	a := NewEmpty()
	a.ResourceRefs = []ResourceRef{{
		Kind:      "upstream_provider",
		ID:        "u1",
		StableKey: "u1",
		Projection: map[string]any{
			"disabled": false,
			"name":     "u1",
			"weight":   5,
		},
	}}
	b := NewEmpty()
	b.ResourceRefs = []ResourceRef{{
		Kind:      "upstream_provider",
		ID:        "u1",
		StableKey: "u1",
		Projection: map[string]any{
			"weight":   5,
			"name":     "u1",
			"disabled": false,
		},
	}}

	if a.Checksum() != b.Checksum() {
		t.Fatalf("projection map key order changed digest: %s vs %s", a.Checksum(), b.Checksum())
	}
}

// TestChecksumExcludesRevisionAndUpdatedAt asserts that changing Revision
// and UpdatedAt alone does not change the digest. These are metadata, not
// part of the canonical content.
func TestChecksumExcludesRevisionAndUpdatedAt(t *testing.T) {
	base := Snapshot{
		Settings:      map[string]any{"port": 8080},
		Extra:         map[string]any{},
		ResourceRefs:  []ResourceRef{},
		UpdatedBy:     "alice",
		UpdatedSource: "dashboard",
	}

	variants := []Snapshot{
		func() (s Snapshot) {
			s = cloneSnapshot(base)
			s.Revision = 0
			s.UpdatedAt = time.Time{}
			return
		}(),
		func() (s Snapshot) {
			s = cloneSnapshot(base)
			s.Revision = 1
			s.UpdatedAt = time.Unix(1700000000, 0).UTC()
			return
		}(),
		func() (s Snapshot) {
			s = cloneSnapshot(base)
			s.Revision = 42
			s.UpdatedAt = time.Unix(1, 2).UTC()
			return
		}(),
		func() (s Snapshot) {
			s = cloneSnapshot(base)
			s.Revision = 9999999
			s.UpdatedAt = time.Date(2099, 1, 2, 3, 4, 5, 6, time.UTC)
			return
		}(),
	}

	first := variants[0].Checksum()
	for i, v := range variants {
		if v.Checksum() != first {
			t.Fatalf("variant %d checksum differs (Revision/UpdatedAt leaked):\n  first=%s\n  got  =%s", i, first, v.Checksum())
		}
	}
}

// TestChecksumIncludesActorAndSource asserts UpdatedBy and UpdatedSource
// participate in the digest. Two revisions produced by different actors or
// channels must compare unequal.
func TestChecksumIncludesActorAndSource(t *testing.T) {
	base := func() Snapshot {
		s := NewEmpty()
		s.Settings["port"] = 8080
		return s
	}

	a := base()
	a.UpdatedBy = "alice"
	a.UpdatedSource = "dashboard"

	b := base()
	b.UpdatedBy = "bob"
	b.UpdatedSource = "dashboard"

	c := base()
	c.UpdatedBy = "alice"
	c.UpdatedSource = "cli-import"

	if a.Checksum() == b.Checksum() {
		t.Fatalf("UpdatedBy change must affect digest, both: %s", a.Checksum())
	}
	if a.Checksum() == c.Checksum() {
		t.Fatalf("UpdatedSource change must affect digest, both: %s", a.Checksum())
	}
}

// TestChecksumNilSafe asserts Checksum does not panic and returns a valid
// hex SHA-256 when called on a zero-value Snapshot whose maps are nil.
func TestChecksumNilSafe(t *testing.T) {
	var s Snapshot
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Checksum panicked on zero-value Snapshot: %v", r)
		}
	}()
	got := s.Checksum()
	if len(got) != 64 {
		t.Fatalf("Checksum length: got %d, want 64", len(got))
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("Checksum not hex: %v", err)
	}
}

// TestChecksumContentChange asserts that mutating Settings content (not
// keys) changes the digest. This is the basic soundness property.
func TestChecksumContentChange(t *testing.T) {
	a := NewEmpty()
	a.Settings["port"] = 8080
	b := NewEmpty()
	b.Settings["port"] = 9090

	if a.Checksum() == b.Checksum() {
		t.Fatalf("different Settings content must change digest, both: %s", a.Checksum())
	}
}

// cloneSnapshot returns a deep-enough copy of s for tests that vary only
// metadata fields. It rebuilds the top-level maps so the copy is fully
// independent of the source.
func cloneSnapshot(s Snapshot) Snapshot {
	out := Snapshot{
		Revision:      s.Revision,
		UpdatedAt:     s.UpdatedAt,
		UpdatedBy:     s.UpdatedBy,
		UpdatedSource: s.UpdatedSource,
	}
	if s.Settings == nil {
		out.Settings = nil
	} else {
		out.Settings = make(map[string]any, len(s.Settings))
		for k, v := range s.Settings {
			out.Settings[k] = v
		}
	}
	if s.Extra == nil {
		out.Extra = nil
	} else {
		out.Extra = make(map[string]any, len(s.Extra))
		for k, v := range s.Extra {
			out.Extra[k] = v
		}
	}
	if s.ResourceRefs == nil {
		out.ResourceRefs = nil
	} else {
		out.ResourceRefs = make([]ResourceRef, len(s.ResourceRefs))
		copy(out.ResourceRefs, s.ResourceRefs)
	}
	return out
}
