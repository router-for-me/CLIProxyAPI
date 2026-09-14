package configsnapshot

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestMarshalYAMLEmitsPortAndRouting verifies that MarshalYAML preserves
// hyphenated nested keys (e.g. cooldown-wait/max-wait-ms) using the YAML
// literal names declared by config.RoutingConfig/CooldownWaitConfig. The
// snapshot is a generic map; output must surface YAML field names that
// match the existing config parser rather than Go field names.
func TestMarshalYAMLEmitsPortAndRouting(t *testing.T) {
	snap := NewEmpty()
	snap.Settings["port"] = 8317
	snap.Settings["routing"] = map[string]any{
		"strategy": "round-robin",
		"cooldown-wait": map[string]any{
			"max-wait-ms": 15000,
		},
	}
	out, err := MarshalYAML(&snap)
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	body := string(out)
	if !strings.Contains(body, "port: 8317") {
		t.Fatalf("expected port: 8317 in YAML, got:\n%s", body)
	}
	if !strings.Contains(body, "max-wait-ms: 15000") {
		t.Fatalf("expected max-wait-ms: 15000 in YAML, got:\n%s", body)
	}
	if !strings.Contains(body, "cooldown-wait") {
		t.Fatalf("expected cooldown-wait in YAML, got:\n%s", body)
	}
}

// TestUnmarshalYAMLHydratesKnownKeys verifies UnmarshalYAML copies
// allowlisted top-level keys into Settings and leaves them accessible via
// Settings. It also asserts non-nil Settings/Extra so callers never see a
// nil map.
func TestUnmarshalYAMLHydratesKnownKeys(t *testing.T) {
	raw := []byte("port: 9000\nrouting:\n  strategy: round-robin\n")
	var snap Snapshot
	if err := UnmarshalYAML(raw, &snap); err != nil {
		t.Fatalf("UnmarshalYAML: %v", err)
	}
	if snap.Settings == nil {
		t.Fatal("Settings must be non-nil after UnmarshalYAML")
	}
	if snap.Extra == nil {
		t.Fatal("Extra must be non-nil after UnmarshalYAML")
	}
	got, ok := snap.Settings["port"]
	if !ok {
		t.Fatalf("port missing from Settings; have %v", snap.Settings)
	}
	// yaml.v3 decodes scalars into interface{} values; integers come through
	// as int. Assert the numeric value rather than the concrete type so
	// downstream encoders are not chained to one encoding library.
	if n, _ := got.(int); n != 9000 {
		t.Fatalf("port = %v (%T), want 9000", got, got)
	}
	if _, ok := snap.Settings["routing"]; !ok {
		t.Fatalf("routing missing from Settings; have %v", snap.Settings)
	}
}

// TestUnmarshalYAMLDivertsUnknownToExtra verifies that top-level keys not
// present in the Phase 1 projection end up in Extra rather than being
// silently dropped. Extra is the forward-compatibility channel.
func TestUnmarshalYAMLDivertsUnknownToExtra(t *testing.T) {
	raw := []byte("port: 9000\nfuture-feature: enabled\n")
	var snap Snapshot
	if err := UnmarshalYAML(raw, &snap); err != nil {
		t.Fatalf("UnmarshalYAML: %v", err)
	}
	if _, ok := snap.Settings["future-feature"]; ok {
		t.Fatalf("future-feature must not enter Settings: %v", snap.Settings)
	}
	if v, ok := snap.Extra["future-feature"]; !ok || v != "enabled" {
		t.Fatalf("future-feature must land in Extra=%v", snap.Extra)
	}
}

// TestExtraRoundTrip verifies that the Extra channel survives an
// UnmarshalYAML -> MarshalYAML round-trip. Operators can stash unmodeled
// fields and not lose them on export.
func TestExtraRoundTrip(t *testing.T) {
	raw := []byte("port: 9000\nforward-key: hello\nnested-extra:\n  inner: 7\n")
	var snap Snapshot
	if err := UnmarshalYAML(raw, &snap); err != nil {
		t.Fatalf("UnmarshalYAML: %v", err)
	}
	out, err := MarshalYAML(&snap)
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	body := string(out)
	if !strings.Contains(body, "forward-key") {
		t.Fatalf("forward-key missing from output:\n%s", body)
	}
	if !strings.Contains(body, "nested-extra") {
		t.Fatalf("nested-extra missing from output:\n%s", body)
	}
	if !strings.Contains(body, "inner: 7") {
		t.Fatalf("nested-extra.inner=7 missing from output:\n%s", body)
	}
}

// TestMarshalYAMLDeterministicRepeated verifies MarshalYAML is stable
// across repeated calls. yaml.v3 map encoding is unordered; the
// implementation must use a representation that doesn't depend on
// time/randomness/Go map iteration order.
func TestMarshalYAMLDeterministicRepeated(t *testing.T) {
	snap := NewEmpty()
	snap.Settings["port"] = 8317
	snap.Settings["host"] = "0.0.0.0"
	snap.Settings["debug"] = true
	snap.Settings["routing"] = map[string]any{
		"strategy":       "round-robin",
		"max-attempts":   3,
		"reclassify-403": true,
	}

	first, err := MarshalYAML(&snap)
	if err != nil {
		t.Fatalf("first MarshalYAML: %v", err)
	}
	for i := 0; i < 10; i++ {
		again, err := MarshalYAML(&snap)
		if err != nil {
			t.Fatalf("MarshalYAML iter %d: %v", i, err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("MarshalYAML output changed across calls:\nfirst:\n%s\nlater:\n%s", first, again)
		}
	}
}

// TestMarshalYAMLNilSnapshotErrors verifies explicit nil-snapshot
// rejection rather than panic, so callers cannot silently corrupt output.
func TestMarshalYAMLNilSnapshotErrors(t *testing.T) {
	_, err := MarshalYAML(nil)
	if err == nil {
		t.Fatal("MarshalYAML(nil) should return an error")
	}
}

// TestUnmarshalYAMLNilTargetErrors verifies explicit nil-target rejection
// so callers cannot accidentally write through a nil pointer.
func TestUnmarshalYAMLNilTargetErrors(t *testing.T) {
	err := UnmarshalYAML([]byte("port: 8317\n"), nil)
	if err == nil {
		t.Fatal("UnmarshalYAML(nil target) should return an error")
	}
}

// TestUnmarshalYAMLMalformedErrors verifies malformed YAML produces a
// contextual error rather than silent failure. yaml.v3 returns a typed
// *yaml.TypeError for shape issues and a generic error for syntax; we
// surface either.
func TestUnmarshalYAMLMalformedErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{"unbalanced", []byte("port: [8317\n")},
		{"tab-indent", []byte("\tport: 8317\n")},
		{"mixed-tab", []byte("port:\n\t8317: bad\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var snap Snapshot
			err := UnmarshalYAML(tc.raw, &snap)
			if err == nil {
				t.Fatalf("expected error for malformed YAML %q", tc.raw)
			}
			if !strings.Contains(err.Error(), "configsnapshot") {
				t.Fatalf("error should be tagged configsnapshot; got %v", err)
			}
		})
	}
}

// TestUnmarshalYAMLEmptyWhitespaceRejection matches
// config.ParseConfigBytes which rejects empty payloads. Phase 1 keeps the
// empty-input contract identical so the YAML layer and the parser layer
// agree.
func TestUnmarshalYAMLEmptyWhitespaceRejection(t *testing.T) {
	cases := [][]byte{nil, {}, []byte("   \n  \t  \n")}
	for i, raw := range cases {
		var snap Snapshot
		err := UnmarshalYAML(raw, &snap)
		if err == nil {
			t.Fatalf("case %d: expected error on empty/whitespace YAML", i)
		}
	}
}

// TestMarshalYAMLNestedMapListPreservation exercises the recursive shape
// conversion. Nested maps and lists must appear in the YAML output so
// complex Settings values (e.g. routing entries, claude API key arrays)
// survive a round-trip without lossy scalarisation.
func TestMarshalYAMLNestedMapListPreservation(t *testing.T) {
	snap := NewEmpty()
	snap.Settings["routing"] = map[string]any{
		"strategy": "round-robin",
		"cooldown-wait": map[string]any{
			"max-wait-ms":  15000,
			"max-attempts": 3,
		},
		"session-affinity-ttl": "5m",
	}
	snap.Settings["payload"] = map[string]any{
		"override": []any{
			map[string]any{
				"name":   "rule1",
				"path":   "$.messages[0]",
				"params": map[string]any{"k": "v"},
			},
		},
	}
	snap.Settings["oauth-excluded-models"] = map[string]any{
		"claude": []any{"claude-3-opus", "claude-3-sonnet"},
	}
	out, err := MarshalYAML(&snap)
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	body := string(out)
	for _, want := range []string{
		"cooldown-wait",
		"max-wait-ms: 15000",
		"max-attempts: 3",
		"session-affinity-ttl",
		"override",
		"rule1",
		"oauth-excluded-models",
		"claude",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in output:\n%s", want, body)
		}
	}
}

// TestMarshalYAMLEmitsTrailingNewline verifies the output ends with a
// newline (POSIX text file convention) so that operators can `cat` exported
// YAML without an extra `echo`. LF only — Go's yaml.v3 encoder writes LF.
func TestMarshalYAMLEmitsTrailingNewline(t *testing.T) {
	snap := NewEmpty()
	snap.Settings["port"] = 8317
	out, err := MarshalYAML(&snap)
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	if !bytes.HasSuffix(out, []byte{'\n'}) {
		t.Fatalf("MarshalYAML output must end with newline, got: %q", out)
	}
	if bytes.Contains(out, []byte{'\r'}) {
		t.Fatalf("MarshalYAML output must use LF line endings")
	}
}

// TestUnmarshalYAMLAllowsNilInputToInitialize ensures the contract for
// callers that want an empty-but-non-nil snapshot. The caller-supplied
// *Snapshot may already have nil maps; the function must initialize them
// rather than pass nil back to repo code that types assert and range.
func TestUnmarshalYAMLAllowsNilInputToInitialize(t *testing.T) {
	var snap Snapshot // zero value, all maps nil
	if err := UnmarshalYAML([]byte("port: 8317\n"), &snap); err != nil {
		t.Fatalf("UnmarshalYAML: %v", err)
	}
	if snap.Settings == nil {
		t.Fatal("Settings must be initialized non-nil even when caller passed zero-value Snapshot")
	}
	if snap.Extra == nil {
		t.Fatal("Extra must be initialized non-nil even when caller passed zero-value Snapshot")
	}
}

// TestUnmarshalYAMLMalformedErrorType checks that malformed-input errors
// are non-nil and distinguishable by callers (e.g. callers can use
// errors.Is if they wrap further).
func TestUnmarshalYAMLMalformedErrorType(t *testing.T) {
	var snap Snapshot
	err := UnmarshalYAML([]byte("port: [unclosed\n"), &snap)
	if err == nil {
		t.Fatal("expected error for unbalanced YAML")
	}
	// Malformed errors should be wrapped and tagged. Don't pin the error
	// type because yaml.v3 returns either an internal *yaml.TypeError or
	// a wrapped error depending on the failure mode.
	if errors.Unwrap(err) == nil && !strings.Contains(err.Error(), "yaml") {
		t.Fatalf("error should mention yaml; got %v", err)
	}
}
