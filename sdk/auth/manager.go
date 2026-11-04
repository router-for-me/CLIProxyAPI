package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Manager aggregates authenticators and coordinates persistence via a token store.
type Manager struct {
	authenticators map[string]Authenticator
	store          coreauth.Store
}

// NewManager constructs a manager with the provided token store and authenticators.
// If store is nil, the caller must set it later using SetStore.
func NewManager(store coreauth.Store, authenticators ...Authenticator) *Manager {
	mgr := &Manager{
		authenticators: make(map[string]Authenticator),
		store:          store,
	}
	for i := range authenticators {
		mgr.Register(authenticators[i])
	}
	return mgr
}

// Register adds or replaces an authenticator keyed by its provider identifier.
func (m *Manager) Register(a Authenticator) {
	if a == nil {
		return
	}
	if m.authenticators == nil {
		m.authenticators = make(map[string]Authenticator)
	}
	m.authenticators[a.Provider()] = a
}

// SetStore updates the token store used for persistence.
func (m *Manager) SetStore(store coreauth.Store) {
	m.store = store
}

// Login executes the provider login flow and persists the resulting auth record.
func (m *Manager) Login(ctx context.Context, provider string, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, string, error) {
	auth, ok := m.authenticators[provider]
	if !ok {
		return nil, "", fmt.Errorf("cliproxy auth: authenticator %s not registered", provider)
	}

	record, err := auth.Login(ctx, cfg, opts)
	if err != nil {
		return nil, "", err
	}
	if record == nil {
		return nil, "", fmt.Errorf("cliproxy auth: authenticator %s returned nil record", provider)
	}

	if m.store == nil {
		return record, "", nil
	}

	if cfg != nil {
		if dirSetter, ok := m.store.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(cfg.AuthDir)
		}
		if strings.TrimSpace(cfg.AuthDir) != "" {
			targetFile := record.FileName
			if targetFile == "" {
				targetFile = record.ID
			}
			if targetFile != "" {
				fullPath := filepath.Join(cfg.AuthDir, targetFile)
				if raw, errRead := os.ReadFile(fullPath); errRead == nil && len(raw) > 0 {
					var existingMap map[string]any
					if errUnmarshal := json.Unmarshal(raw, &existingMap); errUnmarshal == nil && len(existingMap) > 0 {
						coreauth.MergeExistingAuthMetadata(record, existingMap)
					}
				}
			}
		}
	}
	legacyClaudeCredential, errLegacy := claudeauth.FindMatchingLegacyCredential(ctx, m.store, record)
	if errLegacy != nil {
		return record, "", errLegacy
	}
	if legacyClaudeCredential != nil {
		coreauth.MergeExistingAuthMetadata(record, legacyClaudeCredential.Metadata)
	}
	legacyXAICredential, errLegacyXAI := xaiauth.FindMatchingLegacyCredential(ctx, m.store, record)
	if errLegacyXAI != nil {
		return record, "", errLegacyXAI
	}
	adoptXAICredential := legacyXAICredential != nil && record != nil &&
		strings.EqualFold(strings.TrimSpace(record.Provider), "xai") &&
		metadataStringValue(record.Metadata, "email") == "" &&
		metadataStringValue(record.Metadata, "sub") != "" &&
		metadataStringValue(legacyXAICredential.Metadata, "email") != ""
	if adoptXAICredential {
		// The upstream stopped returning the email; keep the account on its
		// richer email-named credential (richest-known name wins) so the next
		// login that returns the email finds the same file again. Renaming is
		// limited to stores that resolve the save path from the record name;
		// every built-in backend (file, git, Postgres, object) exposes the
		// SetBaseDir capability, so unknown stores keep the ordinary
		// migration instead of risking a rename they cannot honor.
		if _, isFileNameStore := m.store.(interface{ SetBaseDir(string) }); !isFileNameStore {
			adoptXAICredential = false
		} else if disabled, hasDisabled := record.Metadata["disabled"].(bool); hasDisabled && disabled {
			// The destination file deliberately disables this credential; renaming
			// would leave it disabled while the richer file gets deleted, losing
			// the operator's opt-out and the saved credential on re-login.
			adoptXAICredential = false
		}
	}
	if legacyXAICredential != nil {
		coreauth.MergeExistingAuthMetadata(record, legacyXAICredential.Metadata)
	}
	if adoptXAICredential {
		adoptLegacyXAIFileName(record, legacyXAICredential)
	}

	savedPath, err := m.store.Save(coreauth.WithAuthCreationIntent(ctx), record)
	if err != nil {
		return record, "", err
	}
	if legacyClaudeCredential != nil {
		if strings.TrimSpace(savedPath) == "" {
			return record, "", fmt.Errorf("cliproxy auth: canonical Claude credential was not persisted; legacy credential retained")
		}
		legacyID := strings.TrimSpace(legacyClaudeCredential.ID)
		if legacyID == "" {
			legacyID = strings.TrimSpace(legacyClaudeCredential.FileName)
		}
		if errDelete := m.store.Delete(ctx, legacyID); errDelete != nil {
			return record, savedPath, fmt.Errorf("cliproxy auth: canonical Claude credential saved but legacy credential cleanup failed: %w", errDelete)
		}
	}
	if legacyXAICredential != nil && !adoptXAICredential {
		if strings.TrimSpace(savedPath) == "" {
			return record, "", fmt.Errorf("cliproxy auth: canonical xAI credential was not persisted; legacy credential retained")
		}
		legacyID := strings.TrimSpace(legacyXAICredential.ID)
		if legacyID == "" {
			legacyID = strings.TrimSpace(legacyXAICredential.FileName)
		}
		if errDelete := m.store.Delete(ctx, legacyID); errDelete != nil {
			return record, savedPath, fmt.Errorf("cliproxy auth: canonical xAI credential saved but legacy credential cleanup failed: %w", errDelete)
		}
	}
	return record, savedPath, nil
}

func metadataStringValue(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

// adoptLegacyXAIFileName points a subject-only xAI record at the existing
// email-named credential so the save refreshes that file in place. The token
// storage email is backfilled because SaveTokenToFile merges the record
// metadata over the serialized storage and would otherwise drop the email.
// Path attributes pointing at the superseded subject-named file are cleared
// so the store resolves the adopted name.
func adoptLegacyXAIFileName(record, legacy *coreauth.Auth) {
	legacyFileName := strings.TrimSpace(legacy.FileName)
	if legacyFileName == "" {
		legacyFileName = strings.TrimSpace(legacy.ID)
	}
	if legacyFileName == "" {
		return
	}
	oldBaseName := filepath.Base(strings.TrimSpace(record.FileName))
	if oldBaseName == "" || oldBaseName == "." {
		oldBaseName = filepath.Base(strings.TrimSpace(record.ID))
	}
	record.ID = legacyFileName
	record.FileName = legacyFileName
	if storage, ok := record.Storage.(*xaiauth.TokenStorage); ok && storage != nil {
		if email := metadataStringValue(legacy.Metadata, "email"); email != "" {
			storage.Email = email
		}
	}
	for key, value := range record.Attributes {
		if strings.TrimSpace(value) != "" && strings.EqualFold(filepath.Base(strings.TrimSpace(value)), oldBaseName) {
			delete(record.Attributes, key)
		}
	}
}
