package configsnapshot

import (
	"strings"
	"testing"
)

// buildConcurrencySnapshot returns a Snapshot whose Extra carries one
// openai-compatibility provider row with a per-entry MaxConcurrent/MaxWaitMs
// under api-key-entries and provider-level auto-disable fields, mirroring
// what MapResources would assemble for the upstream_providers row.
//
// Provider sections are not part of the scalarKeys projection; they travel
// through the Extra channel and the __extra envelope, so the round-trip
// tests exercise Extra here — that is where configsnapshot records them.
func buildConcurrencySnapshot(mc int) Snapshot {
	s := NewEmpty()
	s.Extra["openai-compatibility"] = []any{
		map[string]any{
			"name":                          "provider-a",
			"base-url":                      "https://openrouter.ai/api/v1",
			"auto-disable-error-codes":      []any{"401", "billing_not_active"},
			"auto-disable-cooldown-seconds": 3600,
			"api-key-entries": []any{
				map[string]any{
					"api-key":        "sk-xxx",
					"max-concurrent": mc,
					"max-wait-ms":    1500,
				},
			},
		},
	}
	return s
}

// TestConcurrencyFieldsRoundTripYAML pins that the per-entry
// max-concurrent/max-wait-ms and provider-level auto-disable fields survive
// the MarshalYAML -> UnmarshalYAML snapshot projection with exact values.
// This is the D7 round-trip guarantee: a snapshot that the dashboard/import
// path saves must reconstruct the identical provider shape.
func TestConcurrencyFieldsRoundTripYAML(t *testing.T) {
	snap := buildConcurrencySnapshot(3)

	out, err := MarshalYAML(&snap)
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	body := string(out)
	for _, want := range []string{
		"max-concurrent: 3",
		"max-wait-ms: 1500",
		"auto-disable-error-codes:",
		`- "401"`,
		"- billing_not_active",
		"auto-disable-cooldown-seconds: 3600",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in projected YAML:\n%s", want, body)
		}
	}

	var rt Snapshot
	if err := UnmarshalYAML(out, &rt); err != nil {
		t.Fatalf("UnmarshalYAML: %v", err)
	}
	providers, ok := rt.Extra["openai-compatibility"].([]any)
	if !ok || len(providers) != 1 {
		t.Fatalf("openai-compatibility did not survive round-trip: %#v", rt.Extra["openai-compatibility"])
	}
	p, ok := providers[0].(map[string]any)
	if !ok {
		t.Fatalf("provider not a map[string]any: %T", providers[0])
	}
	codes, ok := p["auto-disable-error-codes"].([]any)
	if !ok || len(codes) != 2 || codes[0] != "401" || codes[1] != "billing_not_active" {
		t.Fatalf("auto-disable-error-codes = %#v; want [401 billing_not_active]", p["auto-disable-error-codes"])
	}
	if p["auto-disable-cooldown-seconds"] != 3600 {
		t.Fatalf("auto-disable-cooldown-seconds = %v; want 3600", p["auto-disable-cooldown-seconds"])
	}
	entries, ok := p["api-key-entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("api-key-entries did not survive round-trip: %#v", p["api-key-entries"])
	}
	e, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("entry not a map[string]any: %T", entries[0])
	}
	if v, _ := e["max-concurrent"].(int); v != 3 {
		t.Fatalf("max-concurrent = %#v; want 3", e["max-concurrent"])
	}
	if v, _ := e["max-wait-ms"].(int); v != 1500 {
		t.Fatalf("max-wait-ms = %#v; want 1500", e["max-wait-ms"])
	}
}

// TestConcurrencyFieldsChecksumSensitivity pins plan D7's "config change
// alters plan identity": changing a per-entry MaxConcurrent (1 -> 2) on an
// otherwise-identical snapshot must change the snapshot's SHA-256 checksum,
// so the repository recognises the update as a distinct revision rather
// than a no-op dedup.
func TestConcurrencyFieldsChecksumSensitivity(t *testing.T) {
	a := buildConcurrencySnapshot(1)
	b := buildConcurrencySnapshot(2)

	ca, cb := a.Checksum(), b.Checksum()
	if ca == cb {
		t.Fatalf("MaxConcurrent 1 -> 2 must change the checksum; both %s", ca)
	}

	// Prove the only delta is the MaxConcurrent field: copying a.Extra and
	// bumping its max-concurrent to 2 must reproduce b.Extra exactly, so
	// the checksum pair is sensitive to the field alone. All other content
	// follows from the build helper on both sides.
	patched := deepCopyMap(a.Extra)
	patchedProviders := patched["openai-compatibility"].([]any)
	patchedProvider := patchedProviders[0].(map[string]any)
	patchedEntries := patchedProvider["api-key-entries"].([]any)
	patchedEntry := patchedEntries[0].(map[string]any)
	patchedEntry["max-concurrent"] = 2
	if !mapsDeepEqual(patched, b.Extra) {
		t.Fatal("test is broken: snapshots differ in more than MaxConcurrent")
	}
}

// deepCopyMap returns a structural copy of m so patching a nested value in
// a test cannot mutate the shared snapshot underneath.
func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyVal(v)
	}
	return out
}

func deepCopyVal(v any) any {
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = deepCopyVal(t[i])
		}
		return out
	case map[string]any:
		return deepCopyMap(t)
	default:
		return v
	}
}

// TestConcurrencyFieldsAutoDisableCodesChecksumSensitivity complements the
// MaxConcurrent case: changing the provider-level auto-disable-error-codes
// ordering/content must also alter the checksum. Plan D7 treats the
// auto-disable policy as part of the plan identity.
func TestConcurrencyFieldsAutoDisableCodesChecksumSensitivity(t *testing.T) {
	a := buildConcurrencySnapshot(1)
	b := buildConcurrencySnapshot(1)
	providers := b.Extra["openai-compatibility"].([]any)
	p := providers[0].(map[string]any)
	p["auto-disable-error-codes"] = []any{"403", "billing_not_active"}

	if a.Checksum() == b.Checksum() {
		t.Fatalf("auto-disable-error-codes change must alter checksum; both %s", a.Checksum())
	}
}

// mapsDeepEqual reports structural equality for map[string]any so a test
// can prove the only delta between two snapshots is the targeted field.
func mapsDeepEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !valsEqual(av, bv) {
			return false
		}
	}
	return true
}

func valsEqual(a, b any) bool {
	switch at := a.(type) {
	case []any:
		bt, ok := b.([]any)
		if !ok || len(at) != len(bt) {
			return false
		}
		for i := range at {
			if !valsEqual(at[i], bt[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bt, ok := b.(map[string]any)
		return ok && mapsDeepEqual(at, bt)
	default:
		return a == b
	}
}
