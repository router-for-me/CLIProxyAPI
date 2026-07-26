package management

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	codexexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

func warningFilterEnabled(c *gin.Context) bool {
	if c == nil {
		return false
	}
	for _, key := range []string{"health_status", "health"} {
		value := strings.ToLower(strings.TrimSpace(c.Query(key)))
		if value == "warning" || value == "warn" || value == "1" || value == "true" || value == "yes" || value == "on" {
			return true
		}
	}
	return false
}

func authHasWarning(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Unavailable {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(string(auth.Status)))
	if status == string(coreauth.StatusError) || status == string(coreauth.StatusUnknown) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(auth.StatusMessage))
	if msg != "" {
		needles := []string{
			"warning",
			"error",
			"invalid",
			"invalid_request_error",
			"token_invalidated",
			"unauthorized",
			"401",
			"payment_required",
			"quota",
			"request failed",
		}
		for _, needle := range needles {
			if strings.Contains(msg, needle) {
				return true
			}
		}
	}
	if auth.LastError != nil {
		if auth.LastError.HTTPStatus >= 400 {
			return true
		}
		lastErrText := strings.ToLower(strings.TrimSpace(auth.LastError.Message + " " + auth.LastError.Code))
		if strings.Contains(lastErrText, "token_invalidated") || strings.Contains(lastErrText, "invalid_request_error") || strings.Contains(lastErrText, "401") {
			return true
		}
	}
	return false
}

func (h *Handler) ListCodexRefreshAuthFiles(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}
	auths := h.authManager.List()
	files := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		if entry := h.buildAuthFileEntry(auth); entry != nil {
			files = append(files, entry)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		nameI, _ := files[i]["name"].(string)
		nameJ, _ := files[j]["name"].(string)
		return strings.ToLower(nameI) < strings.ToLower(nameJ)
	})
	c.JSON(http.StatusOK, gin.H{"files": files})
}

func codexAccountIDForAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if accountID, ok := auth.Metadata["account_id"].(string); ok {
			if trimmed := strings.TrimSpace(accountID); trimmed != "" {
				return trimmed
			}
		}
	}
	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		if accountID, ok := claims["chatgpt_account_id"].(string); ok {
			return strings.TrimSpace(accountID)
		}
	}
	return ""
}

func (h *Handler) deleteAuthFileByResolvedTarget(ctx context.Context, targetPath string, targetID string, deletedName string) (string, int, error) {
	if deletedName == "" {
		deletedName = filepath.Base(targetPath)
	}
	if errRemove := os.Remove(targetPath); errRemove != nil {
		if os.IsNotExist(errRemove) {
			return deletedName, http.StatusNotFound, errAuthFileNotFound
		}
		return deletedName, http.StatusInternalServerError, fmt.Errorf("failed to remove file: %w", errRemove)
	}
	if errDeleteRecord := h.deleteTokenRecord(ctx, targetPath); errDeleteRecord != nil {
		return deletedName, http.StatusInternalServerError, errDeleteRecord
	}
	h.removeAuthsForPath(ctx, targetPath, targetID)
	return deletedName, http.StatusOK, nil
}

func (h *Handler) SyncCodexQuotaMetadata(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name      string         `json:"name"`
		AuthIndex string         `json:"auth_index"`
		AccountID string         `json:"account_id"`
		Payload   map[string]any `json:"payload"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if len(req.Payload) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payload is required"})
		return
	}

	ctx := c.Request.Context()
	accountID := strings.TrimSpace(req.AccountID)
	name := strings.TrimSpace(req.Name)
	authIndex := strings.TrimSpace(req.AuthIndex)

	var targetAuth *coreauth.Auth
	if name != "" {
		if auth, ok := h.authManager.GetByID(name); ok {
			targetAuth = auth
		} else {
			for _, auth := range h.authManager.List() {
				if auth != nil && strings.TrimSpace(auth.FileName) == name {
					targetAuth = auth
					break
				}
			}
		}
	}
	if targetAuth == nil {
		for _, auth := range h.authManager.List() {
			if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
				continue
			}
			auth.EnsureIndex()
			if authIndex != "" && strings.EqualFold(strings.TrimSpace(auth.Index), authIndex) {
				targetAuth = auth
				break
			}
			if accountID != "" && strings.EqualFold(codexAccountIDForAuth(auth), accountID) {
				targetAuth = auth
				break
			}
		}
	}
	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "codex auth not found"})
		return
	}

	if targetAuth.Metadata == nil {
		targetAuth.Metadata = make(map[string]any)
	}
	now := time.Now().UTC()
	targetAuth.Metadata["codex_quota"] = req.Payload
	targetAuth.Metadata["last_refresh"] = now.Format(time.RFC3339)
	targetAuth.LastRefreshedAt = now
	targetAuth.UpdatedAt = now

	if _, err := h.authManager.Update(ctx, targetAuth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"auth_id":    targetAuth.ID,
		"auth_index": targetAuth.Index,
	})
}

type codexFreeRefreshTask struct {
	mu      sync.Mutex
	total   int
	done    int
	running bool
	results []codexFreeRefreshResult
}

// codexFreeRefreshResult holds the outcome for a single account refresh.
type codexFreeRefreshResult struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type codexFreeRefreshRequest struct {
	AuthIndex      string   `json:"auth_index"`
	AuthIndexCamel string   `json:"authIndex"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	FileName       string   `json:"file_name"`
	Email          string   `json:"email"`
	AuthIndices    []string `json:"auth_indices"`
}

var (
	codexFreeRefreshTasks   = make(map[string]*codexFreeRefreshTask)
	codexFreeRefreshTasksMu sync.Mutex
	codexFreeRefreshCounter int
)

// isCodexFreePlanAuth checks whether an auth record represents a Codex free-plan account.
func isCodexFreePlanAuth(auth *coreauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["plan_type"]), "free")
}

func codexRefreshSelectorValue(req codexFreeRefreshRequest) string {
	for _, value := range []string{req.AuthIndex, req.AuthIndexCamel, req.ID, req.Name, req.FileName, req.Email} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func authMatchesCodexRefreshSelector(auth *coreauth.Auth, selector string) bool {
	if auth == nil || selector == "" {
		return false
	}
	auth.EnsureIndex()
	candidates := []string{
		auth.Index,
		auth.ID,
		auth.FileName,
		filepath.Base(auth.FileName),
		auth.Label,
		authEmail(auth),
	}
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate), selector) {
			return true
		}
	}
	return false
}

func (h *Handler) selectCodexRefreshTarget(selector string) (*coreauth.Auth, int, string) {
	if h == nil || h.authManager == nil {
		return nil, http.StatusInternalServerError, "handler not initialized"
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, http.StatusBadRequest, "missing auth selector"
	}
	matches := make([]*coreauth.Auth, 0, 1)
	for _, auth := range h.authManager.List() {
		if !authMatchesCodexRefreshSelector(auth, selector) {
			continue
		}
		matches = append(matches, auth)
	}
	if len(matches) == 0 {
		return nil, http.StatusNotFound, "auth not found"
	}
	if len(matches) > 1 {
		return nil, http.StatusBadRequest, "auth selector is ambiguous"
	}
	auth := matches[0]
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return nil, http.StatusBadRequest, "auth is not codex"
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return nil, http.StatusBadRequest, "auth is disabled"
	}
	return auth, http.StatusOK, ""
}

// RefreshCodexFreeAccounts starts a background task that sends a minimal chat
// request to each Codex free-plan account in sequence, with random delays
// between accounts to avoid triggering risk control.
func (h *Handler) RefreshCodexFreeAccounts(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	var body codexFreeRefreshRequest
	if c.Request != nil && c.Request.Body != nil {
		if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil && !errors.Is(errBindJSON, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}

	selector := codexRefreshSelectorValue(body)
	var targets []*coreauth.Auth
	if len(body.AuthIndices) > 0 {
		auths := h.authManager.List()
		indexSet := make(map[string]bool, len(body.AuthIndices))
		for _, idx := range body.AuthIndices {
			if trimmed := strings.TrimSpace(idx); trimmed != "" {
				indexSet[trimmed] = true
			}
		}
		for _, a := range auths {
			if !isCodexFreePlanAuth(a) || a.Disabled || a.Status == coreauth.StatusDisabled {
				continue
			}
			a.EnsureIndex()
			if indexSet[a.Index] {
				targets = append(targets, a)
			}
		}
		if len(targets) == 0 {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "no matching codex accounts found", "total": 0})
			return
		}
	} else if selector != "" {
		target, statusCode, errMsg := h.selectCodexRefreshTarget(selector)
		if errMsg != "" {
			c.JSON(statusCode, gin.H{"error": errMsg})
			return
		}
		targets = append(targets, target)
	} else {
		auths := h.authManager.List()
		for _, a := range auths {
			if isCodexFreePlanAuth(a) && !a.Disabled && a.Status != coreauth.StatusDisabled {
				targets = append(targets, a)
			}
		}
		if len(targets) == 0 {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "no free codex accounts found", "total": 0})
			return
		}
	}

	codexFreeRefreshTasksMu.Lock()
	codexFreeRefreshCounter++
	taskID := fmt.Sprintf("codex-free-refresh-%d-%d", time.Now().UnixMilli(), codexFreeRefreshCounter)
	task := &codexFreeRefreshTask{
		total:   len(targets),
		running: true,
		results: make([]codexFreeRefreshResult, 0, len(targets)),
	}
	codexFreeRefreshTasks[taskID] = task
	codexFreeRefreshTasksMu.Unlock()

	go h.runCodexFreeRefresh(taskID, task, targets)

	c.JSON(http.StatusAccepted, gin.H{
		"status":  "started",
		"task_id": taskID,
		"total":   len(targets),
	})
}

// runCodexFreeRefresh processes each target account sequentially.
func (h *Handler) runCodexFreeRefresh(taskID string, task *codexFreeRefreshTask, targets []*coreauth.Auth) {
	defer func() {
		task.mu.Lock()
		task.running = false
		task.mu.Unlock()
	}()

	for i, auth := range targets {
		result := codexFreeRefreshResult{
			Name:  auth.FileName,
			Email: authEmail(auth),
		}

		errPing := h.pingCodexAccount(auth)
		if errPing != nil {
			result.Success = false
			result.Error = errPing.Error()
			log.WithError(errPing).WithField("auth", auth.FileName).Warn("codex free refresh ping failed")
			h.markRefreshPingFailure(auth, "gpt-5.4-mini", errPing)
		} else {
			result.Success = true
			now := time.Now().UTC()
			if auth.Metadata == nil {
				auth.Metadata = make(map[string]any)
			}
			auth.Metadata["last_refresh"] = now.Format(time.RFC3339)
			auth.LastRefreshedAt = now
			auth.UpdatedAt = now
			if _, errUpdate := h.authManager.Update(context.Background(), auth); errUpdate != nil {
				log.WithError(errUpdate).WithField("auth", auth.FileName).Warn("failed to persist last_refresh after codex free ping")
			}
		}

		task.mu.Lock()
		task.results = append(task.results, result)
		task.done = i + 1
		task.mu.Unlock()

		// Random delay between accounts (3-8 seconds) to avoid risk control.
		if i < len(targets)-1 {
			jitter := time.Duration(3000+rand.IntN(5000)) * time.Millisecond
			time.Sleep(jitter)
		}
	}
}

func minimalCodexRefreshPayload() []byte {
	return []byte(`{"model":"gpt-5.4-mini","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true,"store":false,"instructions":""}`)
}

// pingCodexAccount sends a minimal chat request to activate the account cycle.
// It reuses the existing CodexExecutor to ensure all headers, token resolution,
// proxy transport, and request body formatting are identical to normal requests.
func (h *Handler) pingCodexAccount(auth *coreauth.Auth) error {
	executor := codexexecutor.NewCodexExecutor(h.cfg)

	minimalPayload := minimalCodexRefreshPayload()

	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: minimalPayload,
		Format:  sdktranslator.FromString("codex"),
	}
	opts := cliproxyexecutor.Options{
		Stream:       true,
		SourceFormat: sdktranslator.FromString("codex"),
	}

	ctx := context.Background()
	_, errExec := executor.Execute(ctx, auth, req, opts)
	if errExec != nil {
		return fmt.Errorf("codex executor ping failed: %w", errExec)
	}
	return nil
}

// GetRefreshCodexFreeStatus returns the current progress of a refresh task.
func (h *Handler) GetRefreshCodexFreeStatus(c *gin.Context) {
	taskID := c.Param("taskId")
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing taskId"})
		return
	}

	codexFreeRefreshTasksMu.Lock()
	task, ok := codexFreeRefreshTasks[taskID]
	codexFreeRefreshTasksMu.Unlock()

	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	status := "running"
	if !task.running {
		status = "completed"
	}

	successCount := 0
	failCount := 0
	for _, r := range task.results {
		if r.Success {
			successCount++
		} else {
			failCount++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  status,
		"total":   task.total,
		"done":    task.done,
		"success": successCount,
		"failed":  failCount,
		"results": task.results,
	})
}

// isXAIAuth checks whether an auth record represents an xAI account.
func isXAIAuth(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "xai")
}

// xaiRefreshTask tracks the progress of a batch xAI refresh operation.
type xaiRefreshTask struct {
	mu      sync.Mutex
	total   int
	done    int
	running bool
	results []xaiRefreshResult
}

// xaiRefreshResult holds the outcome for a single xAI account refresh.
type xaiRefreshResult struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type xaiRefreshRequest struct {
	AuthIndex      string   `json:"auth_index"`
	AuthIndexCamel string   `json:"authIndex"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	FileName       string   `json:"file_name"`
	Email          string   `json:"email"`
	AuthIndices    []string `json:"auth_indices"`
}

var (
	xaiRefreshTasks   = make(map[string]*xaiRefreshTask)
	xaiRefreshTasksMu sync.Mutex
	xaiRefreshCounter int
)

func xaiRefreshSelectorValue(req xaiRefreshRequest) string {
	for _, value := range []string{req.AuthIndex, req.AuthIndexCamel, req.ID, req.Name, req.FileName, req.Email} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func authMatchesXAIRefreshSelector(auth *coreauth.Auth, selector string) bool {
	if auth == nil || selector == "" {
		return false
	}
	auth.EnsureIndex()
	candidates := []string{
		auth.Index,
		auth.ID,
		auth.FileName,
		filepath.Base(auth.FileName),
		auth.Label,
		authEmail(auth),
	}
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate), selector) {
			return true
		}
	}
	return false
}

func (h *Handler) selectXAIRefreshTarget(selector string) (*coreauth.Auth, int, string) {
	if h == nil || h.authManager == nil {
		return nil, http.StatusInternalServerError, "handler not initialized"
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, http.StatusBadRequest, "missing auth selector"
	}
	matches := make([]*coreauth.Auth, 0, 1)
	for _, auth := range h.authManager.List() {
		if !authMatchesXAIRefreshSelector(auth, selector) {
			continue
		}
		matches = append(matches, auth)
	}
	if len(matches) == 0 {
		return nil, http.StatusNotFound, "auth not found"
	}
	if len(matches) > 1 {
		return nil, http.StatusBadRequest, "auth selector is ambiguous"
	}
	auth := matches[0]
	if !isXAIAuth(auth) {
		return nil, http.StatusBadRequest, "auth is not xai"
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return nil, http.StatusBadRequest, "auth is disabled"
	}
	return auth, http.StatusOK, ""
}

// ListXAIRefreshAuthFiles returns all xAI auth files eligible for refresh.
func (h *Handler) ListXAIRefreshAuthFiles(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}
	auths := h.authManager.List()
	files := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if !isXAIAuth(auth) {
			continue
		}
		if entry := h.buildAuthFileEntry(auth); entry != nil {
			files = append(files, entry)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		nameI, _ := files[i]["name"].(string)
		nameJ, _ := files[j]["name"].(string)
		return strings.ToLower(nameI) < strings.ToLower(nameJ)
	})
	c.JSON(http.StatusOK, gin.H{"files": files})
}

// RefreshXAIFreeAccounts starts a background task that sends a minimal chat
// request to each xAI account in sequence, with random delays between accounts.
func (h *Handler) RefreshXAIFreeAccounts(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	var body xaiRefreshRequest
	if c.Request != nil && c.Request.Body != nil {
		if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil && !errors.Is(errBindJSON, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}

	selector := xaiRefreshSelectorValue(body)
	var targets []*coreauth.Auth
	if len(body.AuthIndices) > 0 {
		auths := h.authManager.List()
		indexSet := make(map[string]struct{}, len(body.AuthIndices))
		for _, idx := range body.AuthIndices {
			if trimmed := strings.TrimSpace(idx); trimmed != "" {
				indexSet[trimmed] = struct{}{}
			}
		}
		for _, a := range auths {
			if !isXAIAuth(a) || a.Disabled || a.Status == coreauth.StatusDisabled {
				continue
			}
			a.EnsureIndex()
			if _, ok := indexSet[a.Index]; ok {
				targets = append(targets, a)
			}
		}
		if len(targets) == 0 {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "no matching xai accounts found", "total": 0})
			return
		}
	} else if selector != "" {
		target, statusCode, errMsg := h.selectXAIRefreshTarget(selector)
		if errMsg != "" {
			c.JSON(statusCode, gin.H{"error": errMsg})
			return
		}
		targets = append(targets, target)
	} else {
		auths := h.authManager.List()
		for _, a := range auths {
			if isXAIAuth(a) && !a.Disabled && a.Status != coreauth.StatusDisabled {
				targets = append(targets, a)
			}
		}
		if len(targets) == 0 {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "no xai accounts found", "total": 0})
			return
		}
	}

	xaiRefreshTasksMu.Lock()
	xaiRefreshCounter++
	taskID := fmt.Sprintf("xai-refresh-%d-%d", time.Now().UnixMilli(), xaiRefreshCounter)
	task := &xaiRefreshTask{
		total:   len(targets),
		running: true,
		results: make([]xaiRefreshResult, 0, len(targets)),
	}
	xaiRefreshTasks[taskID] = task
	xaiRefreshTasksMu.Unlock()

	go h.runXAIRefresh(taskID, task, targets)

	c.JSON(http.StatusAccepted, gin.H{
		"status":  "started",
		"task_id": taskID,
		"total":   len(targets),
	})
}

// runXAIRefresh processes each target xAI account sequentially.
func (h *Handler) runXAIRefresh(taskID string, task *xaiRefreshTask, targets []*coreauth.Auth) {
	defer func() {
		task.mu.Lock()
		task.running = false
		task.mu.Unlock()
	}()

	for i, auth := range targets {
		result := xaiRefreshResult{
			Name:  auth.FileName,
			Email: authEmail(auth),
		}

		errPing := h.pingXAIAccount(auth)
		if errPing != nil {
			result.Success = false
			result.Error = errPing.Error()
			log.WithError(errPing).WithField("auth", auth.FileName).Warn("xai refresh ping failed")
			h.markRefreshPingFailure(auth, "grok-4.5", errPing)
		} else {
			result.Success = true
			now := time.Now().UTC()
			if auth.Metadata == nil {
				auth.Metadata = make(map[string]any)
			}
			auth.Metadata["last_refresh"] = now.Format(time.RFC3339)
			auth.LastRefreshedAt = now
			auth.UpdatedAt = now
			if _, errUpdate := h.authManager.Update(context.Background(), auth); errUpdate != nil {
				log.WithError(errUpdate).WithField("auth", auth.FileName).Warn("failed to persist last_refresh after xai ping")
			}
		}

		task.mu.Lock()
		task.results = append(task.results, result)
		task.done = i + 1
		task.mu.Unlock()

		// Random delay between accounts (3-8 seconds) to avoid risk control.
		if i < len(targets)-1 {
			jitter := time.Duration(3000+rand.IntN(5000)) * time.Millisecond
			time.Sleep(jitter)
		}
	}
}

func minimalXAIRefreshPayload() []byte {
	return []byte(`{"model":"grok-4.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true,"store":false,"instructions":""}`)
}

// markRefreshPingFailure records a management refresh ping failure through the
// same cooldown path as live traffic, so exhausted OAuth accounts leave the
// selection pool instead of only flipping Status to error.
func (h *Handler) markRefreshPingFailure(auth *coreauth.Auth, model string, errPing error) {
	if h == nil || h.authManager == nil || auth == nil || errPing == nil {
		return
	}
	provider := strings.TrimSpace(auth.Provider)
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		authID = strings.TrimSpace(auth.FileName)
	}
	result := coreauth.Result{
		AuthID:   authID,
		Provider: provider,
		Model:    strings.TrimSpace(model),
		Success:  false,
		Error: &coreauth.Error{
			Code:    "refresh_failed",
			Message: errPing.Error(),
		},
	}
	var statusErr interface{ StatusCode() int }
	if errors.As(errPing, &statusErr) && statusErr != nil {
		result.Error.HTTPStatus = statusErr.StatusCode()
	}
	var retryAfterErr interface{ RetryAfter() *time.Duration }
	if errors.As(errPing, &retryAfterErr) && retryAfterErr != nil {
		if ra := retryAfterErr.RetryAfter(); ra != nil {
			wait := *ra
			result.RetryAfter = &wait
		}
	}
	// When the executor did not expose a status code (wrapped errors that lost
	// the type), still treat free-usage / usage-limit style messages as 429 so
	// resolveQuotaCooldown can apply provider policy.
	if result.Error.HTTPStatus == 0 {
		lower := strings.ToLower(errPing.Error())
		if strings.Contains(lower, "free-usage-exhausted") ||
			strings.Contains(lower, "included free usage") ||
			strings.Contains(lower, "usage_limit_reached") ||
			strings.Contains(lower, "too many requests") {
			result.Error.HTTPStatus = http.StatusTooManyRequests
		}
	}
	h.authManager.MarkResult(context.Background(), result)
}

// pingXAIAccount sends a minimal chat request to keep the xAI OAuth token alive.
func (h *Handler) pingXAIAccount(auth *coreauth.Auth) error {
	executor := codexexecutor.NewXAIExecutor(h.cfg)

	minimalPayload := minimalXAIRefreshPayload()

	req := cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: minimalPayload,
		Format:  sdktranslator.FromString("codex"),
	}
	opts := cliproxyexecutor.Options{
		Stream:       true,
		SourceFormat: sdktranslator.FromString("codex"),
	}

	ctx := context.Background()
	_, errExec := executor.Execute(ctx, auth, req, opts)
	if errExec != nil {
		return fmt.Errorf("xai executor ping failed: %w", errExec)
	}
	return nil
}

// GetRefreshXAIFreeStatus returns the current progress of an xAI refresh task.
func (h *Handler) GetRefreshXAIFreeStatus(c *gin.Context) {
	taskID := c.Param("taskId")
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing taskId"})
		return
	}

	xaiRefreshTasksMu.Lock()
	task, ok := xaiRefreshTasks[taskID]
	xaiRefreshTasksMu.Unlock()

	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	status := "running"
	if !task.running {
		status = "completed"
	}

	successCount := 0
	failCount := 0
	for _, r := range task.results {
		if r.Success {
			successCount++
		} else {
			failCount++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  status,
		"total":   task.total,
		"done":    task.done,
		"success": successCount,
		"failed":  failCount,
		"results": task.results,
	})
}
