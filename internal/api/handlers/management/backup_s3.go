package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/backup"
	log "github.com/sirupsen/logrus"
)

// recoverBackupHandler converts a panic inside the /backup routes into a
// documented 500 error response instead of letting the raw panic propagate.
// The panic value and full panic stack are logged so the root cause is always
// captured even when the response body cannot reflect it. Call via defer at
// the top of each /backup handler. If the response has already been committed
// (e.g. the panic happened during JSON rendering), no second write is attempted.
func recoverBackupHandler(c *gin.Context, errorType, route string) {
	if r := recover(); r != nil {
		stack := string(debug.Stack())
		// Embed the panic value and stack in the message, not just fields:
		// LogFormatter only prints a curated set of fields and would silently
		// drop "panic"/"stack", leaving no way to diagnose the root cause.
		message := fmt.Sprintf("management %s panicked: %v\npanic stack:\n%s", route, r, stack)
		log.WithFields(log.Fields{
			"panic": r,
			"stack": stack,
			"route": route,
		}).Error(message)
		if c != nil && !c.Writer.Written() {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"type":    errorType + "_panic",
				"message": "internal error while processing backup request",
			}})
		}
	}
}

// backupRunner is the subset of the backup subsystem the management /backup
// routes need to list and create snapshots. Kept small for testability.
type backupRunner interface {
	RunBackup(ctx context.Context) (backup.SnapshotMeta, error)
	List(ctx context.Context) ([]backup.SnapshotMeta, error)
}

// backupRestorer is the subset of the backup subsystem the /backup/restore
// route needs to apply a snapshot. *backup.Runner satisfies it via
// RestoreFromS3 (which delegates bundle application to its attached
// *Restorer).
type backupRestorer interface {
	RestoreFromS3(ctx context.Context, key string, mode backup.RestoreMode) (backup.RestoreResult, error)
}

// backupSettings describes the current backup configuration for status display.
type backupSettings struct {
	S3Configured bool   `json:"s3_configured"`
	Interval     string `json:"interval"`
	Retention    int    `json:"retention"`
}

// backupNotConfigured writes the canonical 503 response used when the
// full-backup-to-S3 subsystem is not wired (BACKUP_S3_ENDPOINT unset).
func (h *Handler) backupNotConfigured(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": gin.H{
			"type":    "backup_not_configured",
			"message": "S3 backup is not configured",
		},
	})
}

// ListBackups returns the snapshots currently available on the object store
// (GET /backup). 503 when the backup subsystem is not configured.
func (h *Handler) ListBackups(c *gin.Context) {
	runner := h.backupRunner()
	if runner == nil {
		h.backupNotConfigured(c)
		return
	}
	defer recoverBackupHandler(c, "backup_list", "GET /backup")
	snaps, err := runner.List(c.Request.Context())
	if err != nil {
		h.backupOperationFailed(c, "backup_list_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"snapshots": snaps})
}

// backupOperationFailed renders a backup-subystem operation error. When the
// runner reports the S3 client is missing the operation is treated as "backup
// not configured" (503), mirroring the explicit backupNotConfigured path;
// otherwise it is a generic 500.
func (h *Handler) backupOperationFailed(c *gin.Context, errorType string, err error) {
	if errors.Is(err, backup.ErrS3NotConfigured) {
		h.backupNotConfigured(c)
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": errorType, "message": err.Error()}})
}

// CreateBackup triggers a manual full backup (POST /backup). 503 when the
// backup subsystem is not configured.
func (h *Handler) CreateBackup(c *gin.Context) {
	runner := h.backupRunner()
	if runner == nil {
		h.backupNotConfigured(c)
		return
	}
	defer recoverBackupHandler(c, "backup_create", "POST /backup")
	meta, err := runner.RunBackup(c.Request.Context())
	if err != nil {
		h.backupOperationFailed(c, "backup_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"snapshot": meta})
}

// RestoreBackup restores a snapshot from S3 (POST /backup/restore). Body:
// {"object_key": "...", "mode": "replace|merge", "confirm": true}. The
// replace mode is destructive and requires an explicit confirm=true. 503 when
// the backup subsystem is not configured.
func (h *Handler) RestoreBackup(c *gin.Context) {
	restorer := h.backupRestorer()
	if restorer == nil {
		h.backupNotConfigured(c)
		return
	}
	defer recoverBackupHandler(c, "backup_restore", "POST /backup/restore")
	var req struct {
		ObjectKey string             `json:"object_key"`
		Mode      backup.RestoreMode `json:"mode"`
		Confirm   bool               `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_body", "message": err.Error()}})
		return
	}
	if !backup.ValidRestoreMode(req.Mode) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_mode", "message": "mode must be replace or merge"}})
		return
	}
	if req.ObjectKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "missing_object_key", "message": "object_key is required"}})
		return
	}
	if req.Mode == backup.RestoreModeReplace && !req.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "confirmation_required", "message": "replace restore requires confirm=true"}})
		return
	}
	res, err := restorer.RestoreFromS3(c.Request.Context(), req.ObjectKey, req.Mode)
	if err != nil {
		h.backupOperationFailed(c, "restore_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"restore": res})
}

// BackupSettings returns the current backup configuration for status display
// (GET /backup/settings).
func (h *Handler) BackupSettings(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"settings": h.backupSettings()})
}
