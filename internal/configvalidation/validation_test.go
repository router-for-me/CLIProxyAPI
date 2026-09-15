package configvalidation_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configvalidation"
)

// TestValidateNilSnapshot asserts that a nil Snapshot is rejected with a
// contextual error prefixed by "configvalidation:" and matching the
// exported ErrNilSnapshot sentinel so callers can discriminate the
// missing-snapshot class via errors.Is.
func TestValidateNilSnapshot(t *testing.T) {
	cfg, err := configvalidation.Validate(nil)
	if err == nil {
		t.Fatal("Validate(nil) error = nil; want contextual error")
	}
	if cfg != nil {
		t.Errorf("Validate(nil) cfg = %+v; want nil", cfg)
	}
	if !strings.HasPrefix(err.Error(), "configvalidation:") {
		t.Errorf("Validate(nil) error prefix = %q; want %q", err.Error(), "configvalidation:")
	}
	if !errors.Is(err, configvalidation.ErrNilSnapshot) {
		t.Errorf("Validate(nil) error = %v; want errors.Is(err, configvalidation.ErrNilSnapshot)", err)
	}
}

// TestValidateEmptySnapshot asserts that an empty (NewEmpty) snapshot
// produces a non-nil Config with ParseConfigBytes defaults (e.g.
// WebsocketAuth true). NewEmpty returns a Snapshot value, so we take its
// address when calling Validate.
func TestValidateEmptySnapshot(t *testing.T) {
	snap := configsnapshot.NewEmpty()
	cfg, err := configvalidation.Validate(&snap)
	if err != nil {
		t.Fatalf("Validate(NewEmpty) error = %v", err)
	}
	if cfg == nil {
		t.Fatal("Validate(NewEmpty) cfg = nil; want non-nil Config with defaults")
	}
	if !cfg.WebsocketAuth {
		t.Errorf("cfg.WebsocketAuth = false; want true (ParseConfigBytes default)")
	}
	if cfg.RedisUsageQueueRetentionSeconds != 60 {
		t.Errorf("cfg.RedisUsageQueueRetentionSeconds = %d; want 60 (ParseConfigBytes default)", cfg.RedisUsageQueueRetentionSeconds)
	}
}

// TestValidateValidSettings asserts that a snapshot whose Settings map
// contains the scalar projection entries (port + nested routing.cooldown-wait
// + max-wait-ms) survives the YAML round-trip and lands in the typed Config
// fields. The Settings map mirrors the YAML tag spelling so the snapshot
// projection matches what MarshalYAML emits.
func TestValidateValidSettings(t *testing.T) {
	snap := configsnapshot.NewEmpty()
	snap.Settings["port"] = 9000
	snap.Settings["routing"] = map[string]any{
		"cooldown-wait": map[string]any{
			"max-wait-ms":    15000,
			"max-attempts":   3,
			"reclassify-403": true,
		},
	}

	cfg, err := configvalidation.Validate(&snap)
	if err != nil {
		t.Fatalf("Validate(valid settings) error = %v", err)
	}
	if cfg == nil {
		t.Fatal("Validate(valid settings) cfg = nil")
	}
	if cfg.Port != 9000 {
		t.Errorf("cfg.Port = %d; want 9000", cfg.Port)
	}
	if cfg.Routing.CooldownWait.MaxWaitMS != 15000 {
		t.Errorf("cfg.Routing.CooldownWait.MaxWaitMS = %d; want 15000", cfg.Routing.CooldownWait.MaxWaitMS)
	}
	if cfg.Routing.CooldownWait.MaxAttempts != 3 {
		t.Errorf("cfg.Routing.CooldownWait.MaxAttempts = %d; want 3", cfg.Routing.CooldownWait.MaxAttempts)
	}
	if !cfg.Routing.CooldownWait.Reclassify403 {
		t.Errorf("cfg.Routing.CooldownWait.Reclassify403 = false; want true")
	}
}

// TestValidateRejectsInvalidValues asserts that values ParseConfigBytes
// rejects propagate through the validation pipeline. The cheapest reliable
// rejection is to set credential-in-flight.snapshot-interval to a string
// that time.ParseDuration cannot decode.
func TestValidateRejectsInvalidValues(t *testing.T) {
	snap := configsnapshot.NewEmpty()
	snap.Settings["credential-in-flight"] = map[string]any{
		"snapshot-interval": "definitely-not-a-duration",
	}

	_, err := configvalidation.Validate(&snap)
	if err == nil {
		t.Fatal("Validate(invalid settings) error = nil; want ParseConfigBytes-rejected error")
	}
	if !strings.HasPrefix(err.Error(), "configvalidation:") {
		t.Errorf("Validate(invalid settings) error prefix = %q; want %q", err.Error(), "configvalidation:")
	}
}

// TestValidateDoesNotMutateInput asserts that Validate does not mutate the
// input Snapshot. Calling Validate twice on the same Snapshot must produce
// equal Config values and leave the input Snapshot structurally identical
// across Settings, Extra, and a populated ResourceRefs slice.
func TestValidateDoesNotMutateInput(t *testing.T) {
	snap := configsnapshot.NewEmpty()
	snap.Settings["port"] = 9000
	snap.Settings["routing"] = map[string]any{
		"cooldown-wait": map[string]any{
			"max-wait-ms": 15000,
		},
	}
	snap.Extra["future-section"] = map[string]any{
		"flag": true,
	}
	snap.ResourceRefs = append(snap.ResourceRefs, configsnapshot.ResourceRef{
		Kind:      "upstream_provider",
		ID:        "abc-123",
		StableKey: "primary",
		Projection: map[string]any{
			"name":     "primary",
			"disabled": false,
		},
	})

	// Snapshot the input shape so we can detect any mutation Validate might
	// perform through the YAML round-trip.
	originalSettings := snapcopy(snap.Settings)
	originalExtra := snapcopy(snap.Extra)
	originalRefs := append([]configsnapshot.ResourceRef(nil), snap.ResourceRefs...)

	cfg1, err := configvalidation.Validate(&snap)
	if err != nil {
		t.Fatalf("Validate(first call) error = %v", err)
	}
	cfg2, err := configvalidation.Validate(&snap)
	if err != nil {
		t.Fatalf("Validate(second call) error = %v", err)
	}
	if cfg1.Port != cfg2.Port || cfg1.Routing.CooldownWait.MaxWaitMS != cfg2.Routing.CooldownWait.MaxWaitMS {
		t.Errorf("Validate is non-deterministic: cfg1=%+v cfg2=%+v", cfg1, cfg2)
	}
	if len(snap.ResourceRefs) != len(originalRefs) {
		t.Errorf("ResourceRefs length changed: got %d, want %d", len(snap.ResourceRefs), len(originalRefs))
	}
	if !reflect.DeepEqual(snap.ResourceRefs, originalRefs) {
		t.Errorf("ResourceRefs mutated by Validate; before=%v after=%v", originalRefs, snap.ResourceRefs)
	}
	if !mapsEqual(originalSettings, snap.Settings) {
		t.Errorf("snap.Settings mutated by Validate; before=%v after=%v", originalSettings, snap.Settings)
	}
	if !mapsEqual(originalExtra, snap.Extra) {
		t.Errorf("snap.Extra mutated by Validate; before=%v after=%v", originalExtra, snap.Extra)
	}
}

// TestValidatePreservesNestedMaps asserts that the snapshot's nested
// maps survive the projection enough for ParseConfigBytes to reach the
// nested routing.cooldown-wait block and emit corresponding typed Config
// fields. The test is the negative twin of TestValidateValidSettings: it
// focuses on the inner shape rather than the scalar.
func TestValidatePreservesNestedMaps(t *testing.T) {
	snap := configsnapshot.NewEmpty()
	nested := map[string]any{
		"cooldown-wait": map[string]any{
			"max-wait-ms":  15000,
			"max-attempts": 7,
		},
	}
	snap.Settings["routing"] = nested

	cfg, err := configvalidation.Validate(&snap)
	if err != nil {
		t.Fatalf("Validate(nested) error = %v", err)
	}
	if got := cfg.Routing.CooldownWait.MaxWaitMS; got != 15000 {
		t.Errorf("MaxWaitMS = %d; want 15000", got)
	}
	if got := cfg.Routing.CooldownWait.MaxAttempts; got != 7 {
		t.Errorf("MaxAttempts = %d; want 7", got)
	}
}

// TestValidateDeterministicAcrossSettingsOrder pins the determinism claim
// the package doc makes: two Snapshots built with the same Settings keys
// in opposite insertion orders must produce identical Configs. Go map
// iteration order is not stable, so the snapshot projection must sort keys
// before emitting YAML; if the package ever drops that invariant, this
// test catches it.
func TestValidateDeterministicAcrossSettingsOrder(t *testing.T) {
	snapAB := configsnapshot.NewEmpty()
	snapAB.Settings["a"] = 1
	snapAB.Settings["b"] = 2
	snapAB.Settings["routing"] = map[string]any{
		"strategy": "round-robin",
	}

	snapBA := configsnapshot.NewEmpty()
	snapBA.Settings["routing"] = map[string]any{
		"strategy": "round-robin",
	}
	snapBA.Settings["b"] = 2
	snapBA.Settings["a"] = 1

	cfgAB, errAB := configvalidation.Validate(&snapAB)
	if errAB != nil {
		t.Fatalf("Validate(snapAB) error = %v", errAB)
	}
	cfgBA, errBA := configvalidation.Validate(&snapBA)
	if errBA != nil {
		t.Fatalf("Validate(snapBA) error = %v", errBA)
	}
	if !reflect.DeepEqual(cfgAB, cfgBA) {
		t.Errorf("Validate is order-sensitive: cfgAB=%+v cfgBA=%+v", cfgAB, cfgBA)
	}
}

// snapcopy returns a shallow copy of m so callers can detect later mutation.
func snapcopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// mapsEqual reports whether a and b carry the same keys with the same
// values via reflect.DeepEqual so nested map[string]any values are
// compared structurally.
func mapsEqual(a, b map[string]any) bool {
	return reflect.DeepEqual(a, b)
}
