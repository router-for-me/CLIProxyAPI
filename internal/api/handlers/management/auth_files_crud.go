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

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
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
		entries, err := os.ReadDir(h.cfg.AuthDir)
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
			return
		}
		deleted := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
			full := filepath.Join(h.cfg.AuthDir, name)
			if !filepath.IsAbs(full) {
				if abs, errAbs := filepath.Abs(full); errAbs == nil {
					full = abs
				}
			}
			errDelete := func() error {
				release := h.beginSIWCFileMutation(ctx, full)
				defer release()
				if errRemove := os.Remove(full); errRemove != nil {
					return nil
				}
				if errDel := h.deleteTokenRecord(ctx, full); errDel != nil {
					return errDel
				}
				deleted++
				if isAuthFileManagementV8(ctx) {
					h.removeAuthsForPathV8(ctx, full)
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
	if isAuthFileManagementV8(ctx) {
		return h.writeAuthFileV8(ctx, name, data)
	}
	dst := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
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
	h.authFileWriteMu.Lock()
	defer h.authFileWriteMu.Unlock()
	dst := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}
	if _, err := parseAuthFileMetadata(data); err != nil {
		return err
	}
	release := h.beginSIWCFileMutation(ctx, dst)
	defer release()
	auths, err := h.buildAuthRecordsFromFileDataWithParser(dst, data, h.pluginAuthParserSnapshot())
	if err != nil {
		return err
	}
	var lockAuth *coreauth.Auth
	for _, auth := range auths {
		if h.shouldLockCodexAuthFile(auth) {
			lockAuth = auth
			break
		}
	}
	if lockAuth == nil && h.shouldLockCodexAuthPath(dst) {
		lockAuth = &coreauth.Auth{Provider: "codex"}
	}
	if errWrite := h.writeAuthFileBytesV8(dst, data, lockAuth); errWrite != nil {
		return fmt.Errorf("failed to write file: %w", errWrite)
	}
	if err := h.upsertAuthRecords(ctx, auths); err != nil {
		return err
	}
	keepIDs := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		if auth != nil {
			keepIDs[auth.ID] = struct{}{}
		}
	}
	h.removeConflictingFileAuthsAtPath(ctx, dst, keepIDs)
	if h.postAuthPersistHook != nil {
		for _, auth := range auths {
			if errHook := h.postAuthPersistHook(ctx, auth); errHook != nil {
				return fmt.Errorf("post-auth persist hook failed: %w", errHook)
			}
		}
	}
	return nil
}

func (h *Handler) writeAuthFileBytes(dst string, data []byte, auth *coreauth.Auth) error {
	if h.shouldLockCodexAuthFile(auth) {
		return codex.WithFileLock(dst, func() error {
			return os.WriteFile(dst, data, 0o600)
		})
	}
	return os.WriteFile(dst, data, 0o600)
}

func (h *Handler) writeAuthFileBytesV8(dst string, data []byte, auth *coreauth.Auth) error {
	write := func() error { return atomicWriteAuthFile(dst, data) }
	if h.shouldLockCodexAuthFile(auth) {
		return codex.WithFileLock(dst, write)
	}
	return write()
}

func atomicWriteAuthFile(dst string, data []byte) error {
	if info, err := os.Lstat(dst); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return os.WriteFile(dst, data, 0o600)
	}
	dir := filepath.Dir(dst)
	prefix := "." + filepath.Base(dst)
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	tmp, errCreate := os.CreateTemp(dir, prefix+".tmp-*")
	if errCreate != nil {
		return errCreate
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if errChmod := tmp.Chmod(0o600); errChmod != nil {
		cleanup()
		return errChmod
	}
	if _, errWrite := tmp.Write(data); errWrite != nil {
		cleanup()
		return errWrite
	}
	if errSync := tmp.Sync(); errSync != nil {
		cleanup()
		return errSync
	}
	if errClose := tmp.Close(); errClose != nil {
		_ = os.Remove(tmpName)
		return errClose
	}
	if errRename := os.Rename(tmpName, dst); errRename != nil {
		_ = os.Remove(tmpName)
		return errRename
	}
	return nil
}

func (h *Handler) shouldLockCodexAuthPath(path string) bool {
	if h == nil || h.authManager == nil {
		return false
	}
	for _, existing := range h.authManager.List() {
		if coreauth.IsCodexAuthRecord(existing) && sameManagedAuthFilePath(path, authFilePath(existing)) {
			return true
		}
	}
	return false
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

	targetPath := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
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
	h.removeAuthsForPath(ctx, targetPath, targetID)
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
	if h != nil && h.cfg != nil {
		authDir := strings.TrimSpace(h.cfg.AuthDir)
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

// pluginOwnershipRecorder wraps a plugin auth parser and remembers whether a
// plugin claimed the file, so the caller can keep the plugin-assigned auth ID.
type pluginOwnershipRecorder struct {
	parser  synthesizer.PluginAuthParser
	claimed bool
}

func (r *pluginOwnershipRecorder) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	auth, handled, err := r.parser.ParseAuth(ctx, req)
	if err == nil && handled {
		r.claimed = true
	}
	return auth, handled, err
}

func (r *pluginOwnershipRecorder) ParseAuths(ctx context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	if multi, ok := r.parser.(synthesizer.PluginMultiAuthParser); ok {
		auths, handled, err := multi.ParseAuths(ctx, req)
		if err == nil && handled {
			r.claimed = true
		}
		return auths, handled, err
	}
	auth, handled, err := r.ParseAuth(ctx, req)
	if err != nil || !handled || auth == nil {
		return nil, handled, err
	}
	return []*coreauth.Auth{auth}, true, nil
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
	if h != nil && h.cfg != nil {
		sctx := &synthesizer.SynthesisContext{
			Config:      h.cfg,
			AuthDir:     h.cfg.AuthDir,
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

// buildAuthFromFileDataWithParser keeps the historical single-record API for callers that need it.
func (h *Handler) buildAuthFromFileDataWithParser(path string, data []byte, parser synthesizer.PluginAuthParser) (*coreauth.Auth, error) {
	auths, err := h.buildAuthRecordsFromFileDataWithParser(path, data, parser)
	if err != nil || len(auths) == 0 {
		return nil, err
	}
	return auths[0], nil
}

// buildAuthRecordsFromFileDataWithParser preserves every record returned by a
// plugin-owned multi-auth file. A handled-empty plugin result deliberately
// returns no records and must not fall back to a path-derived credential.
func (h *Handler) buildAuthRecordsFromFileDataWithParser(path string, data []byte, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, error) {
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
	metadata, err := parseAuthFileMetadata(data)
	if err != nil {
		return nil, err
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
	var generated []*coreauth.Auth
	var recorder *pluginOwnershipRecorder
	if h != nil && h.cfg != nil {
		sctx := &synthesizer.SynthesisContext{
			Config:            h.cfg,
			AuthDir:           h.cfg.AuthDir,
			Now:               time.Now(),
			IDGenerator:       synthesizer.NewStableIDGenerator(),
			StrictFileParsing: true,
		}
		if parser != nil {
			recorder = &pluginOwnershipRecorder{parser: parser}
			sctx.PluginAuthParser = recorder
		}
		var errSynthesize error
		generated, errSynthesize = synthesizer.SynthesizeAuthFile(sctx, path, data)
		if errSynthesize != nil {
			return nil, fmt.Errorf("%w: %w", errInvalidAuthFile, errSynthesize)
		}
	}
	if recorder != nil && recorder.claimed {
		out := make([]*coreauth.Auth, 0, len(generated))
		for index, generatedAuth := range generated {
			if generatedAuth == nil {
				continue
			}
			if strings.TrimSpace(generatedAuth.ID) == "" {
				if len(generated) == 1 {
					generatedAuth.ID = authID
				} else {
					generatedAuth.ID = fmt.Sprintf("%s#%d", authID, index)
				}
			}
			h.prepareManagedAuthRecord(generatedAuth, path, authID, hasLastRefresh, lastRefresh, true)
			out = append(out, generatedAuth.Clone())
		}
		return out, nil
	}

	var auth *coreauth.Auth
	for _, generatedAuth := range generated {
		if generatedAuth != nil {
			auth = generatedAuth.Clone()
			break
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
	h.prepareManagedAuthRecord(auth, path, authID, hasLastRefresh, lastRefresh, false)
	return []*coreauth.Auth{auth}, nil
}

func (h *Handler) prepareManagedAuthRecord(auth *coreauth.Auth, path, fallbackID string, hasLastRefresh bool, lastRefresh time.Time, preserveID bool) {
	if !preserveID || strings.TrimSpace(auth.ID) == "" {
		auth.ID = fallbackID
	}
	if !preserveID || auth.FileName == "" {
		auth.FileName = filepath.Base(path)
	}
	if hasLastRefresh {
		auth.LastRefreshedAt = lastRefresh
	}
	if h != nil && h.authManager != nil {
		if existing, ok := h.authManager.GetByID(auth.ID); ok {
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

func (h *Handler) upsertAuthRecords(ctx context.Context, auths []*coreauth.Auth) error {
	for _, auth := range auths {
		if err := h.upsertAuthRecord(ctx, auth); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) removeConflictingFileAuthsAtPath(ctx context.Context, path string, keepIDs map[string]struct{}) {
	if h == nil || h.authManager == nil || path == "" {
		return
	}
	for _, existing := range h.authManager.List() {
		if existing == nil || existing.AuthSourceKind() != coreauth.AuthSourceFile {
			continue
		}
		if _, keep := keepIDs[existing.ID]; keep {
			continue
		}
		if sameManagedAuthFilePath(path, authFilePath(existing)) {
			h.authManager.Remove(ctx, existing.ID)
		}
	}
}

func parseAuthFileMetadata(data []byte) (map[string]any, error) {
	metadata := make(map[string]any)
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidAuthFile, err)
	}
	coreauth.NormalizeCredentialMetadata(metadata)
	if _, errExcluded := synthesizer.ExtractExcludedModelsFromMetadata(metadata); errExcluded != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidAuthFile, errExcluded)
	}
	return metadata, nil
}

func authFilePath(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	for _, key := range []string{coreauth.AttributePath, coreauth.AttributeVirtualSource, coreauth.AttributeSource} {
		value := strings.TrimSpace(auth.Attributes[key])
		if value != "" && value != coreauth.AuthSourceFile {
			return value
		}
	}
	return ""
}

// sameManagedAuthFilePath matches v8 source identities through symlink aliases.
func sameManagedAuthFilePath(left, right string) bool {
	canonical := func(path string) string {
		if strings.TrimSpace(path) == "" {
			return ""
		}
		if real, errReal := filepath.EvalSymlinks(path); errReal == nil {
			return real
		}
		if parent, errParent := filepath.EvalSymlinks(filepath.Dir(path)); errParent == nil {
			return filepath.Join(parent, filepath.Base(path))
		}
		return path
	}
	return sameAuthFilePath(canonical(left), canonical(right))
}

func (h *Handler) removeAuthsForPathV8(ctx context.Context, path string) {
	if h == nil || h.authManager == nil {
		return
	}
	removed := false
	for _, auth := range h.authManager.List() {
		if auth != nil && (sameManagedAuthFilePath(authAttribute(auth, "path"), path) || sameManagedAuthFilePath(authAttribute(auth, coreauth.AttributeVirtualSource), path)) {
			h.removeAuth(ctx, auth.ID)
			removed = true
		}
	}
	if !removed {
		h.removeAuth(ctx, path)
	}
}
