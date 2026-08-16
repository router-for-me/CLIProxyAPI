package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// stubCollector returns a fixed bundle with a deterministic ExportedAt.
type stubCollector struct {
	stamp time.Time
}

func (c stubCollector) Collect(ctx context.Context) (store.BackupBundle, error) {
	return store.BackupBundle{
		Version:    2,
		ExportedAt: c.stamp,
		Mode:       "full",
		Summary:    map[string]int{},
		Resources:  map[string]store.BackupResourceData{},
	}, nil
}

// TestRunnerRotationKeepsNewestN: with retention=3, after 5 RunBackup calls
// (with staggered stamps) the in-memory S3 holds exactly the 3 newest keys.
func TestRunnerRotationKeepsNewestN(t *testing.T) {
	stub := newStubMinioClient()
	cli := newTestClient(stub)
	r := NewRunner(cli, stubCollector{}, 3)

	stamp := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		r.collector = stubCollector{stamp: stamp}
		if _, err := r.RunBackup(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Next backup gets a later stamp so ordering is deterministic.
		stamp = stamp.Add(time.Minute)
	}

	snaps, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 3 {
		t.Fatalf("want 3 retained, got %d: %v", len(snaps), snaps)
	}
}

// TestRunnerRetentionZeroKeepsAll: retention=0 → keep everything.
func TestRunnerRetentionZeroKeepsAll(t *testing.T) {
	stub := newStubMinioClient()
	cli := newTestClient(stub)
	r := NewRunner(cli, stubCollector{}, 0)

	for i := 0; i < 4; i++ {
		r.collector = stubCollector{stamp: time.Now().Add(time.Duration(i) * time.Minute)}
		if _, err := r.RunBackup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	snaps, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 4 {
		t.Fatalf("want 4 kept (retention=0), got %d", len(snaps))
	}
}

// TestRunnerUploadedKeyNeverPrunedOnClockSkew: when List returns entries in
// order that puts the just-uploaded key in the prune range (e.g. stale
// S3 clock), rotation must NOT delete the snapshot that was just uploaded.
func TestRunnerUploadedKeyNeverPrunedOnClockSkew(t *testing.T) {
	stub := newStubMinioClient()
	cli := newTestClient(stub)
	r := NewRunner(cli, stubCollector{}, 2)

	// Two backups land in S3 with plausible times.
	for i := 0; i < 2; i++ {
		r.collector = stubCollector{stamp: time.Date(2026, 8, 16, 12, i, 0, 0, time.UTC)}
		if _, err := r.RunBackup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	// Manually overwrite the older snapshot's LastModified to appear NEWER than
	// the most recent upload in S3 listing order. This simulates skew where
	// the uploadedKey appears to sort behind it.
	keys := make([]string, 0, len(stub.objects))
	for k := range stub.objects {
		keys = append(keys, k)
	}
	if len(keys) != 2 {
		t.Fatalf("setup: want 2 objects, got %d", len(keys))
	}
	// Push the older one forward by 24h so List() sorts it as newest.
	stub.mu.Lock()
	stub.times[keys[0]] = time.Now().Add(24 * time.Hour)
	stub.mu.Unlock()

	// Run a third backup: rotation should kick in with keep=2; the newly
	// uploaded key is never deleted even though it wouldn't be in the top-N
	// by mtime order.
	r.collector = stubCollector{stamp: time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)}
	newMeta, err := r.RunBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// After RunBackup 3, the just-uploaded one is kept (even if its LastModified
	// puts it earlier than the manipulated one). Total == 2 due to retention.
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots retained, got %d: %v", len(snaps), snaps)
	}
	found := false
	for _, s := range snaps {
		if s.Key == newMeta.Key {
			found = true
		}
	}
	if !found {
		t.Errorf("newly uploaded snapshot %s was pruned (must never happen)", newMeta.Key)
	}
}

// helper: json encoding check that round trips through WriteBundle works
func bundleRoundTrip(t *testing.T, b store.BackupBundle) store.BackupBundle {
	t.Helper()
	buf, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var back store.BackupBundle
	if err := json.Unmarshal(buf, &back); err != nil {
		t.Fatal(err)
	}
	return back
}

// TestRunnerRestoreFromS3 verifies RestoreFromS3 downloads a snapshot by key,
// parses the bundle, and applies it (merge mode writes missing auth files).
func TestRunnerRestoreFromS3(t *testing.T) {
	stub := newStubMinioClient()
	cli := newTestClient(stub)
	authDir := filepath.Join(t.TempDir(), "auths")

	r := NewRunner(cli, stubCollector{}, 0)
	r.SetRestorer(NewRestorer(nil, nil, "", authDir))

	bundle := store.BackupBundle{
		Version: 2,
		Mode:    "full",
		AuthFiles: []store.BackupAuthFile{
			{Path: "a.json", Content: "a"},
		},
	}
	var buf bytes.Buffer
	if err := WriteBundle(&buf, bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Upload(context.Background(), "snap.json", buf.Bytes(), "application/json"); err != nil {
		t.Fatal(err)
	}

	report, err := r.RestoreFromS3(context.Background(), "snap.json", RestoreModeMerge)
	if err != nil {
		t.Fatal(err)
	}
	if report != nil {
		t.Errorf("report = %+v, want nil (importer is nil)", report)
	}
	if _, err := os.Stat(filepath.Join(authDir, "a.json")); err != nil {
		t.Errorf("a.json not restored: %v", err)
	}
}

// TestRunnerRestoreFromS3NoRestorer verifies RestoreFromS3 returns an error
// when the runner has no attached restorer.
func TestRunnerRestoreFromS3NoRestorer(t *testing.T) {
	stub := newStubMinioClient()
	cli := newTestClient(stub)
	r := NewRunner(cli, stubCollector{}, 0)

	var buf bytes.Buffer
	if err := WriteBundle(&buf, store.BackupBundle{Version: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Upload(context.Background(), "snap.json", buf.Bytes(), "application/json"); err != nil {
		t.Fatal(err)
	}

	if _, err := r.RestoreFromS3(context.Background(), "snap.json", RestoreModeMerge); err == nil {
		t.Fatal("expected error when restorer not configured")
	}
}
