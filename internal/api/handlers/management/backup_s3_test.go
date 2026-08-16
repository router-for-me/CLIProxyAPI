package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/backup"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// stubBackupRunner implements backupRunner for handler tests.
type stubBackupRunner struct {
	meta    backup.SnapshotMeta
	snaps   []backup.SnapshotMeta
	runErr  error
	listErr error
}

func (s *stubBackupRunner) RunBackup(ctx context.Context) (backup.SnapshotMeta, error) {
	return s.meta, s.runErr
}

func (s *stubBackupRunner) List(ctx context.Context) ([]backup.SnapshotMeta, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.snaps, nil
}

// stubBackupRestorer implements backupRestorer for handler tests.
type stubBackupRestorer struct {
	result backup.RestoreResult
	err    error
	key    string
	mode   backup.RestoreMode
}

func (s *stubBackupRestorer) RestoreFromS3(ctx context.Context, key string, mode backup.RestoreMode) (backup.RestoreResult, error) {
	s.key = key
	s.mode = mode
	return s.result, s.err
}

// newBackupRouter builds a router with the /backup routes wired to the given
// runner/restorer via SetBackupS3.
func newBackupRouter(runner backupRunner, restorer backupRestorer) *gin.Engine {
	h := newBareHandler()
	h.SetBackupS3(runner, restorer, 30*time.Minute, 7)
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/backup", h.ListBackups)
	g.POST("/backup", h.CreateBackup)
	g.POST("/backup/restore", h.RestoreBackup)
	g.GET("/backup/settings", h.BackupSettings)
	return r
}

func TestListBackups(t *testing.T) {
	now := time.Now().UTC()
	stub := &stubBackupRunner{snaps: []backup.SnapshotMeta{
		{Key: "bk/nixllm_backup_2026-08-16T00_00_00Z.json", Size: 10, ExportedAt: now},
	}}
	r := newBackupRouter(stub, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/backup", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "bk/nixllm_backup_2026-08-16T00_00_00Z.json") {
		t.Errorf("response missing snapshot key: %s", w.Body.String())
	}
}

func TestListBackupsNotConfigured(t *testing.T) {
	r := newBackupRouter(nil, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/backup", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503; body=%s", w.Code, w.Body.String())
	}
}

func TestCreateBackup(t *testing.T) {
	now := time.Now().UTC()
	stub := &stubBackupRunner{meta: backup.SnapshotMeta{Key: "bk/snap.json", Size: 42, ExportedAt: now}}
	r := newBackupRouter(stub, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/backup", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "bk/snap.json") {
		t.Errorf("response missing snapshot meta: %s", w.Body.String())
	}
}

func TestCreateBackupNotConfigured(t *testing.T) {
	r := newBackupRouter(nil, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/backup", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503; body=%s", w.Code, w.Body.String())
	}
}

func TestRestoreBackupRequiresConfirm(t *testing.T) {
	stub := &stubBackupRestorer{}
	r := newBackupRouter(nil, stub)

	body := `{"object_key":"bk/snap.json","mode":"replace"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "confirmation_required") {
		t.Errorf("response missing confirmation_required type: %s", w.Body.String())
	}
}

func TestRestoreBackupInvalidMode(t *testing.T) {
	stub := &stubBackupRestorer{}
	r := newBackupRouter(nil, stub)

	body := `{"object_key":"bk/snap.json","mode":"bogus","confirm":true}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_mode") {
		t.Errorf("response missing invalid_mode type: %s", w.Body.String())
	}
}

func TestRestoreBackupMissingObjectKey(t *testing.T) {
	stub := &stubBackupRestorer{}
	r := newBackupRouter(nil, stub)

	body := `{"mode":"merge"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "missing_object_key") {
		t.Errorf("response missing missing_object_key type: %s", w.Body.String())
	}
}

func TestRestoreBackupSuccess(t *testing.T) {
	stub := &stubBackupRestorer{result: backup.RestoreResult{Report: &store.BackupImportReport{}}}
	r := newBackupRouter(nil, stub)

	body := `{"object_key":"bk/snap.json","mode":"merge"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", w.Code, w.Body.String())
	}
	if stub.key != "bk/snap.json" {
		t.Errorf("restorer got key %q, want bk/snap.json", stub.key)
	}
	if stub.mode != backup.RestoreModeMerge {
		t.Errorf("restorer got mode %q, want merge", stub.mode)
	}
	if !strings.Contains(w.Body.String(), "restore") {
		t.Errorf("response missing restore key: %s", w.Body.String())
	}
}

func TestListBackupsError(t *testing.T) {
	stub := &stubBackupRunner{listErr: errors.New("list boom")}
	r := newBackupRouter(stub, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/backup", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "backup_list_failed") {
		t.Errorf("response missing backup_list_failed type: %s", w.Body.String())
	}
}

func TestCreateBackupError(t *testing.T) {
	stub := &stubBackupRunner{runErr: errors.New("run boom")}
	r := newBackupRouter(stub, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/backup", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "backup_failed") {
		t.Errorf("response missing backup_failed type: %s", w.Body.String())
	}
}

func TestRestoreBackupError(t *testing.T) {
	stub := &stubBackupRestorer{err: errors.New("restore boom")}
	r := newBackupRouter(nil, stub)

	body := `{"object_key":"bk/snap.json","mode":"merge"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "restore_failed") {
		t.Errorf("response missing restore_failed type: %s", w.Body.String())
	}
}

func TestRestoreBackupInvalidBody(t *testing.T) {
	stub := &stubBackupRestorer{}
	r := newBackupRouter(nil, stub)

	// Malformed JSON body → ShouldBindJSON fails.
	body := `{"object_key":`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_body") {
		t.Errorf("response missing invalid_body type: %s", w.Body.String())
	}
}

func TestRestoreBackupNotConfigured(t *testing.T) {
	r := newBackupRouter(nil, nil)
	body := `{"object_key":"bk/snap.json","mode":"merge"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503; body=%s", w.Code, w.Body.String())
	}
}

func TestBackupSettings(t *testing.T) {
	h := newBareHandler()
	h.SetBackupS3(&stubBackupRunner{}, &stubBackupRestorer{}, 30*time.Minute, 7)
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/backup/settings", h.BackupSettings)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/backup/settings", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Settings struct {
			S3Configured bool   `json:"s3_configured"`
			Interval     string `json:"interval"`
			Retention    int    `json:"retention"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if !resp.Settings.S3Configured {
		t.Error("s3_configured = false; want true")
	}
	if resp.Settings.Interval != "30m0s" {
		t.Errorf("interval = %q; want 30m0s", resp.Settings.Interval)
	}
	if resp.Settings.Retention != 7 {
		t.Errorf("retention = %d; want 7", resp.Settings.Retention)
	}
}
