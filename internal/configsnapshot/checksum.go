package configsnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Checksum returns a deterministic hex-encoded SHA-256 digest of the
// canonical content of s.
//
// Canonicalization rules:
//
//   - Revision and UpdatedAt are excluded; they are metadata that the
//     repository assigns, not part of the snapshot's identity.
//   - Settings, Extra, ResourceRefs, UpdatedBy, and UpdatedSource are
//     included.
//   - Map keys are sorted recursively at every level so two Snapshots with
//     logically equal content but different map iteration order yield the
//     same digest.
//   - Slice order is preserved. ResourceRefs and Projection slices are
//     semantically ordered, so reordering them changes the digest.
//
// Checksum is safe to call on a zero-value Snapshot. It does not panic on
// nil map fields; they canonicalize as JSON null which is stable across
// calls.
func (s Snapshot) Checksum() string {
	canonical := canonicalize(s)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// canonicalize returns the deterministic JSON encoding used as the input
// to the checksum digest. The top-level payload is a map[string]any so
// keys at that level sort via the recursive marshalSorted path; nested
// maps in Settings and Extra therefore also sort.
func canonicalize(s Snapshot) []byte {
	payload := map[string]any{
		"extra":          s.Extra,
		"resource_refs":  s.ResourceRefs,
		"settings":       s.Settings,
		"updated_by":     s.UpdatedBy,
		"updated_source": s.UpdatedSource,
	}
	return marshalSorted(payload)
}

// marshalSorted encodes v as JSON with all map keys recursively sorted.
// Slices (including ResourceRefs and Projection slices) preserve their
// original order. nil maps are encoded as JSON null so the digest remains
// stable for zero-value snapshots.
func marshalSorted(v any) []byte {
	switch t := v.(type) {
	case nil:
		return []byte("null")
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf := []byte{'{'}
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				// json.Marshal on a string cannot fail; preserve the
				// quoted form rather than panicking.
				kb = append(append([]byte{'"'}, []byte(k)...), '"')
			}
			buf = append(buf, kb...)
			buf = append(buf, ':')
			buf = append(buf, marshalSorted(t[k])...)
		}
		buf = append(buf, '}')
		return buf
	case []ResourceRef:
		buf := []byte{'['}
		for i, r := range t {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = append(buf, marshalResourceRef(r)...)
		}
		buf = append(buf, ']')
		return buf
	case []any:
		buf := []byte{'['}
		for i, item := range t {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = append(buf, marshalSorted(item)...)
		}
		buf = append(buf, ']')
		return buf
	default:
		b, err := json.Marshal(t)
		if err != nil {
			// Fall back to a deterministic string form so the digest does
			// not panic on unencodable types.
			b, _ = json.Marshal(stringify(t))
		}
		return b
	}
}

// marshalResourceRef renders a ResourceRef as canonical JSON with fields
// emitted in a fixed order so the encoding is deterministic regardless of
// struct field declaration order.
func marshalResourceRef(r ResourceRef) []byte {
	buf := []byte{'{'}
	buf = append(buf, `"id":`...)
	buf = append(buf, marshalSorted(r.ID)...)
	buf = append(buf, `,"kind":`...)
	buf = append(buf, marshalSorted(r.Kind)...)
	buf = append(buf, `,"projection":`...)
	buf = append(buf, marshalSorted(r.Projection)...)
	buf = append(buf, `,"stable_key":`...)
	buf = append(buf, marshalSorted(r.StableKey)...)
	buf = append(buf, '}')
	return buf
}

// stringify returns a stable string form for v when json.Marshal cannot
// encode it. It exists solely to keep the checksum path panic-free.
func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
