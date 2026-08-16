package backup

import (
	"bytes"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestBundleWriteReadRoundTrip(t *testing.T) {
	t.Parallel()

	in := store.BackupBundle{
		Version:    2,
		ExportedAt: time.Date(2026, 8, 16, 2, 30, 0, 0, time.UTC),
		Mode:       "full",
		ConfigYAML: "k: v\n",
		AuthFiles: []store.BackupAuthFile{
			{Path: "a.json", Content: "{}"},
		},
		Resources: map[string]store.BackupResourceData{},
	}

	var buf bytes.Buffer
	if err := WriteBundle(&buf, in); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	out, err := ReadBundle(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}

	if out.Version != in.Version {
		t.Errorf("Version: got %d, want %d", out.Version, in.Version)
	}
	if out.Mode != in.Mode {
		t.Errorf("Mode: got %q, want %q", out.Mode, in.Mode)
	}
	if out.ConfigYAML != in.ConfigYAML {
		t.Errorf("ConfigYAML: got %q, want %q", out.ConfigYAML, in.ConfigYAML)
	}
	if len(out.AuthFiles) != len(in.AuthFiles) {
		t.Fatalf("AuthFiles count: got %d, want %d", len(out.AuthFiles), len(in.AuthFiles))
	}
	if out.AuthFiles[0].Path != in.AuthFiles[0].Path {
		t.Errorf("AuthFiles[0].Path: got %q, want %q", out.AuthFiles[0].Path, in.AuthFiles[0].Path)
	}
	if out.AuthFiles[0].Content != in.AuthFiles[0].Content {
		t.Errorf("AuthFiles[0].Content: got %q, want %q", out.AuthFiles[0].Content, in.AuthFiles[0].Content)
	}
}

func TestSnapshotKeySortable(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 8, 16, 2, 30, 0, 0, time.UTC)
	k0 := snapshotKey(t0)
	k1 := snapshotKey(t0.Add(time.Second))

	if k0 >= k1 {
		t.Errorf("keys not chronological: %q should sort before %q", k0, k1)
	}
	// Same time maps to the same key.
	if snapshotKey(t0) != k0 {
		t.Errorf("same time should yield same key: %q vs %q", snapshotKey(t0), k0)
	}
	// Non-UTC input normalizes to UTC before formatting.
	if snapshotKey(t0.In(time.FixedZone("x", 3600))) != k0 {
		t.Errorf("non-UTC time should normalize to the same key")
	}
}
