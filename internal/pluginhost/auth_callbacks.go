package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type rpcHostAuthGetRequest struct {
	AuthIndex string `json:"auth_index"`
}

type rpcHostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type rpcHostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name,omitempty"`
	Path      string          `json:"path,omitempty"`
	JSON      json.RawMessage `json:"json"`
}

func (h *Host) SetAuthManager(manager *coreauth.Manager) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.authManager = manager
	h.mu.Unlock()
}

func (h *Host) currentAuthManager() *coreauth.Manager {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	return manager
}

func (h *Host) callHostAuthList(ctx context.Context, request []byte) ([]byte, error) {
	_ = ctx
	if len(bytesTrimSpace(request)) > 0 {
		var req map[string]any
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode host auth list request: %w", errUnmarshal)
		}
	}
	entries, errList := h.listAuthFiles()
	if errList != nil {
		return nil, errList
	}
	return marshalRPCResult(rpcHostAuthListResponse{Files: entries})
}

func (h *Host) callHostAuthGet(ctx context.Context, request []byte) ([]byte, error) {
	_ = ctx
	var req rpcHostAuthGetRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth get request: %w", errUnmarshal)
	}
	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("auth_index is required")
	}
	auth, rawJSON, errGet := h.authPhysicalJSONByIndex(authIndex)
	if errGet != nil {
		return nil, errGet
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = strings.TrimSpace(auth.ID)
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	return marshalRPCResult(rpcHostAuthGetResponse{
		AuthIndex: authIndex,
		Name:      name,
		Path:      path,
		JSON:      json.RawMessage(rawJSON),
	})
}

func (h *Host) callHostAuthGetRuntime(ctx context.Context, request []byte) ([]byte, error) {
	_ = ctx
	var req rpcHostAuthGetRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth get runtime request: %w", errUnmarshal)
	}
	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("auth_index is required")
	}
	auth, errGet := h.authByIndex(authIndex)
	if errGet != nil {
		return nil, errGet
	}
	entry := h.buildHostAuthFileEntry(auth)
	if entry == nil {
		return nil, fmt.Errorf("auth runtime info not found for auth_index %s", authIndex)
	}
	return marshalRPCResult(pluginapi.HostAuthGetRuntimeResponse{Auth: *entry})
}

func (h *Host) callHostAuthSave(ctx context.Context, request []byte) ([]byte, error) {
	var req pluginapi.HostAuthSaveRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth save request: %w", errUnmarshal)
	}
	name, rawJSON, errValidate := validateHostAuthSaveRequest(req)
	if errValidate != nil {
		return nil, errValidate
	}
	path, errSave := h.saveAuthFile(ctx, name, rawJSON)
	if errSave != nil {
		return nil, errSave
	}
	return marshalRPCResult(pluginapi.HostAuthSaveResponse{
		Name: name,
		Path: path,
	})
}

func (h *Host) callHostAuthSaveBatch(ctx context.Context, request []byte) ([]byte, error) {
	var req pluginapi.HostAuthSaveBatchRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth save_batch request: %w", errUnmarshal)
	}
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("files are required")
	}
	type validatedAuthFile struct {
		name string
		data []byte
	}
	validated := make([]validatedAuthFile, 0, len(req.Files))
	seenNames := make(map[string]struct{}, len(req.Files))
	for _, file := range req.Files {
		name, rawJSON, errValidate := validateHostAuthSaveRequest(file)
		if errValidate != nil {
			return nil, errValidate
		}
		if _, exists := seenNames[strings.ToLower(name)]; exists {
			return nil, fmt.Errorf("duplicate auth file name %s", name)
		}
		seenNames[strings.ToLower(name)] = struct{}{}
		validated = append(validated, validatedAuthFile{name: name, data: rawJSON})
	}
	files := make([]pluginapi.HostAuthSaveResponse, 0, len(validated))
	for index, file := range validated {
		path, errSave := h.saveAuthFile(ctx, file.name, file.data)
		if errSave != nil {
			return nil, fmt.Errorf("save auth file %s (%d of %d already saved): %w", file.name, index, len(validated), errSave)
		}
		files = append(files, pluginapi.HostAuthSaveResponse{Name: file.name, Path: path})
	}
	return marshalRPCResult(pluginapi.HostAuthSaveBatchResponse{Files: files})
}

func (h *Host) callHostAuthDelete(ctx context.Context, request []byte) ([]byte, error) {
	var req pluginapi.HostAuthDeleteRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth delete request: %w", errUnmarshal)
	}
	auth, path, name, errResolve := h.resolveAuthForDelete(req)
	if errResolve != nil {
		return nil, errResolve
	}
	store := sdkAuth.GetTokenStore()
	if store == nil {
		return nil, fmt.Errorf("token store unavailable")
	}
	if errDelete := store.Delete(ctx, path); errDelete != nil {
		return nil, fmt.Errorf("delete auth file: %w", errDelete)
	}
	targetID := ""
	if auth != nil {
		targetID = strings.TrimSpace(auth.ID)
	}
	h.removeAuthsForPath(ctx, path, targetID)
	if name == "" {
		name = filepath.Base(path)
	}
	return marshalRPCResult(pluginapi.HostAuthDeleteResponse{
		Name: name,
		Path: path,
	})
}

func (h *Host) resolveAuthForDelete(req pluginapi.HostAuthDeleteRequest) (*coreauth.Auth, string, string, error) {
	authIndex := strings.TrimSpace(req.AuthIndex)
	name := strings.TrimSpace(req.Name)
	if authIndex == "" && name == "" {
		return nil, "", "", fmt.Errorf("auth_index or name is required")
	}
	if authIndex != "" {
		auth, errGet := h.authByIndex(authIndex)
		if errGet != nil {
			return nil, "", "", errGet
		}
		path := strings.TrimSpace(authAttribute(auth, "path"))
		if path == "" {
			return nil, "", "", fmt.Errorf("auth file path not found for auth_index %s", authIndex)
		}
		if errGuard := guardVirtualAuthDelete(auth, ""); errGuard != nil {
			return nil, "", "", errGuard
		}
		return auth, path, deleteNameForAuth(auth, path), nil
	}
	manager := h.currentAuthManager()
	if manager != nil {
		if auth, ok := manager.GetByID(name); ok && auth != nil {
			path := strings.TrimSpace(authAttribute(auth, "path"))
			if path == "" {
				return nil, "", "", fmt.Errorf("auth file path not found for name %s", name)
			}
			if errGuard := guardVirtualAuthDelete(auth, name); errGuard != nil {
				return nil, "", "", errGuard
			}
			return auth, path, deleteNameForDelete(auth, path), nil
		}
		for _, auth := range manager.List() {
			if auth == nil {
				continue
			}
			if strings.TrimSpace(auth.FileName) == name || filepath.Base(strings.TrimSpace(authAttribute(auth, "path"))) == name {
				path := strings.TrimSpace(authAttribute(auth, "path"))
				if path == "" {
					return nil, "", "", fmt.Errorf("auth file path not found for name %s", name)
				}
				if errGuard := guardVirtualAuthDelete(auth, name); errGuard != nil {
					return nil, "", "", errGuard
				}
				return auth, path, deleteNameForDelete(auth, path), nil
			}
		}
	}
	if isUnsafeAuthFileName(name) {
		return nil, "", "", fmt.Errorf("invalid auth file name")
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return nil, "", "", fmt.Errorf("auth file name must end with .json")
	}
	authDir := h.resolvedAuthDir()
	if authDir == "" {
		return nil, "", "", fmt.Errorf("auth directory is unavailable")
	}
	path := filepath.Join(authDir, filepath.Base(name))
	if !filepath.IsAbs(path) {
		if abs, errAbs := filepath.Abs(path); errAbs == nil {
			path = abs
		}
	}
	if _, errStat := os.Stat(path); errStat != nil {
		if os.IsNotExist(errStat) {
			return nil, "", "", fmt.Errorf("auth file not found for name %s", name)
		}
		return nil, "", "", fmt.Errorf("failed to stat auth file: %w", errStat)
	}
	return nil, path, filepath.Base(name), nil
}

// guardVirtualAuthDelete rejects deleting an individual plugin-virtual auth.
// Virtual auths expanded from one plugin-owned source file share the same backing
// path, so deleting one child would remove the source file and every sibling.
// Selecting the physical source file name deletes the whole expansion, matching
// the management deletion flow.
func guardVirtualAuthDelete(auth *coreauth.Auth, selectedName string) error {
	if !coreauth.IsPluginVirtualAuth(auth) {
		return nil
	}
	sourcePath := strings.TrimSpace(authAttribute(auth, coreauth.AttributeVirtualSource))
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(authAttribute(auth, "path"))
	}
	// Only the physical source file name itself selects the whole expansion.
	// An expanded auth ID may contain directory components (for example
	// "nested/source.json") whose basename collides with the source name, so a
	// selector carrying path components never matches the physical source.
	if sourcePath != "" && selectedName != "" &&
		!strings.ContainsAny(selectedName, "/\\") &&
		strings.EqualFold(selectedName, filepath.Base(sourcePath)) {
		return nil
	}
	source := filepath.Base(sourcePath)
	if source == "" || source == "." {
		source = "its source file"
	}
	return fmt.Errorf("auth %s is expanded from a plugin-owned source file; delete %s instead", strings.TrimSpace(auth.ID), source)
}

func deleteNameForAuth(auth *coreauth.Auth, path string) string {
	if auth != nil {
		if name := strings.TrimSpace(auth.FileName); name != "" {
			return name
		}
		if id := strings.TrimSpace(auth.ID); id != "" {
			return id
		}
	}
	return filepath.Base(path)
}

// deleteNameForDelete reports the credential identity a delete actually removed.
// A virtual auth deleted through its physical source name removes the whole
// expansion, so report the source file identity rather than the matched child.
func deleteNameForDelete(auth *coreauth.Auth, path string) string {
	if coreauth.IsPluginVirtualAuth(auth) {
		return filepath.Base(path)
	}
	return deleteNameForAuth(auth, path)
}

func (h *Host) removeAuth(ctx context.Context, id string) {
	if h == nil {
		return
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	if _, ok := manager.GetByID(id); ok {
		manager.Remove(ctx, id)
		return
	}
	authID := h.authIDForPath(id)
	if authID == "" {
		return
	}
	manager.Remove(ctx, authID)
}

func (h *Host) removeAuthsForPath(ctx context.Context, path string, fallbackID string) {
	if h == nil {
		return
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return
	}
	removed := false
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		if sameAuthFilePath(authAttribute(auth, "path"), path) {
			h.removeAuth(ctx, auth.ID)
			removed = true
		}
	}
	if removed {
		return
	}
	if strings.TrimSpace(fallbackID) != "" {
		h.removeAuth(ctx, fallbackID)
		return
	}
	h.removeAuth(ctx, path)
}

func sameAuthFilePath(left, right string) bool {
	left = cleanAuthFilePath(left)
	right = cleanAuthFilePath(right)
	if left == "" || right == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func cleanAuthFilePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, errAbs := filepath.Abs(path); errAbs == nil && strings.TrimSpace(abs) != "" {
		path = abs
	}
	return filepath.Clean(path)
}

func (h *Host) listAuthFiles() ([]pluginapi.HostAuthFileEntry, error) {
	manager := h.currentAuthManager()
	if manager != nil {
		auths := manager.List()
		entries := make([]pluginapi.HostAuthFileEntry, 0, len(auths))
		for _, auth := range auths {
			if entry := h.buildHostAuthFileEntry(auth); entry != nil {
				entries = append(entries, *entry)
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
		})
		return entries, nil
	}
	return h.listAuthFilesFromDisk()
}

func (h *Host) listAuthFilesFromDisk() ([]pluginapi.HostAuthFileEntry, error) {
	authDir := h.resolvedAuthDir()
	if authDir == "" {
		return nil, fmt.Errorf("auth directory is unavailable")
	}
	entries, errReadDir := os.ReadDir(authDir)
	if errReadDir != nil {
		return nil, fmt.Errorf("failed to read auth dir: %w", errReadDir)
	}
	files := make([]pluginapi.HostAuthFileEntry, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		full := filepath.Join(authDir, name)
		fileEntry := pluginapi.HostAuthFileEntry{
			Name:   name,
			Source: "file",
			Path:   full,
		}
		if info, errInfo := entry.Info(); errInfo == nil {
			fileEntry.Size = info.Size()
			fileEntry.ModTime = info.ModTime()
		}
		if data, errRead := os.ReadFile(full); errRead == nil {
			var metadata map[string]any
			if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal == nil {
				if provider, ok := metadata["type"].(string); ok {
					fileEntry.Type = strings.TrimSpace(provider)
					fileEntry.Provider = fileEntry.Type
				}
				if email, ok := metadata["email"].(string); ok {
					fileEntry.Email = strings.TrimSpace(email)
				}
				if projectID, ok := metadata["project_id"].(string); ok {
					fileEntry.ProjectID = strings.TrimSpace(projectID)
				}
				if rawPriority, ok := metadata["priority"]; ok {
					if priority, okPriority := parsePriorityValue(rawPriority); okPriority {
						fileEntry.Priority = priority
					}
				}
				if note, ok := metadata["note"].(string); ok {
					fileEntry.Note = strings.TrimSpace(note)
				}
				if websockets, okWebsockets := parseWebsocketsValue(metadata["websockets"]); okWebsockets {
					fileEntry.Websockets = websockets
				}
			}
		}
		files = append(files, fileEntry)
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	return files, nil
}

func (h *Host) authByIndex(authIndex string) (*coreauth.Auth, error) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("auth_index is required")
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return nil, fmt.Errorf("core auth manager unavailable")
	}
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		auth.EnsureIndex()
		if auth.Index == authIndex {
			return auth, nil
		}
	}
	return nil, fmt.Errorf("auth not found for auth_index %s", authIndex)
}

func (h *Host) authPhysicalJSONByIndex(authIndex string) (*coreauth.Auth, []byte, error) {
	auth, errGet := h.authByIndex(authIndex)
	if errGet != nil {
		return nil, nil, errGet
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" {
		return nil, nil, fmt.Errorf("auth file path not found for auth_index %s", authIndex)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil, nil, fmt.Errorf("auth file not found for auth_index %s", authIndex)
		}
		return nil, nil, fmt.Errorf("failed to read auth file: %w", errRead)
	}
	if len(bytesTrimSpace(data)) == 0 {
		return nil, nil, fmt.Errorf("auth file is empty for auth_index %s", authIndex)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		return nil, nil, fmt.Errorf("invalid auth file for auth_index %s: %w", authIndex, errUnmarshal)
	}
	return auth, data, nil
}

func validateHostAuthSaveRequest(req pluginapi.HostAuthSaveRequest) (string, []byte, error) {
	name := strings.TrimSpace(req.Name)
	if isUnsafeAuthFileName(name) {
		return "", nil, fmt.Errorf("invalid auth file name")
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return "", nil, fmt.Errorf("auth file name must end with .json")
	}
	rawJSON := bytesTrimSpace(req.JSON)
	if len(rawJSON) == 0 {
		return "", nil, fmt.Errorf("json is required")
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(rawJSON, &metadata); errUnmarshal != nil {
		return "", nil, fmt.Errorf("invalid auth json: %w", errUnmarshal)
	}
	return filepath.Base(name), rawJSON, nil
}

func (h *Host) saveAuthFile(ctx context.Context, name string, data []byte) (string, error) {
	authDir := h.resolvedAuthDir()
	if authDir == "" {
		return "", fmt.Errorf("auth directory is unavailable")
	}
	dst := filepath.Join(authDir, filepath.Base(name))
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}
	auth, errBuild := h.buildAuthFromFileData(dst, data)
	if errBuild != nil {
		return "", errBuild
	}
	if errWrite := os.WriteFile(dst, data, 0o600); errWrite != nil {
		return "", fmt.Errorf("failed to write auth file: %w", errWrite)
	}
	if errUpsert := h.upsertAuthRecord(ctx, auth); errUpsert != nil {
		return "", errUpsert
	}
	return dst, nil
}

func (h *Host) buildAuthFromFileData(path string, data []byte) (*coreauth.Auth, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("auth path is empty")
	}
	if data == nil {
		var errRead error
		data, errRead = os.ReadFile(path)
		if errRead != nil {
			return nil, fmt.Errorf("failed to read auth file: %w", errRead)
		}
	}
	metadata := make(map[string]any)
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		return nil, fmt.Errorf("invalid auth file: %w", errUnmarshal)
	}
	coreauth.NormalizeCredentialMetadata(metadata)
	provider, _ := metadata["type"].(string)
	if strings.TrimSpace(provider) == "" {
		provider = "unknown"
	}
	label := provider
	if email, ok := metadata["email"].(string); ok && strings.TrimSpace(email) != "" {
		label = strings.TrimSpace(email)
	}
	authID := h.authIDForPath(path)
	if authID == "" {
		authID = path
	}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: provider,
		FileName: filepath.Base(path),
		Label:    label,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":   path,
			"source": path,
		},
		Metadata:  metadata,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if manager := h.currentAuthManager(); manager != nil {
		if existing, ok := manager.GetByID(authID); ok {
			auth.CreatedAt = existing.CreatedAt
			auth.LastRefreshedAt = existing.LastRefreshedAt
			auth.NextRetryAfter = existing.NextRetryAfter
			auth.Runtime = existing.Runtime
		}
	}
	if errWeight := coreauth.ValidateAuthWeight(auth); errWeight != nil {
		return nil, fmt.Errorf("invalid auth weight: %w", errWeight)
	}
	coreauth.ApplyCustomHeadersFromMetadata(auth)
	return auth, nil
}

func (h *Host) upsertAuthRecord(ctx context.Context, auth *coreauth.Auth) error {
	manager := h.currentAuthManager()
	if manager == nil || auth == nil {
		return nil
	}
	if existing, ok := manager.GetByID(auth.ID); ok {
		auth.CreatedAt = existing.CreatedAt
		_, errUpdate := manager.Update(ctx, auth)
		return errUpdate
	}
	_, errRegister := manager.Register(ctx, auth)
	return errRegister
}

func isUnsafeAuthFileName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return true
	}
	if strings.ContainsAny(name, "/\\") {
		return true
	}
	if filepath.VolumeName(name) != "" {
		return true
	}
	return false
}

func (h *Host) buildHostAuthFileEntry(auth *coreauth.Auth) *pluginapi.HostAuthFileEntry {
	if auth == nil {
		return nil
	}
	auth.EnsureIndex()
	runtimeOnly := isRuntimeOnlyAuth(auth)
	if runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
		return nil
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" && !runtimeOnly {
		return nil
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	entry := &pluginapi.HostAuthFileEntry{
		ID:             auth.ID,
		AuthIndex:      auth.Index,
		Name:           name,
		Type:           strings.TrimSpace(auth.Provider),
		Provider:       strings.TrimSpace(auth.Provider),
		Label:          auth.Label,
		Status:         string(auth.Status),
		StatusMessage:  auth.StatusMessage,
		Disabled:       auth.Disabled,
		Unavailable:    auth.Unavailable,
		RuntimeOnly:    runtimeOnly,
		Source:         "memory",
		Success:        auth.Success,
		Failed:         auth.Failed,
		RecentRequests: hostRecentRequests(auth),
	}
	if email := authEmail(auth); email != "" {
		entry.Email = email
	}
	if projectID := authProjectID(auth); projectID != "" {
		entry.ProjectID = projectID
	}
	if accountType, account := auth.AccountInfo(); accountType != "" || account != "" {
		entry.AccountType = accountType
		entry.Account = account
	}
	if !auth.CreatedAt.IsZero() {
		entry.CreatedAt = auth.CreatedAt
	}
	if !auth.UpdatedAt.IsZero() {
		entry.ModTime = auth.UpdatedAt
		entry.UpdatedAt = auth.UpdatedAt
	}
	if !auth.LastRefreshedAt.IsZero() {
		entry.LastRefresh = auth.LastRefreshedAt
	}
	if !auth.NextRetryAfter.IsZero() {
		entry.NextRetryAfter = auth.NextRetryAfter
	}
	if path != "" {
		entry.Path = path
		entry.Source = "file"
		if info, err := os.Stat(path); err == nil {
			entry.Size = info.Size()
			entry.ModTime = info.ModTime()
		} else if os.IsNotExist(err) {
			if !runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled || strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "removed via management api")) {
				return nil
			}
			entry.Source = "memory"
		}
	}
	if p := strings.TrimSpace(authAttribute(auth, "priority")); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			entry.Priority = parsed
		}
	} else if auth.Metadata != nil {
		if rawPriority, ok := auth.Metadata["priority"]; ok {
			if priority, okPriority := parsePriorityValue(rawPriority); okPriority {
				entry.Priority = priority
			}
		}
	}
	if note := strings.TrimSpace(authAttribute(auth, "note")); note != "" {
		entry.Note = note
	} else if auth.Metadata != nil {
		if rawNote, ok := auth.Metadata["note"].(string); ok {
			entry.Note = strings.TrimSpace(rawNote)
		}
	}
	if websockets, ok := authWebsocketsValue(auth); ok {
		entry.Websockets = websockets
	}
	return entry
}

func (h *Host) resolvedAuthDir() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	authDir := ""
	if h.runtimeConfig != nil {
		authDir = strings.TrimSpace(h.runtimeConfig.AuthDir)
	}
	h.mu.Unlock()
	if authDir == "" {
		return ""
	}
	authDir = filepath.Clean(authDir)
	if !filepath.IsAbs(authDir) {
		if abs, errAbs := filepath.Abs(authDir); errAbs == nil {
			authDir = abs
		}
	}
	return authDir
}

func (h *Host) authIDForPath(path string) string {
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
	if authDir := h.resolvedAuthDir(); authDir != "" {
		if rel, errRel := filepath.Rel(authDir, path); errRel == nil && rel != "" {
			id = rel
		}
	}
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func authEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["email"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["email"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(auth.Attributes["account_email"]); v != "" {
			return v
		}
	}
	return ""
}

func authProjectID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["project_id"].(string); ok {
			if projectID := strings.TrimSpace(v); projectID != "" {
				return projectID
			}
		}
	}
	if auth.Attributes != nil {
		if projectID := strings.TrimSpace(auth.Attributes["project_id"]); projectID != "" {
			return projectID
		}
	}
	return ""
}

func authAttribute(auth *coreauth.Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return auth.Attributes[key]
}

func isRuntimeOnlyAuth(auth *coreauth.Auth) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}

func authWebsocketsValue(auth *coreauth.Auth) (bool, bool) {
	if auth == nil {
		return false, false
	}
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed, true
			}
		}
	}
	if auth.Metadata == nil {
		return false, false
	}
	return parseWebsocketsValue(auth.Metadata["websockets"])
}

func parsePriorityValue(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func parseWebsocketsValue(raw any) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func hostRecentRequests(auth *coreauth.Auth) []pluginapi.HostRecentRequestEntry {
	if auth == nil {
		return nil
	}
	snapshot := auth.RecentRequestsSnapshot(time.Now())
	if len(snapshot) == 0 {
		return nil
	}
	out := make([]pluginapi.HostRecentRequestEntry, 0, len(snapshot))
	for _, entry := range snapshot {
		out = append(out, pluginapi.HostRecentRequestEntry{
			Time:    entry.Time,
			Success: entry.Success,
			Failed:  entry.Failed,
		})
	}
	return out
}
