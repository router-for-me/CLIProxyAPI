package management

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	maxCodexCredentialBytes = 1 << 20
	codexReauthStageTTL     = 10 * time.Minute
)

var (
	errCodexReauthBadRequest   = errors.New("invalid reauth request")
	errCodexReauthNotFound     = errors.New("reauth target not found")
	errCodexReauthConflict     = errors.New("reauth target changed")
	codexReauthVerificationURL = "https://chatgpt.com/backend-api/codex/models"
)

type codexReauthTarget struct {
	AuthID     string
	AuthIndex  string
	FileName   string
	Path       string
	Generation string
	Subject    string
}

type codexReauthStage struct {
	Target    codexReauthTarget
	Path      string
	ExpiresAt time.Time
}

type codexReauthStageStore struct {
	mu     sync.Mutex
	dir    string
	stages map[string]codexReauthStage
}

func newCodexReauthStageStore(cfg *config.Config) *codexReauthStageStore {
	authDir := ""
	if cfg != nil {
		authDir = strings.TrimSpace(cfg.AuthDir)
	}
	dir := ""
	if authDir != "" {
		dir = filepath.Join(filepath.Dir(authDir), "."+filepath.Base(authDir)+"-codex-reauth")
	}
	store := &codexReauthStageStore{dir: dir, stages: make(map[string]codexReauthStage)}
	store.clearStaleFiles()
	return store
}

func (s *codexReauthStageStore) clearStaleFiles() {
	if s == nil || s.dir == "" {
		return
	}
	info, err := os.Lstat(s.dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			_ = os.Remove(filepath.Join(s.dir, entry.Name()))
		}
	}
}

func (s *codexReauthStageStore) put(stage codexReauthStage, raw []byte) (string, error) {
	if s == nil || s.dir == "" {
		return "", fmt.Errorf("reauth staging unavailable")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", fmt.Errorf("create reauth staging directory: %w", err)
	}
	info, err := os.Lstat(s.dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("unsafe reauth staging directory")
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return "", fmt.Errorf("secure reauth staging directory: %w", err)
	}
	handle, err := randomHex(32)
	if err != nil {
		return "", err
	}
	stage.Path = filepath.Join(s.dir, handle)
	stage.ExpiresAt = time.Now().Add(codexReauthStageTTL)
	file, err := os.OpenFile(stage.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create staged replacement")
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(stage.Path)
		return "", fmt.Errorf("write staged replacement")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(time.Now())
	s.stages[handle] = stage
	time.AfterFunc(codexReauthStageTTL, func() { s.expire(handle) })
	return handle, nil
}

func (s *codexReauthStageStore) expire(handle string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stage, ok := s.stages[handle]
	if ok && time.Now().After(stage.ExpiresAt) {
		_ = os.Remove(stage.Path)
		delete(s.stages, handle)
	}
}

func (s *codexReauthStageStore) take(handle string) (codexReauthStage, bool) {
	handle = strings.TrimSpace(handle)
	decoded, err := hex.DecodeString(handle)
	if s == nil || err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != handle {
		return codexReauthStage{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(time.Now())
	stage, ok := s.stages[handle]
	if ok {
		delete(s.stages, handle)
	}
	return stage, ok
}

func (s *codexReauthStageStore) discard(handle string) {
	stage, ok := s.take(handle)
	if ok {
		_ = os.Remove(stage.Path)
	}
}

func (s *codexReauthStageStore) removeTarget(fileName string) {
	if s == nil {
		return
	}
	fileName = filepath.Base(strings.TrimSpace(fileName))
	s.mu.Lock()
	defer s.mu.Unlock()
	for handle, stage := range s.stages {
		if stage.Target.FileName == fileName {
			_ = os.Remove(stage.Path)
			delete(s.stages, handle)
		}
	}
}

func (s *codexReauthStageStore) purgeLocked(now time.Time) {
	for handle, stage := range s.stages {
		if now.After(stage.ExpiresAt) {
			_ = os.Remove(stage.Path)
			delete(s.stages, handle)
		}
	}
}

func randomHex(bytesCount int) (string, error) {
	raw := make([]byte, bytesCount)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate reauth handle: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func credentialGeneration(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func credentialIdentityGeneration(raw []byte) (string, error) {
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil {
		return "", errCodexReauthConflict
	}
	delete(document, "disabled")
	canonical, err := json.Marshal(document)
	if err != nil {
		return "", errCodexReauthConflict
	}
	return credentialGeneration(canonical), nil
}

func readBoundedCredential(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxCodexCredentialBytes {
		return nil, fmt.Errorf("unsafe credential file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("unsafe credential file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxCodexCredentialBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxCodexCredentialBytes {
		return nil, fmt.Errorf("read bounded credential")
	}
	return raw, nil
}

func (h *Handler) parseCodexReauthTarget(c *gin.Context) (codexReauthTarget, error) {
	if h == nil || h.authManager == nil || h.cfg == nil || strings.TrimSpace(h.cfg.AuthDir) == "" {
		return codexReauthTarget{}, errCodexReauthNotFound
	}
	var req struct {
		AuthIndex  string `json:"auth_index"`
		Generation string `json:"generation"`
	}
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return codexReauthTarget{}, errCodexReauthBadRequest
	}
	req.AuthIndex = strings.TrimSpace(req.AuthIndex)
	req.Generation = strings.TrimSpace(req.Generation)
	if req.AuthIndex == "" || len(req.Generation) != sha256.Size*2 {
		return codexReauthTarget{}, errCodexReauthBadRequest
	}
	if _, ok := h.tokenStoreWithBaseDir().(*sdkAuth.FileTokenStore); !ok {
		return codexReauthTarget{}, errCodexReauthConflict
	}
	var target *coreauth.Auth
	for _, auth := range h.authManager.List() {
		if auth != nil && lockedAuthIndex(auth) == req.AuthIndex {
			if target != nil {
				return codexReauthTarget{}, errCodexReauthConflict
			}
			target = auth
		}
	}
	if target == nil {
		return codexReauthTarget{}, errCodexReauthNotFound
	}
	if !strings.EqualFold(target.Provider, "codex") || (!target.Disabled && target.Status != coreauth.StatusDisabled) || isRuntimeOnlyAuth(target) {
		return codexReauthTarget{}, errCodexReauthConflict
	}
	path := strings.TrimSpace(authAttribute(target, coreauth.AttributePath))
	authDir, err := filepath.Abs(strings.TrimSpace(h.cfg.AuthDir))
	targetPath, errPath := filepath.Abs(path)
	physicalAuthDir, errAuthPhysical := filepath.EvalSymlinks(authDir)
	physicalTarget, errTargetPhysical := filepath.EvalSymlinks(targetPath)
	rel, errRel := filepath.Rel(physicalAuthDir, physicalTarget)
	if err != nil || errPath != nil || errAuthPhysical != nil || errTargetPhysical != nil || errRel != nil ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return codexReauthTarget{}, errCodexReauthConflict
	}
	path = physicalTarget
	raw, err := readBoundedCredential(path)
	if err != nil || credentialGeneration(raw) != req.Generation {
		return codexReauthTarget{}, errCodexReauthConflict
	}
	var metadata map[string]any
	if json.Unmarshal(raw, &metadata) != nil {
		return codexReauthTarget{}, errCodexReauthConflict
	}
	subject, _ := metadata["account_id"].(string)
	subject = strings.TrimSpace(subject)
	name := strings.TrimSpace(target.FileName)
	if name == "" {
		name = filepath.Base(path)
	}
	if subject == "" || filepath.Base(path) != name {
		return codexReauthTarget{}, errCodexReauthConflict
	}
	return codexReauthTarget{AuthID: target.ID, AuthIndex: req.AuthIndex, FileName: name, Path: path, Generation: req.Generation, Subject: subject}, nil
}

func writeCodexReauthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errCodexReauthNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "reauth target not found"})
	case errors.Is(err, errCodexReauthConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "reauth target changed"})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid reauth request"})
	}
}

func (h *Handler) stageCodexReauthCandidate(target codexReauthTarget, bundle *codex.CodexAuthBundle) (string, error) {
	if bundle == nil {
		return "", fmt.Errorf("missing replacement credentials")
	}
	storage := newCodexOAuthService(h.cfg).CreateTokenStorage(bundle)
	if storage == nil || strings.TrimSpace(storage.AccountID) != target.Subject {
		return "", fmt.Errorf("replacement subject mismatch")
	}
	digest := sha256.Sum256([]byte(storage.AccountID))
	plan := ""
	if claims, _ := codex.ParseJWTToken(storage.IDToken); claims != nil {
		plan = strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
	}
	name := codex.CredentialFileName(storage.Email, plan, hex.EncodeToString(digest[:])[:8], true)
	legacyName := codex.CredentialFileName(storage.Email, plan, "", true)
	if (name != target.FileName && legacyName != target.FileName) || filepath.Base(target.Path) != target.FileName {
		return "", fmt.Errorf("replacement filename mismatch")
	}
	for _, auth := range h.authManager.List() {
		if auth == nil || auth.ID == target.AuthID {
			continue
		}
		if subject, _ := auth.Metadata["account_id"].(string); strings.TrimSpace(subject) == target.Subject {
			return "", fmt.Errorf("duplicate replacement subject")
		}
	}
	current, err := readBoundedCredential(target.Path)
	if err != nil || credentialGeneration(current) != target.Generation {
		return "", fmt.Errorf("reauth target changed")
	}
	var retained map[string]any
	if json.Unmarshal(current, &retained) != nil {
		return "", fmt.Errorf("reauth target changed")
	}
	for _, key := range []string{"id_token", "access_token", "refresh_token", "account_id", "email", "expired", "last_refresh"} {
		delete(retained, key)
	}
	candidate, err := misc.MergeMetadata(storage, nil)
	if err != nil {
		return "", fmt.Errorf("serialize replacement credentials")
	}
	for key, value := range candidate {
		retained[key] = value
	}
	retained["disabled"] = true
	retained["type"] = "codex"
	data := retained
	raw, err := json.Marshal(data)
	if err != nil || len(raw) > maxCodexCredentialBytes {
		return "", fmt.Errorf("serialize replacement credentials")
	}
	handle, err := h.codexReauthStages.put(codexReauthStage{Target: target}, raw)
	if err != nil {
		return "", err
	}
	return handle, nil
}

func (h *Handler) verifyCodexCandidate(ctx context.Context, auth *coreauth.Auth) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexReauthVerificationURL, nil)
	if err != nil {
		return err
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("provider verification failed")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID, _ := auth.Metadata["account_id"].(string); accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Transport: h.apiCallTransport(auth), Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("provider verification failed")
	}
	return nil
}

func (h *Handler) AdoptCodexReauth(c *gin.Context) {
	var req struct {
		StageHandle string `json:"stage_handle"`
	}
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 4097))
	decoder.DisallowUnknownFields()
	if h == nil || h.authManager == nil || h.codexReauthStages == nil || decoder.Decode(&req) != nil || decoder.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(req.StageHandle) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid reauth request"})
		return
	}
	stage, ok := h.codexReauthStages.take(req.StageHandle)
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "reauth stage unavailable"})
		return
	}
	defer os.Remove(stage.Path)
	raw, err := readBoundedCredential(stage.Path)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "reauth stage unavailable"})
		return
	}
	auths, err := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{Config: h.cfg, AuthDir: filepath.Dir(stage.Target.Path), Now: time.Now()}, stage.Target.Path, raw)
	if err != nil || len(auths) != 1 {
		c.JSON(http.StatusConflict, gin.H{"error": "replacement credential invalid"})
		return
	}
	candidate := auths[0]
	candidate.ID = stage.Target.AuthID
	candidate.FileName = stage.Target.FileName
	candidate.Index = stage.Target.AuthIndex
	if !candidate.Disabled || !strings.EqualFold(candidate.Provider, "codex") || strings.TrimSpace(fmt.Sprint(candidate.Metadata["account_id"])) != stage.Target.Subject {
		c.JSON(http.StatusConflict, gin.H{"error": "replacement credential invalid"})
		return
	}
	verifyCtx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	err = h.verifyCodexReauth(verifyCtx, candidate)
	cancel()
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "replacement credential verification failed"})
		return
	}

	h.authMutationMu.Lock()
	defer h.authMutationMu.Unlock()
	target, ok := h.authManager.GetByID(stage.Target.AuthID)
	current, err := readBoundedCredential(stage.Target.Path)
	if !ok || err != nil || lockedAuthIndex(target) != stage.Target.AuthIndex || credentialGeneration(current) != stage.Target.Generation {
		c.JSON(http.StatusConflict, gin.H{"error": "reauth target changed"})
		return
	}
	for _, auth := range h.authManager.List() {
		if auth != nil && auth.ID != target.ID && strings.TrimSpace(fmt.Sprint(auth.Metadata["account_id"])) == stage.Target.Subject {
			c.JSON(http.StatusConflict, gin.H{"error": "duplicate replacement subject"})
			return
		}
	}
	if err := atomicCredentialReplace(stage.Target.Path, stage.Target.Generation, raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to adopt replacement credential"})
		return
	}
	if err = h.updateCodexReauth(c.Request.Context(), candidate); err != nil {
		_ = atomicCredentialReplace(stage.Target.Path, credentialGeneration(raw), current)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to adopt replacement credential"})
		return
	}
	identityGeneration, err := credentialIdentityGeneration(raw)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to adopt replacement credential"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "auth_index": stage.Target.AuthIndex, "disabled": true, "generation": identityGeneration})
}

func (h *Handler) VerifyCodexReauth(c *gin.Context) {
	var req struct {
		AuthIndex  string `json:"auth_index"`
		Generation string `json:"generation"`
	}
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 4097))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid reauth request"})
		return
	}
	req.AuthIndex = strings.TrimSpace(req.AuthIndex)
	req.Generation = strings.TrimSpace(req.Generation)
	if h == nil || h.authManager == nil || len(req.Generation) != sha256.Size*2 {
		c.JSON(http.StatusConflict, gin.H{"error": "reauth target changed"})
		return
	}
	var target *coreauth.Auth
	for _, auth := range h.authManager.List() {
		if auth != nil && lockedAuthIndex(auth) == req.AuthIndex {
			if target != nil {
				c.JSON(http.StatusConflict, gin.H{"error": "reauth target changed"})
				return
			}
			target = auth
		}
	}
	if target == nil || !strings.EqualFold(target.Provider, "codex") || target.Disabled || target.Status == coreauth.StatusDisabled || isRuntimeOnlyAuth(target) {
		c.JSON(http.StatusConflict, gin.H{"error": "reauth target changed"})
		return
	}
	path := strings.TrimSpace(authAttribute(target, coreauth.AttributePath))
	raw, err := readBoundedCredential(path)
	identityGeneration, errIdentity := credentialIdentityGeneration(raw)
	if err != nil || errIdentity != nil || identityGeneration != req.Generation {
		c.JSON(http.StatusConflict, gin.H{"error": "reauth target changed"})
		return
	}
	verifyCtx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	err = h.verifyCodexReauth(verifyCtx, target)
	cancel()
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "replacement credential verification failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "auth_index": req.AuthIndex, "disabled": false})
}

func atomicCredentialReplace(path, expectedGeneration string, raw []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".codex-reauth-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = io.Copy(tmp, bytes.NewReader(raw))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	current, err := readBoundedCredential(path)
	if err != nil || credentialGeneration(current) != expectedGeneration {
		return errCodexReauthConflict
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return err
	}
	if directory, openErr := os.Open(dir); openErr == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	return err
}
