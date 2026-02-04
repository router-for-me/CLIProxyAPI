package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	log "github.com/sirupsen/logrus"
)

// AuthSyncData represents auth data for sync (without refresh_token).
type AuthSyncData struct {
	ID       string         `json:"id"`
	Provider string         `json:"provider"`
	Metadata map[string]any `json:"metadata"`
}

// tryFetchFromMasterOnUnauthorized attempts to fetch credentials from master on 401 errors.
// Returns true if retry should happen (fetch succeeded and auth not yet retried).
// The fetched map tracks which auth IDs have already been fetched to prevent infinite loops.
func (m *Manager) tryFetchFromMasterOnUnauthorized(ctx context.Context, statusCode int, authID, provider string, fetched map[string]struct{}) bool {
	if statusCode != 401 || m.GetCredentialMaster() == "" {
		return false
	}
	if _, alreadyFetched := fetched[authID]; alreadyFetched {
		log.Warnf("got %d again after fetching from master, not retrying", statusCode)
		return false
	}
	log.Infof("got %d, fetching credential from master and retrying...", statusCode)
	fetched[authID] = struct{}{}
	if err := m.fetchCredentialFromMaster(ctx, authID, provider); err != nil {
		log.Warnf("failed to fetch credential from master: %v", err)
		return false
	}
	return true
}

// GetCredentialMaster returns the configured master node URL from runtime config.
func (m *Manager) GetCredentialMaster() string {
	if m == nil {
		return ""
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.CredentialMaster)
}

// getPeerSecret returns the peer secret from runtime config.
func (m *Manager) getPeerSecret() string {
	if m == nil {
		return ""
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return ""
	}
	return cfg.RemoteManagement.SecretKey
}

// GetAccessToken returns the access_token for a given auth ID.
// Used by master node to serve credential queries from followers.
func (m *Manager) GetAccessToken(id string) string {
	if m == nil || id == "" {
		return ""
	}
	m.mu.RLock()
	auth, ok := m.auths[id]
	m.mu.RUnlock()
	if !ok || auth == nil || auth.Metadata == nil {
		return ""
	}
	if at, ok := auth.Metadata["access_token"].(string); ok {
		return at
	}
	return ""
}

// RefreshIfNeeded checks if the token for the given auth ID needs refresh,
// and refreshes it if necessary. This is called by master node when serving
// credential queries from followers, ensuring tokens are refreshed on-demand
// even when master itself is not making API requests.
func (m *Manager) RefreshIfNeeded(ctx context.Context, id string) {
	if m == nil || id == "" {
		return
	}
	m.mu.RLock()
	auth := m.auths[id]
	m.mu.RUnlock()
	if auth == nil {
		return
	}
	now := time.Now()
	if m.shouldRefresh(auth, now) {
		log.Debugf("RefreshIfNeeded: token needs refresh for %s, triggering refresh", id)
		m.refreshAuth(ctx, id)
	}
}

// GetExpirationTime returns the expiration time for a given auth ID.
// Used by master node to include expiration info in credential responses.
func (m *Manager) GetExpirationTime(id string) (time.Time, bool) {
	if m == nil || id == "" {
		return time.Time{}, false
	}
	m.mu.RLock()
	auth := m.auths[id]
	m.mu.RUnlock()
	if auth == nil {
		return time.Time{}, false
	}
	return auth.ExpirationTime()
}

// GetAllAuthsForSync returns all auth entries for sync (without refresh_token).
func (m *Manager) GetAllAuthsForSync() []AuthSyncData {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]AuthSyncData, 0, len(m.auths))
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled {
			continue
		}
		syncData := AuthSyncData{
			ID:       auth.ID,
			Provider: auth.Provider,
			Metadata: sanitizeMetadataForSync(auth.Metadata),
		}
		result = append(result, syncData)
	}
	return result
}

// sanitizeMetadataForSync removes sensitive fields like refresh_token.
func sanitizeMetadataForSync(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	result := make(map[string]any, len(meta))
	for k, v := range meta {
		if k == "refresh_token" || k == "refreshToken" {
			continue
		}
		result[k] = v
	}
	return result
}

// fetchCredentialFromMaster fetches the latest access_token from master node.
func (m *Manager) fetchCredentialFromMaster(ctx context.Context, id, provider string) error {
	master := m.GetCredentialMaster()
	if master == "" {
		return errors.New("credential-master not configured")
	}
	secret := m.getPeerSecret()
	if secret == "" {
		return errors.New("peer secret not configured")
	}

	url := strings.TrimRight(master, "/") + "/v0/internal/credential?id=" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return errors.New("master returned " + resp.Status + ": " + string(body))
	}

	var result struct {
		ID          string `json:"id"`
		AccessToken string `json:"access_token"`
		Expired     string `json:"expired"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if result.AccessToken == "" {
		return errors.New("master returned empty access_token")
	}

	m.mu.Lock()
	auth, ok := m.auths[id]
	if !ok || auth == nil {
		m.mu.Unlock()
		return errors.New("auth not found locally")
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = result.AccessToken
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	if result.Expired != "" {
		auth.Metadata["expired"] = result.Expired
	}
	auth.UpdatedAt = time.Now()
	auth.LastRefreshedAt = time.Now()
	auth.LastError = nil
	auth.Status = StatusActive
	auth.Unavailable = false
	auth.NextRetryAfter = time.Time{}
	auth.ModelStates = nil
	m.mu.Unlock()

	_ = m.persist(ctx, auth)
	registry.GetGlobalRegistry().ResumeClientAllModels(id)

	log.Infof("fetched access_token from master: provider=%s, id=%s", provider, id)
	m.hook.OnAuthUpdated(ctx, auth.Clone())
	return nil
}

// SyncAuthsFromMaster syncs all auth entries from master node at startup.
// It writes auth files to the local auth directory for file watcher to pick up.
func (m *Manager) SyncAuthsFromMaster(ctx context.Context, authDir string) error {
	master := m.GetCredentialMaster()
	if master == "" {
		return nil
	}
	secret := m.getPeerSecret()
	if secret == "" {
		log.Warnf("SyncAuthsFromMaster: peer secret not configured")
		return errors.New("peer secret not configured")
	}
	log.Infof("SyncAuthsFromMaster: syncing from %s with authDir=%s", master, authDir)

	url := strings.TrimRight(master, "/") + "/v0/internal/auth-list"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return errors.New("master returned " + resp.Status + ": " + string(body))
	}

	var result struct {
		Auths []AuthSyncData `json:"auths"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	for _, syncData := range result.Auths {
		auth := syncDataToAuth(syncData, authDir)
		m.mu.Lock()
		m.auths[auth.ID] = auth
		m.mu.Unlock()

		if err := writeAuthToFile(authDir, syncData); err != nil {
			log.Warnf("failed to write auth file %s: %v", syncData.ID, err)
		}

		registry.GetGlobalRegistry().ResumeClientAllModels(auth.ID)
		m.hook.OnAuthUpdated(ctx, auth.Clone())
	}

	log.Infof("synced %d auths from master", len(result.Auths))
	return nil
}

// syncDataToAuth converts AuthSyncData to Auth for memory storage.
func syncDataToAuth(data AuthSyncData, authDir string) *Auth {
	now := time.Now()
	filename := data.ID
	if !strings.HasSuffix(filename, ".json") {
		filename += ".json"
	}
	return &Auth{
		ID:        data.ID,
		Provider:  data.Provider,
		FileName:  filename,
		Metadata:  data.Metadata,
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Attributes: map[string]string{
			"path": filepath.Join(authDir, filename),
		},
	}
}

// writeAuthToFile writes an auth entry to local file for persistence.
func writeAuthToFile(authDir string, syncData AuthSyncData) error {
	if authDir == "" || syncData.ID == "" {
		return nil
	}

	filename := syncData.ID
	if !strings.HasSuffix(filename, ".json") {
		filename += ".json"
	}
	filePath := filepath.Join(authDir, filename)

	data, err := json.MarshalIndent(syncData.Metadata, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0600)
}
