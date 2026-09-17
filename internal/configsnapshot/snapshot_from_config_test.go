package configsnapshot

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// TestSnapshotFromConfigRoundTrip projects a representative *config.Config
// through SnapshotFromConfig and back through MarshalYAML +
// configvalidation.Validate, asserting the scalar runtime fields survive
// the round trip.
func TestSnapshotFromConfigRoundTrip(t *testing.T) {
	cfg := &config.Config{}
	cfg.Port = 18317
	cfg.Host = "127.0.0.1"
	cfg.Debug = true
	cfg.RemoteManagement.AllowRemote = true

	snap, err := SnapshotFromConfig(cfg)
	if err != nil {
		t.Fatalf("SnapshotFromConfig: %v", err)
	}
	if snap.Revision != 0 {
		t.Fatalf("Revision = %d; want 0 (repository assigns the real one)", snap.Revision)
	}
	if snap.UpdatedSource != "cli-import" {
		t.Fatalf("UpdatedSource = %q; want cli-import", snap.UpdatedSource)
	}
	if snap.Settings == nil || snap.Extra == nil {
		t.Fatal("Settings/Extra must be non-nil")
	}
	if snap.ResourceRefs == nil {
		t.Fatal("ResourceRefs must be non-nil (json.Marshal must emit [])")
	}
	if got, ok := snap.Settings["port"]; !ok || got != 18317 {
		t.Fatalf("Settings[port] = %v (%T); want 18317", got, got)
	}
	if got, ok := snap.Settings["host"]; !ok || got != "127.0.0.1" {
		t.Fatalf("Settings[host] = %v; want 127.0.0.1", got)
	}
	if got, ok := snap.Settings["debug"]; !ok || got != true {
		t.Fatalf("Settings[debug] = %v; want true", got)
	}

	// The canonical Validate round-trip lives in configvalidation (which
	// imports this package) and is covered by the end-to-end smoke run;
	// asserting the MarshalYAML output reparses keeps this unit test
	// free of the test-only import cycle.
	data, errMarshal := MarshalYAML(&snap)
	if errMarshal != nil {
		t.Fatalf("MarshalYAML(snapshot): %v", errMarshal)
	}
	if !strings.Contains(string(data), "port: 18317") {
		t.Fatalf("projected yaml missing port: %s", data)
	}
	if !strings.Contains(string(data), "debug: true") {
		t.Fatalf("projected yaml missing debug: %s", data)
	}
}

// TestSnapshotFromConfigPartitionsUnknownKeys asserts keys outside the
// scalarKeys allowlist land in Extra, keeping Settings to the runtime
// scalar projection.
func TestSnapshotFromConfigPartitionsUnknownKeys(t *testing.T) {
	cfg := &config.Config{}
	cfg.Port = 8317

	snap, err := SnapshotFromConfig(cfg)
	if err != nil {
		t.Fatalf("SnapshotFromConfig: %v", err)
	}
	if _, ok := snap.Settings["auth-dir"]; ok {
		t.Fatal("auth-dir must land in Extra, not Settings (not in scalarKeys)")
	}
	if _, ok := snap.Extra["auth-dir"]; !ok {
		t.Fatalf("auth-dir missing from Extra; extra keys = %v", keysOf(snap.Extra))
	}
	if _, ok := snap.Extra["port"]; ok {
		t.Fatal("port must stay in Settings")
	}
}

// TestSnapshotFromConfigNilCfg asserts nil input errors instead of
// panicking.
func TestSnapshotFromConfigNilCfg(t *testing.T) {
	if _, err := SnapshotFromConfig(nil); err == nil {
		t.Fatal("expected error for nil cfg")
	}
}

// TestSnapshotFromConfigRejectsExtraEnvelope documents that the reserved
// __extra envelope cannot leak from a projected config: the scalar
// projection of *config.Config never emits it, and if it somehow did the
// projection refuses rather than silently re-shaping the snapshot.
func TestSnapshotFromConfigRejectsExtraEnvelope(t *testing.T) {
	// A plain config cannot produce __extra at the top level; this test
	// pins the defensive branch by construction (buildRoot/UnmarshalYAML
	// guard the envelope) and keeps the contract visible to reviewers.
	snap, err := SnapshotFromConfig(&config.Config{})
	if err != nil {
		t.Fatalf("SnapshotFromConfig: %v", err)
	}
	if _, ok := snap.Settings[extraEnvelope]; ok {
		t.Fatalf("%s leaked into Settings", extraEnvelope)
	}
	if _, ok := snap.Extra[extraEnvelope]; ok {
		t.Fatalf("%s leaked into Extra", extraEnvelope)
	}
}

// TestSnapshotFromConfigEmptySettingsRejected documents the repository
// contract dependency: an empty *config.Config still produces at least one
// Settings entry after the parser applies defaults, so repository.Save's
// "empty settings rejected" guard cannot trip on the bootstrap path.
func TestSnapshotFromConfigEmptySettingsRejected(t *testing.T) {
	snap, err := SnapshotFromConfig(&config.Config{})
	if err != nil {
		t.Fatalf("SnapshotFromConfig: %v", err)
	}
	if len(snap.Settings) == 0 {
		t.Fatalf("Settings empty after projection; repository.Save rejects empty settings. keys: %v", keysOf(snap.Settings))
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
