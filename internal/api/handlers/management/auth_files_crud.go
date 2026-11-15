package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Download single auth file by name
func (h *Handler) DownloadAuthFile(c *gin.Context) {
	name := strings.TrimSpace(c.Query("name"))
	if isUnsafeAuthFileName(name) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		c.JSON(400, gin.H{"error": "name must end with .json"})
		return
	}
	full := filepath.Join(h.cfg.AuthDir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(404, gin.H{"error": "file not found"})
		} else {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read file: %v", err)})
		}
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	c.Data(200, "application/json", data)
}

// Upload auth file: multipart or raw JSON with ?name=
func (h *Handler) UploadAuthFile(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx := c.Request.Context()

	fileHeaders, errMultipart := h.multipartAuthFileHeaders(c)
	if errMultipart != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errMultipart, http.StatusBadRequest), gin.H{"error": fmt.Sprintf("invalid multipart form: %v", errMultipart)})
		return
	}
	if len(fileHeaders) == 1 {
		if _, errUpload := h.storeUploadedAuthFile(ctx, fileHeaders[0]); errUpload != nil {
			if errors.Is(errUpload, errAuthFileMustBeJSON) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "file must be .json"})
				return
			}
			if errors.Is(errUpload, errInvalidAuthFile) {
				c.JSON(http.StatusBadRequest, gin.H{"error": errUpload.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": errUpload.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if len(fileHeaders) > 1 {
		uploaded := make([]string, 0, len(fileHeaders))
		failed := make([]gin.H, 0)
		for _, file := range fileHeaders {
			name, errUpload := h.storeUploadedAuthFile(ctx, file)
			if errUpload != nil {
				failureName := ""
				if file != nil {
					failureName = filepath.Base(file.Filename)
				}
				msg := errUpload.Error()
				if errors.Is(errUpload, errAuthFileMustBeJSON) {
					msg = "file must be .json"
				}
				failed = append(failed, gin.H{"name": failureName, "error": msg})
				continue
			}
			uploaded = append(uploaded, name)
		}
		if len(failed) > 0 {
			c.JSON(http.StatusMultiStatus, gin.H{
				"status":   "partial",
				"uploaded": len(uploaded),
				"files":    uploaded,
				"failed":   failed,
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "uploaded": len(uploaded), "files": uploaded})
		return
	}
	if c.ContentType() == "multipart/form-data" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files uploaded"})
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if isUnsafeAuthFileName(name) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		c.JSON(400, gin.H{"error": "name must end with .json"})
		return
	}
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), gin.H{"error": "failed to read body"})
		return
	}
	if err = h.writeAuthFile(ctx, filepath.Base(name), data); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errInvalidAuthFile) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

// Delete auth files: single by name or all
func (h *Handler) DeleteAuthFile(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx := c.Request.Context()
	if all := c.Query("all"); all == "true" || all == "1" || all == "*" {
		authDir := h.currentHandlerAuthDir()
		entries, err := os.ReadDir(authDir)
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
			return
		}
		v8Mode := isV8AuthFileMutation(ctx)
		deleted := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
			full := filepath.Join(authDir, name)
			if !filepath.IsAbs(full) {
				if abs, errAbs := filepath.Abs(full); errAbs == nil {
					full = abs
				}
			}
			errDelete := func() error {
				resolvedFull := ""
				if v8Mode {
					resolvedFull = canonicalV8AuthFilePath(full)
				}
				release := h.beginSIWCFileMutation(ctx, full)
				defer release()
				if errRemove := os.Remove(full); errRemove != nil {
					return nil
				}
				if errDel := h.deleteTokenRecord(ctx, full); errDel != nil {
					return errDel
				}
				deleted++
				if v8Mode {
					h.reconcileRemovedV8SourceAuths(ctx, full, resolvedFull, "")
				} else {
					h.removeAuth(ctx, full)
				}
				return nil
			}()
			if errDelete != nil {
				c.JSON(500, gin.H{"error": errDelete.Error()})
				return
			}
		}
		c.JSON(200, gin.H{"status": "ok", "deleted": deleted})
		return
	}

	names, errNames := requestedAuthFileNamesForDelete(c)
	if errNames != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errNames, http.StatusBadRequest), gin.H{"error": errNames.Error()})
		return
	}
	if len(names) == 0 {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if len(names) == 1 {
		if _, status, errDelete := h.deleteAuthFileByName(ctx, names[0]); errDelete != nil {
			c.JSON(status, gin.H{"error": errDelete.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	deletedFiles := make([]string, 0, len(names))
	failed := make([]gin.H, 0)
	for _, name := range names {
		deletedName, _, errDelete := h.deleteAuthFileByName(ctx, name)
		if errDelete != nil {
			failed = append(failed, gin.H{"name": name, "error": errDelete.Error()})
			continue
		}
		deletedFiles = append(deletedFiles, deletedName)
	}
	if len(failed) > 0 {
		c.JSON(http.StatusMultiStatus, gin.H{
			"status":  "partial",
			"deleted": len(deletedFiles),
			"files":   deletedFiles,
			"failed":  failed,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted": len(deletedFiles), "files": deletedFiles})
}

func (h *Handler) multipartAuthFileHeaders(c *gin.Context) ([]*multipart.FileHeader, error) {
	if h == nil || c == nil || c.ContentType() != "multipart/form-data" {
		return nil, nil
	}
	form, err := c.MultipartForm()
	if err != nil {
		return nil, err
	}
	if form == nil || len(form.File) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(form.File))
	for key := range form.File {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	headers := make([]*multipart.FileHeader, 0)
	for _, key := range keys {
		headers = append(headers, form.File[key]...)
	}
	return headers, nil
}

func (h *Handler) storeUploadedAuthFile(ctx context.Context, file *multipart.FileHeader) (string, error) {
	if file == nil {
		return "", fmt.Errorf("no file uploaded")
	}
	name := filepath.Base(strings.TrimSpace(file.Filename))
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return "", errAuthFileMustBeJSON
	}
	src, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("failed to open uploaded file: %w", err)
	}
	defer src.Close()

	data, err := io.ReadAll(src)
	if err != nil {
		return "", fmt.Errorf("failed to read uploaded file: %w", err)
	}
	if err := h.writeAuthFile(ctx, name, data); err != nil {
		return "", err
	}
	return name, nil
}

func (h *Handler) writeAuthFile(ctx context.Context, name string, data []byte) error {
	if isV8AuthFileMutation(ctx) {
		return h.writeAuthFileV8(ctx, name, data)
	}
	dst := filepath.Join(h.currentHandlerAuthDir(), filepath.Base(name))
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}
	auth, err := h.buildAuthFromFileData(dst, data)
	if err != nil {
		return err
	}
	release := h.beginSIWCFileMutation(ctx, dst)
	defer release()
	if errWrite := h.writeAuthFileBytes(dst, data, auth); errWrite != nil {
		return fmt.Errorf("failed to write file: %w", errWrite)
	}
	if err := h.upsertAuthRecord(ctx, auth); err != nil {
		return err
	}
	if h.postAuthPersistHook != nil {
		if errHook := h.postAuthPersistHook(ctx, auth); errHook != nil {
			return fmt.Errorf("post-auth persist hook failed: %w", errHook)
		}
	}
	return nil
}

func (h *Handler) writeAuthFileV8(ctx context.Context, name string, data []byte) error {
	h.v8AuthUploadMu.Lock()
	defer h.v8AuthUploadMu.Unlock()

	dst := filepath.Join(h.currentHandlerAuthDir(), filepath.Base(name))
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}
	dst = filepath.Clean(dst)

	auths, metadata, errBuild := h.buildAuthsFromFileDataV8(dst, data)
	if errBuild != nil {
		return errBuild
	}
	needCodexLock := h.shouldLockCodexAuthFileV8(dst, metadata, auths)

	release := h.beginSIWCFileMutation(ctx, dst)
	defer release()

	if errWrite := h.writeAuthFileBytesV8(dst, data, needCodexLock); errWrite != nil {
		return fmt.Errorf("failed to write file: %w", errWrite)
	}
	if errReconcile := h.reconcileV8UploadedAuths(ctx, dst, auths); errReconcile != nil {
		return errReconcile
	}
	if h.postAuthPersistHook != nil {
		for _, auth := range auths {
			if auth == nil {
				continue
			}
			if errHook := h.postAuthPersistHook(ctx, auth); errHook != nil {
				return fmt.Errorf("post-auth persist hook failed: %w", errHook)
			}
		}
	}
	return nil
}

func (h *Handler) writeAuthFileBytesV8(dst string, data []byte, lockCodex bool) error {
	if lockCodex {
		return codex.WithFileLock(dst, func() error {
			return writeV8AuthFilePayload(dst, data)
		})
	}
	return writeV8AuthFilePayload(dst, data)
}

func writeV8AuthFilePayload(dst string, data []byte) error {
	if info, errLstat := os.Lstat(dst); errLstat == nil && info.Mode()&os.ModeSymlink != 0 {
		return os.WriteFile(dst, data, 0o600)
	}
	dir := filepath.Dir(dst)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return errMkdir
	}
	tmp, errTemp := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp-*")
	if errTemp != nil {
		return errTemp
	}
	tmpPath := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			if errRemove := os.Remove(tmpPath); errRemove != nil && !os.IsNotExist(errRemove) {
				log.Errorf("failed to remove temporary auth file %s: %v", filepath.Base(tmpPath), errRemove)
			}
		}
	}()
	if errChmod := tmp.Chmod(0o600); errChmod != nil {
		if errClose := tmp.Close(); errClose != nil {
			log.Errorf("failed to close temporary auth file %s: %v", filepath.Base(tmpPath), errClose)
		}
		return errChmod
	}
	if n, errWrite := tmp.Write(data); errWrite != nil {
		if errClose := tmp.Close(); errClose != nil {
			log.Errorf("failed to close temporary auth file %s: %v", filepath.Base(tmpPath), errClose)
		}
		return errWrite
	} else if n != len(data) {
		if errClose := tmp.Close(); errClose != nil {
			log.Errorf("failed to close temporary auth file %s: %v", filepath.Base(tmpPath), errClose)
		}
		return io.ErrShortWrite
	}
	if errClose := tmp.Close(); errClose != nil {
		return errClose
	}
	if errRename := os.Rename(tmpPath, dst); errRename != nil {
		return errRename
	}
	cleanupTmp = false
	return nil
}

func (h *Handler) shouldLockCodexAuthFileV8(dst string, metadata map[string]any, auths []*coreauth.Auth) bool {
	for _, auth := range auths {
		if h.shouldLockCodexAuthFile(auth) {
			return true
		}
	}
	if rawType, _ := metadata["type"].(string); strings.EqualFold(strings.TrimSpace(rawType), "codex") {
		return true
	}
	if h == nil || h.authManager == nil {
		return false
	}
	resolvedDst := canonicalV8AuthFilePath(dst)
	for _, existing := range h.authManager.List() {
		if existing == nil || !coreauth.IsCodexAuthRecord(existing) {
			continue
		}
		if h.authMatchesV8SourcePath(existing, dst, resolvedDst) {
			return true
		}
	}
	return false
}

func (h *Handler) writeAuthFileBytes(dst string, data []byte, auth *coreauth.Auth) error {
	if h.shouldLockCodexAuthFile(auth) {
		return codex.WithFileLock(dst, func() error {
			return os.WriteFile(dst, data, 0o600)
		})
	}
	return os.WriteFile(dst, data, 0o600)
}

func (h *Handler) shouldLockCodexAuthFile(auth *coreauth.Auth) bool {
	if coreauth.IsCodexAuthRecord(auth) {
		return true
	}
	if h != nil && h.authManager != nil && auth != nil && auth.ID != "" {
		if existing, ok := h.authManager.GetByID(auth.ID); ok && coreauth.IsCodexAuthRecord(existing) {
			return true
		}
	}
	return false
}

func requestedAuthFileNamesForDelete(c *gin.Context) ([]string, error) {
	if c == nil {
		return nil, nil
	}
	names := uniqueAuthFileNames(c.QueryArray("name"))
	if len(names) > 0 {
		return names, nil
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to read body")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}

	var objectBody struct {
		Name  string   `json:"name"`
		Names []string `json:"names"`
	}
	if body[0] == '[' {
		var arrayBody []string
		if err := json.Unmarshal(body, &arrayBody); err != nil {
			return nil, fmt.Errorf("invalid request body")
		}
		return uniqueAuthFileNames(arrayBody), nil
	}
	if err := json.Unmarshal(body, &objectBody); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}

	out := make([]string, 0, len(objectBody.Names)+1)
	if strings.TrimSpace(objectBody.Name) != "" {
		out = append(out, objectBody.Name)
	}
	out = append(out, objectBody.Names...)
	return uniqueAuthFileNames(out), nil
}

func uniqueAuthFileNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func (h *Handler) deleteAuthFileByName(ctx context.Context, name string) (string, int, error) {
	name = strings.TrimSpace(name)
	if isUnsafeAuthFileName(name) {
		return "", http.StatusBadRequest, fmt.Errorf("invalid name")
	}

	targetPath := filepath.Join(h.currentHandlerAuthDir(), filepath.Base(name))
	targetID := ""
	if targetAuth := h.findAuthForDelete(name); targetAuth != nil {
		if !isPluginVirtualSourceDelete(name, targetAuth) {
			return filepath.Base(name), http.StatusConflict, errPluginVirtualAuth
		}
		targetID = strings.TrimSpace(targetAuth.ID)
		if path := strings.TrimSpace(authAttribute(targetAuth, "path")); path != "" {
			targetPath = path
		}
	}
	if !filepath.IsAbs(targetPath) {
		if abs, errAbs := filepath.Abs(targetPath); errAbs == nil {
			targetPath = abs
		}
	}
	resolvedTargetPath := ""
	if isV8AuthFileMutation(ctx) {
		resolvedTargetPath = canonicalV8AuthFilePath(targetPath)
	}
	release := h.beginSIWCFileMutation(ctx, targetPath)
	defer release()
	if errRemove := os.Remove(targetPath); errRemove != nil {
		if os.IsNotExist(errRemove) {
			return filepath.Base(name), http.StatusNotFound, errAuthFileNotFound
		}
		return filepath.Base(name), http.StatusInternalServerError, fmt.Errorf("failed to remove file: %w", errRemove)
	}
	if errDeleteRecord := h.deleteTokenRecord(ctx, targetPath); errDeleteRecord != nil {
		return filepath.Base(name), http.StatusInternalServerError, errDeleteRecord
	}
	if isV8AuthFileMutation(ctx) {
		h.reconcileRemovedV8SourceAuths(ctx, targetPath, resolvedTargetPath, targetID)
	} else {
		h.removeAuthsForPath(ctx, targetPath, targetID)
	}
	return filepath.Base(name), http.StatusOK, nil
}

func isPluginVirtualSourceDelete(name string, auth *coreauth.Auth) bool {
	if !coreauth.IsPluginVirtualAuth(auth) {
		return true
	}
	sourcePath := strings.TrimSpace(authAttribute(auth, coreauth.AttributeVirtualSource))
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(authAttribute(auth, "path"))
	}
	if sourcePath == "" {
		return false
	}
	return strings.EqualFold(filepath.Base(strings.TrimSpace(name)), filepath.Base(sourcePath))
}

func (h *Handler) findAuthForDelete(name string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if auth, ok := h.authManager.GetByID(name); ok {
		return auth
	}
	auths := h.authManager.List()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.FileName) == name {
			return auth
		}
		if filepath.Base(strings.TrimSpace(authAttribute(auth, "path"))) == name {
			return auth
		}
	}
	return nil
}

func (h *Handler) authIDForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		if abs, errAbs := filepath.Abs(path); errAbs == nil {
			path = abs
		}
	}
	id := path
	if cfg := h.configSnapshot(); cfg != nil {
		authDir := strings.TrimSpace(cfg.AuthDir)
		if resolvedAuthDir, errResolve := util.ResolveAuthDir(authDir); errResolve == nil && resolvedAuthDir != "" {
			authDir = resolvedAuthDir
		}
		if authDir != "" {
			authDir = filepath.Clean(authDir)
			if !filepath.IsAbs(authDir) {
				if abs, errAbs := filepath.Abs(authDir); errAbs == nil {
					authDir = abs
				}
			}
			if rel, errRel := filepath.Rel(authDir, path); errRel == nil && rel != "" {
				id = rel
			}
		}
	}
	// On Windows, normalize ID casing to avoid duplicate auth entries caused by case-insensitive paths.
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func (h *Handler) registerAuthFromFile(ctx context.Context, path string, data []byte) error {
	return h.registerAuthFromFileWithID(ctx, path, data, "")
}

func (h *Handler) registerAuthFromFileWithID(ctx context.Context, path string, data []byte, preferredID string) error {
	if h.authManager == nil {
		return nil
	}
	auth, err := h.buildAuthFromFileData(path, data)
	if err != nil {
		return err
	}
	if trimmedID := strings.TrimSpace(preferredID); trimmedID != "" && auth != nil && auth.ID != trimmedID {
		auth.ID = trimmedID
		if existing, ok := h.authManager.GetByID(trimmedID); ok && existing != nil {
			auth.CreatedAt = existing.CreatedAt
			if !coreauth.CredentialsChanged(existing, auth) {
				if auth.LastRefreshedAt.IsZero() {
					auth.LastRefreshedAt = existing.LastRefreshedAt
				}
				auth.NextRefreshAfter = existing.NextRefreshAfter
			}
			auth.Runtime = existing.Runtime
		}
	}
	return h.upsertAuthRecord(ctx, auth)
}

func (h *Handler) buildAuthFromFileData(path string, data []byte) (*coreauth.Auth, error) {
	if path == "" {
		return nil, fmt.Errorf("auth path is empty")
	}
	if data == nil {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read auth file: %w", err)
		}
	}
	metadata := make(map[string]any)
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidAuthFile, err)
	}
	coreauth.NormalizeCredentialMetadata(metadata)
	// Loading tolerates invalid exclusion rules, so reject them here before anything is persisted.
	if _, errExcluded := synthesizer.ExtractExcludedModelsFromMetadata(metadata); errExcluded != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidAuthFile, errExcluded)
	}
	provider, _ := metadata["type"].(string)
	if provider == "" {
		provider = "unknown"
	}
	label := provider
	if email, ok := metadata["email"].(string); ok && email != "" {
		label = email
	}
	lastRefresh, hasLastRefresh := extractLastRefreshTimestamp(metadata)

	authID := h.authIDForPath(path)
	if authID == "" {
		authID = path
	}
	auth := (*coreauth.Auth)(nil)
	if cfg := h.configSnapshot(); cfg != nil {
		sctx := &synthesizer.SynthesisContext{
			Config:      cfg,
			AuthDir:     cfg.AuthDir,
			Now:         time.Now(),
			IDGenerator: synthesizer.NewStableIDGenerator(),
		}
		generated, errSynthesize := synthesizer.SynthesizeAuthFile(sctx, path, data)
		if errSynthesize != nil {
			return nil, fmt.Errorf("%w: %w", errInvalidAuthFile, errSynthesize)
		}
		if len(generated) > 0 && generated[0] != nil {
			auth = generated[0].Clone()
		}
	}
	if auth == nil {
		auth = &coreauth.Auth{
			ID:       authID,
			Provider: provider,
			Label:    label,
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path":   path,
				"source": path,
			},
			Metadata:  metadata,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
	}
	auth.ID = authID
	auth.FileName = filepath.Base(path)
	if hasLastRefresh {
		auth.LastRefreshedAt = lastRefresh
	}
	if h != nil && h.authManager != nil {
		if existing, ok := h.authManager.GetByID(authID); ok {
			auth.CreatedAt = existing.CreatedAt
			if !coreauth.CredentialsChanged(existing, auth) {
				if !hasLastRefresh {
					auth.LastRefreshedAt = existing.LastRefreshedAt
				}
				auth.NextRefreshAfter = existing.NextRefreshAfter
			}
			auth.Runtime = existing.Runtime
		}
	}
	coreauth.ApplyCustomHeadersFromMetadata(auth)
	return auth, nil
}

func (h *Handler) upsertAuthRecord(ctx context.Context, auth *coreauth.Auth) error {
	if h == nil || h.authManager == nil || auth == nil {
		return nil
	}
	if existing, ok := h.authManager.GetByID(auth.ID); ok {
		auth.CreatedAt = existing.CreatedAt
		_, err := h.authManager.Update(ctx, auth)
		return err
	}
	_, err := h.authManager.Register(ctx, auth)
	return err
}

func (h *Handler) currentHandlerAuthDir() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return ""
	}
	return h.cfg.AuthDir
}

func resolvedV8SynthesisAuthDir(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	authDir := strings.TrimSpace(cfg.AuthDir)
	if authDir == "" {
		return ""
	}
	if resolved, errResolve := util.ResolveAuthDir(authDir); errResolve == nil && resolved != "" {
		authDir = resolved
	}
	if abs, errAbs := filepath.Abs(authDir); errAbs == nil && strings.TrimSpace(abs) != "" {
		authDir = abs
	}
	return filepath.Clean(authDir)
}

func (h *Handler) v8AuthIDForPath(cfg *config.Config, path string) string {
	id := h.authIDForPath(path)
	if id != "" && id != path && !strings.HasPrefix(id, ".."+string(filepath.Separator)) {
		return id
	}
	authDir := resolvedV8SynthesisAuthDir(cfg)
	if authDir != "" {
		canonDir := canonicalV8AuthFilePath(authDir)
		canonPath := canonicalV8AuthFilePath(path)
		if canonDir != "" && canonPath != "" {
			if rel, errRel := filepath.Rel(canonDir, canonPath); errRel == nil && rel != "" && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				if runtime.GOOS == "windows" {
					return strings.ToLower(rel)
				}
				return rel
			}
		}
	}
	if id == "" {
		return path
	}
	return id
}

func (h *Handler) buildAuthsFromFileDataV8(path string, data []byte) ([]*coreauth.Auth, map[string]any, error) {
	if path == "" {
		return nil, nil, fmt.Errorf("auth path is empty")
	}
	if data == nil {
		var errRead error
		data, errRead = os.ReadFile(path)
		if errRead != nil {
			return nil, nil, fmt.Errorf("failed to read auth file: %w", errRead)
		}
	}
	metadata, errParseMeta := parseV8AuthMetadataObject(data)
	if errParseMeta != nil {
		return nil, nil, fmt.Errorf("%w: %w", errInvalidAuthFile, errParseMeta)
	}
	coreauth.NormalizeCredentialMetadata(metadata)
	if errWeight := coreauth.ValidateAuthWeight(&coreauth.Auth{Metadata: metadata}); errWeight != nil {
		return nil, nil, fmt.Errorf("%w: %w", errInvalidAuthFile, errWeight)
	}
	if _, errExcluded := synthesizer.ExtractExcludedModelsFromMetadata(metadata); errExcluded != nil {
		return nil, nil, fmt.Errorf("%w: %w", errInvalidAuthFile, errExcluded)
	}

	provider, _ := metadata["type"].(string)
	if strings.TrimSpace(provider) == "" {
		provider = "unknown"
	}
	label := provider
	if email, ok := metadata["email"].(string); ok && email != "" {
		label = email
	}
	lastRefresh, hasLastRefresh := extractLastRefreshTimestamp(metadata)

	cfg := h.configSnapshot()
	authID := h.v8AuthIDForPath(cfg, path)
	defaultFileName := filepath.Base(path)

	sctx := &synthesizer.SynthesisContext{
		Config:            cfg,
		AuthDir:           resolvedV8SynthesisAuthDir(cfg),
		Now:               time.Now(),
		IDGenerator:       synthesizer.NewStableIDGenerator(),
		PluginAuthParser:  h.pluginHostSnapshot(),
		StrictFileParsing: true,
	}
	generated, pluginHandled, errSynthesize := synthesizer.SynthesizeAuthFileDetailed(sctx, path, data)
	if errSynthesize != nil {
		return nil, nil, fmt.Errorf("%w: %w", errInvalidAuthFile, errSynthesize)
	}

	if pluginHandled {
		if len(generated) == 0 {
			return nil, metadata, nil
		}
		out := make([]*coreauth.Auth, 0, len(generated))
		for _, item := range generated {
			if item == nil {
				continue
			}
			auth := item.Clone()
			auth.ID = strings.TrimSpace(auth.ID)
			if auth.ID == "" {
				auth.ID = authID
			}
			auth.FileName = strings.TrimSpace(auth.FileName)
			if auth.FileName == "" {
				auth.FileName = defaultFileName
			}
			h.preserveExistingAuthFields(auth, lastRefresh, hasLastRefresh)
			coreauth.ApplyCustomHeadersFromMetadata(auth)
			auth.BindFileSnapshot(path, data)
			out = append(out, auth)
		}
		return out, metadata, nil
	}

	var auth *coreauth.Auth
	if len(generated) > 0 && generated[0] != nil {
		auth = generated[0].Clone()
	}
	if auth == nil {
		disabled, _ := metadata["disabled"].(bool)
		status := coreauth.StatusActive
		if disabled {
			status = coreauth.StatusDisabled
		}
		auth = &coreauth.Auth{
			ID:       authID,
			Provider: provider,
			Label:    label,
			Status:   status,
			Disabled: disabled,
			Attributes: map[string]string{
				coreauth.AttributePath:          path,
				coreauth.AttributeSource:        path,
				coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			},
			Metadata:  metadata,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		coreauth.ApplyAuthPriorityMetadata(auth, metadata)
		if errWeight := coreauth.ApplyAuthWeightMetadata(auth, metadata); errWeight != nil {
			return nil, nil, fmt.Errorf("%w: %w", errInvalidAuthFile, errWeight)
		}
	}
	auth.ID = authID
	auth.FileName = defaultFileName
	h.preserveExistingAuthFields(auth, lastRefresh, hasLastRefresh)
	coreauth.ApplyCustomHeadersFromMetadata(auth)
	auth.BindFileSnapshot(path, data)
	return []*coreauth.Auth{auth}, metadata, nil
}

func (h *Handler) preserveExistingAuthFields(auth *coreauth.Auth, lastRefresh time.Time, hasLastRefresh bool) {
	if auth == nil {
		return
	}
	if hasLastRefresh {
		auth.LastRefreshedAt = lastRefresh
	}
	if h != nil && h.authManager != nil && strings.TrimSpace(auth.ID) != "" {
		if existing, ok := h.authManager.GetByID(auth.ID); ok && existing != nil {
			auth.CreatedAt = existing.CreatedAt
			if !coreauth.CredentialsChanged(existing, auth) {
				if !hasLastRefresh {
					auth.LastRefreshedAt = existing.LastRefreshedAt
				}
				auth.NextRefreshAfter = existing.NextRefreshAfter
			}
			auth.Runtime = existing.Runtime
		}
	}
}

func parseV8AuthMetadataObject(data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 || !ojson.ValidBytes(data) {
		return nil, fmt.Errorf("invalid JSON payload")
	}
	parsed := ojson.ParseBytes(data)
	if !parsed.IsObject() {
		return nil, fmt.Errorf("auth file payload must be a JSON object")
	}
	if metadata, ok := parsed.Value().(map[string]any); ok && metadata != nil {
		return metadata, nil
	}
	return make(map[string]any), nil
}

func (h *Handler) reconcileV8UploadedAuths(ctx context.Context, dst string, auths []*coreauth.Auth) error {
	if h == nil || h.authManager == nil {
		return nil
	}
	skipPersistCtx := coreauth.WithSkipPersist(ctx)
	keepIDs := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		if auth == nil || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		keepIDs[auth.ID] = struct{}{}
		if errUpsert := h.upsertAuthRecord(skipPersistCtx, auth.Clone()); errUpsert != nil {
			return errUpsert
		}
	}
	resolvedDst := canonicalV8AuthFilePath(dst)
	for _, existing := range h.authManager.List() {
		if !isReconcilableV8FileAuth(existing) {
			continue
		}
		if _, keep := keepIDs[existing.ID]; keep {
			continue
		}
		if h.authMatchesV8SourcePath(existing, dst, resolvedDst) {
			h.removeAuthAndUnregister(skipPersistCtx, existing.ID)
		}
	}
	if store := h.tokenStoreWithBaseDir(); store != nil {
		if persister, ok := store.(interface {
			PersistAuthFiles(context.Context, string, ...string) error
		}); ok {
			if errPersist := persister.PersistAuthFiles(ctx, fmt.Sprintf("Upload auth %s", filepath.Base(dst)), dst); errPersist != nil {
				log.WithError(errPersist).Warnf("failed to persist uploaded auth file %s to backing store", filepath.Base(dst))
			}
		}
	}
	return nil
}

func (h *Handler) reconcileRemovedV8SourceAuths(ctx context.Context, targetPath, resolvedTargetPath, fallbackID string) {
	if h == nil || h.authManager == nil {
		return
	}
	skipPersistCtx := coreauth.WithSkipPersist(ctx)
	removed := false
	for _, existing := range h.authManager.List() {
		if !isReconcilableV8FileAuth(existing) {
			continue
		}
		if h.authMatchesV8SourcePath(existing, targetPath, resolvedTargetPath) {
			h.removeAuthAndUnregister(skipPersistCtx, existing.ID)
			removed = true
		}
	}
	if removed {
		return
	}
	if trimmedID := strings.TrimSpace(fallbackID); trimmedID != "" {
		h.removeAuthAndUnregister(skipPersistCtx, trimmedID)
		return
	}
	if authID := h.v8AuthIDForPath(h.configSnapshot(), targetPath); strings.TrimSpace(authID) != "" {
		h.removeAuthAndUnregister(skipPersistCtx, authID)
	}
}

func (h *Handler) removeAuthAndUnregister(ctx context.Context, id string) {
	if h == nil || h.authManager == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	if _, ok := h.authManager.GetByID(id); ok {
		h.authManager.Remove(ctx, id)
		registry.GetGlobalRegistry().UnregisterClient(id)
		return
	}
	authID := h.authIDForPath(id)
	if authID == "" {
		return
	}
	h.authManager.Remove(ctx, authID)
	registry.GetGlobalRegistry().UnregisterClient(authID)
}

func isReconcilableV8FileAuth(auth *coreauth.Auth) bool {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return false
	}
	switch auth.AuthSourceKind() {
	case coreauth.AuthSourceMemory, coreauth.AuthSourceConfig:
		return false
	default:
		return true
	}
}

func (h *Handler) authMatchesV8SourcePath(auth *coreauth.Auth, targetPath, resolvedTargetPath string) bool {
	if auth == nil || strings.TrimSpace(targetPath) == "" {
		return false
	}
	for _, key := range []string{coreauth.AttributePath, coreauth.AttributeSource, coreauth.AttributeVirtualSource} {
		candidate := strings.TrimSpace(authAttribute(auth, key))
		if candidate == "" {
			continue
		}
		if sameV8AuthFilePath(candidate, targetPath) || (resolvedTargetPath != "" && sameV8AuthFilePath(candidate, resolvedTargetPath)) {
			return true
		}
	}
	if strings.TrimSpace(authAttribute(auth, coreauth.AttributePath)) == "" &&
		strings.TrimSpace(authAttribute(auth, coreauth.AttributeVirtualSource)) == "" &&
		!coreauth.IsPluginVirtualAuth(auth) {
		cfg := h.configSnapshot()
		if auth.ID == h.v8AuthIDForPath(cfg, targetPath) || (resolvedTargetPath != "" && auth.ID == h.v8AuthIDForPath(cfg, resolvedTargetPath)) {
			return true
		}
	}
	return false
}

func sameV8AuthFilePath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return sameSIWCMutationPath(a, b)
}

func canonicalV8AuthFilePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if real, errEval := filepath.EvalSymlinks(path); errEval == nil && strings.TrimSpace(real) != "" {
		path = real
	} else if parent, errParent := filepath.EvalSymlinks(filepath.Dir(path)); errParent == nil && strings.TrimSpace(parent) != "" {
		path = filepath.Join(parent, filepath.Base(path))
	}
	if abs, errAbs := filepath.Abs(path); errAbs == nil && strings.TrimSpace(abs) != "" {
		path = abs
	}
	return filepath.Clean(path)
}
