package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// BindCodexSIWC explicitly replaces the SIWC partition of an existing file-backed
// Codex registration. Unlike ordinary persistence, a failed write never publishes
// credentials. expectedEpoch and expectedAccount must match the registration captured before OAuth.
func (m *Manager) BindCodexSIWC(ctx context.Context, id string, expectedEpoch uint64, expectedAccount string, tokenData *codex.CodexSIWCTokenData) (*Auth, error) {
	if m == nil || strings.TrimSpace(id) == "" || expectedEpoch == 0 || tokenData == nil || strings.TrimSpace(tokenData.AccessToken) == "" || strings.TrimSpace(tokenData.RefreshToken) == "" || strings.TrimSpace(tokenData.ClientID) == "" {
		return nil, fmt.Errorf("bind SIWC: invalid target or token data")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := m.lockAuthMutationContext(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	m.mu.RLock()
	base := m.auths[id].Clone()
	m.mu.RUnlock()
	if base == nil || base.RegistrationEpoch != expectedEpoch || !strings.EqualFold(strings.TrimSpace(base.Provider), "codex") || !codexBindingHasOuter(base.Metadata) {
		return nil, fmt.Errorf("bind SIWC: target is missing, changed, or not a Codex OAuth credential")
	}
	path, err := m.codexBindingPath(base)
	if err != nil {
		return nil, err
	}
	var committed *Auth
	cooldownChanged := false
	err = codex.WithFileLock(path, func() error {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		checkedPath, errPath := m.codexBindingPath(base)
		if errPath != nil || checkedPath != path {
			return fmt.Errorf("bind SIWC: backing file changed")
		}
		info, errStat := os.Lstat(path)
		if errStat != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("bind SIWC: backing file is not a regular file")
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			return fmt.Errorf("bind SIWC: read backing file: %w", errRead)
		}
		var disk map[string]any
		if json.Unmarshal(data, &disk) != nil || !codexBindingHasOuter(disk) {
			return fmt.Errorf("bind SIWC: invalid Codex OAuth file")
		}
		kind, _ := disk["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(kind), "codex") {
			return fmt.Errorf("bind SIWC: backing file type changed")
		}
		if account, _ := base.Metadata["account_id"].(string); account != expectedAccount || (account != "" && disk["account_id"] != account) || (account == "" && disk["account_id"] != nil && disk["account_id"] != "") {
			return fmt.Errorf("bind SIWC: backing account changed")
		}
		// Keep arbitrary disk values (including large JSON numbers) losslessly. Only
		// the two explicitly bound fields are replaced; no metadata normalization runs.
		var raw map[string]json.RawMessage
		if errJSON := json.Unmarshal(data, &raw); errJSON != nil {
			return errJSON
		}
		replacement := tokenData.ToMap()
		siwc, errJSON := json.Marshal(replacement)
		if errJSON != nil {
			return errJSON
		}
		out := make(map[string]any, len(raw)+2)
		for key, value := range raw {
			out[key] = value
		}
		out["siwc"] = json.RawMessage(siwc)
		out["use_siwc"] = true
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		latestInfo, errStat := os.Lstat(path)
		if errStat != nil || !os.SameFile(info, latestInfo) {
			return fmt.Errorf("bind SIWC: backing file changed during binding")
		}
		latestData, errReadLatest := os.ReadFile(path)
		if errReadLatest != nil || !bytes.Equal(data, latestData) {
			return fmt.Errorf("bind SIWC: backing file changed during binding")
		}
		if errWrite := codex.WriteJSONFileAtomic(path, out); errWrite != nil {
			return fmt.Errorf("bind SIWC: persist binding: %w", errWrite)
		}
		disk["siwc"] = replacement
		disk["use_siwc"] = true
		// The mutation gate fences Delete, reload, and refresh publication. Clone
		// only now so counters and request-time runtime changes during I/O survive.
		m.mu.Lock()
		current := m.auths[id]
		if current == nil || current.RegistrationEpoch != expectedEpoch {
			m.mu.Unlock()
			return fmt.Errorf("bind SIWC: registration changed before publication")
		}
		updated := current.Clone()
		updated.Metadata = disk
		updated.Storage = nil
		updated.CodexRefreshPatch = nil
		// The file owns this toggle; stale attribute overrides must not mask it
		// or prevent a later file edit from disabling SIWC.
		delete(updated.Attributes, "use_siwc")
		if disabled, ok := disk["disabled"].(bool); ok {
			updated.Disabled = disabled
		}
		if updated.Disabled {
			updated.Status = StatusDisabled
		} else if updated.Status == StatusDisabled {
			updated.Status = StatusActive
		}
		now := time.Now()
		if !updated.Disabled {
			updated.RejectedAccessToken = ""
			if hasUnauthorizedAuthFailure(current) || (updated.LastError != nil && isUnauthorizedError(updated.LastError)) {
				updated.Unavailable = false
				updated.LastError = nil
				updated.StatusMessage = ""
				updated.Status = StatusActive
				updated.NextRetryAfter = time.Time{}
				cooldownChanged = true
			}
			clearUnauthorizedModelStates(updated, now)
			// The helper can clear errors while preserving a model quota block.
			if len(updated.ModelStates) > 0 {
				cooldownChanged = true
			}
			if current.Quota.Exceeded && (current.Quota.NextRecoverAt.IsZero() || current.Quota.NextRecoverAt.After(now)) {
				updated.Quota = current.Quota.Clone()
				updated.Unavailable = current.Unavailable
				updated.NextRetryAfter = current.NextRetryAfter
				updated.Status = current.Status
			}
		}
		updated.Generation++
		updated.CredentialVersion = max(current.CredentialVersion, 1) + 1
		updated.UpdatedAt = now
		updated.ClearFileSnapshot()
		m.auths[id] = updated
		m.notifyAuthChangeLocked(id)
		committed = updated.Clone()
		m.mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, err
	}
	release()
	m.finishAuthUpdate(ctx, committed, cooldownChanged)
	return committed, nil
}

func codexBindingHasOuter(metadata map[string]any) bool {
	access, _ := metadata["access_token"].(string)
	refresh, _ := metadata["refresh_token"].(string)
	return strings.TrimSpace(access) != "" && strings.TrimSpace(refresh) != ""
}

// Resolve confinement again at commit, not just when the management flow starts.
func (m *Manager) codexBindingPath(auth *Auth) (string, error) {
	source := strings.ToLower(authAttribute(auth, AttributeSource))
	if auth == nil || auth.AuthSourceKind() != AuthSourceFile || source == AuthSourceConfig || strings.HasPrefix(source, AuthSourceConfig+":") || auth.AuthKind() != AuthKindOAuth {
		return "", fmt.Errorf("bind SIWC: target is not a file-backed OAuth credential")
	}
	cfg, _ := m.runtimeConfig.Load().(*config.Config)
	if cfg == nil || strings.TrimSpace(cfg.AuthDir) == "" {
		return "", fmt.Errorf("bind SIWC: auth directory is not configured")
	}
	authDir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err != nil {
		return "", fmt.Errorf("bind SIWC: resolve auth directory: %w", err)
	}
	authDir, err = filepath.Abs(authDir)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(authDir)
	if err != nil {
		return "", fmt.Errorf("bind SIWC: invalid auth directory")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(auth.Attributes["path"])
	if path == "" {
		path = strings.TrimSpace(auth.Attributes["source"])
	}
	if path != "" && !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return "", err
		}
	}
	if path == "" && strings.TrimSpace(auth.FileName) != "" {
		fileName := strings.TrimSpace(auth.FileName)
		if filepath.IsAbs(fileName) {
			path = fileName
		} else {
			path = filepath.Join(root, fileName)
		}
	}
	if path == "" {
		return "", fmt.Errorf("bind SIWC: target is not file-backed")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("bind SIWC: backing file is missing or inaccessible")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bind SIWC: backing file is outside auth directory")
	}
	// Reject final-component symlinks rather than replacing their targets.
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("bind SIWC: backing file is not regular")
	}
	return resolved, nil
}
