package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// runnerMu serializes backup and restore operations across the whole process
// so two simultaneous backups don't interleave, and a restore never races a
// backup.
var runnerMu sync.Mutex

// collector is the subset of Collector used by Runner, kept small for tests.
type collector interface {
	Collect(ctx context.Context) (store.BackupBundle, error)
}

// s3 is the subset of S3Client used by Runner.
type s3 interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) (string, error)
	Download(ctx context.Context, key string) ([]byte, error)
	List(ctx context.Context) ([]SnapshotMeta, error)
	Delete(ctx context.Context, key string) error
}

// Runner orchestrates full backups: collect state, upload to S3, rotate to
// retention. It also exposes restore-from-S3 via RestoreFromS3, delegating the
// bundle application to the attached *Restorer (see SetRestorer).
type Runner struct {
	client    s3
	collector collector
	restorer  *Restorer
	retention int // 0 = keep all
}

// NewRunner builds a Runner.
func NewRunner(client s3, col collector, retention int) *Runner {
	return &Runner{client: client, collector: col, retention: retention}
}

// SetRestorer attaches the bundle-applying Restorer so RestoreFromS3 can work.
// Required before the first RestoreFromS3 call; otherwise it returns an error.
func (r *Runner) SetRestorer(restorer *Restorer) {
	r.restorer = restorer
}

// List returns the snapshots currently stored on the object store, newest
// first. Read-only; does not serialize with backups/restores.
func (r *Runner) List(ctx context.Context) ([]SnapshotMeta, error) {
	return r.client.List(ctx)
}

// RestoreFromS3 downloads a snapshot by key and applies it with mode. The
// runner itself does not lock runnerMu here: the attached Restorer.Restore
// already serializes bundle application against RunBackup, and taking the lock
// twice would self-deadlock.
func (r *Runner) RestoreFromS3(ctx context.Context, key string, mode RestoreMode) (*store.BackupImportReport, error) {
	data, err := r.client.Download(ctx, key)
	if err != nil {
		return nil, err
	}
	bundle, err := ReadBundle(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("backup runner: parse snapshot %s: %w", key, err)
	}
	if r.restorer == nil {
		return nil, errors.New("backup runner: restorer not configured")
	}
	res, err := r.restorer.Restore(ctx, bundle, mode)
	if err != nil {
		return nil, err
	}
	return res.Report, nil
}

// RunBackup performs one full backup and returns the snapshot metadata. It is
// serialized with restore operations via runnerMu. Rotation runs only when
// Upload succeeds so a failed upload never triggers deletion of older snapshots.
func (r *Runner) RunBackup(ctx context.Context) (SnapshotMeta, error) {
	runnerMu.Lock()
	defer runnerMu.Unlock()

	bundle, err := r.collector.Collect(ctx)
	if err != nil {
		return SnapshotMeta{}, err
	}
	var buf bytes.Buffer
	if err := WriteBundle(&buf, bundle); err != nil {
		return SnapshotMeta{}, err
	}
	key := snapshotKey(bundle.ExportedAt)
	full, err := r.client.Upload(ctx, key, buf.Bytes(), "application/json")
	if err != nil {
		return SnapshotMeta{}, err
	}
	meta := SnapshotMeta{Key: full, Size: int64(buf.Len()), ExportedAt: bundle.ExportedAt}
	if err := r.rotate(ctx, full); err != nil {
		log.WithError(err).Warn("backup runner: rotation failed; snapshot was uploaded")
	}
	return meta, nil
}

// rotate deletes snapshots beyond the retention window, keeping the newest N.
// It is called after a successful upload so an upload failure never causes
// deletion of older snapshots. retention<=0 means keep-all and returns early.
func (r *Runner) rotate(ctx context.Context, uploadedKey string) error {
	if r.retention <= 0 {
		return nil
	}
	snaps, err := r.client.List(ctx)
	if err != nil {
		return err
	}
	if len(snaps) <= r.retention {
		return nil
	}
	for _, s := range snaps[r.retention:] {
		if s.Key == uploadedKey {
			continue
		}
		if err := r.client.Delete(ctx, s.Key); err != nil {
			return err
		}
		log.WithField("key", s.Key).Info("backup runner: pruned snapshot past retention")
	}
	return nil
}
