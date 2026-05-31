package grok

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// GrokTokenStorage persists xAI OAuth credentials for a single account.
// JSON serialization matches the on-disk shape the SDK reads back when the
// service boots.
type GrokTokenStorage struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
	Email        string `json:"email,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	LastRefresh  string `json:"last_refresh"`
	Type         string `json:"type"`
	Expire       string `json:"expired"`

	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata.
func (ts *GrokTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile writes the storage to disk atomically (tmp + rename).
// Caller-provided path is created (with parents). Type is forced to "grok".
func (ts *GrokTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "grok"

	dir := filepath.Dir(authFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create grok auth dir: %w", err)
	}

	// Build payload via misc.MergeMetadata so injected metadata flattens
	// into the top-level JSON the way it does for codex.
	data, err := misc.MergeMetadata(ts, ts.Metadata)
	if err != nil {
		return fmt.Errorf("merge grok metadata: %w", err)
	}

	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode grok token: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, ".grok-token-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create tmp grok token: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanup := func() {
		_ = os.Remove(tmpPath)
	}

	if _, err := tmpFile.Write(encoded); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return fmt.Errorf("write tmp grok token: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return fmt.Errorf("fsync tmp grok token: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tmp grok token: %w", err)
	}
	if err := os.Rename(tmpPath, authFilePath); err != nil {
		cleanup()
		return fmt.Errorf("rename tmp grok token: %w", err)
	}
	// Best-effort chmod; non-fatal on filesystems that don't support it.
	_ = os.Chmod(authFilePath, 0600)
	return nil
}

// ApplyRefresh updates token fields after a successful refresh. The expires_in
// seconds is converted to an RFC3339 timestamp for the Expire field. If the
// server omitted a rotated refresh_token, the existing one is preserved.
func (ts *GrokTokenStorage) ApplyRefresh(tok *TokenResponse) {
	if tok == nil {
		return
	}
	ts.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		ts.RefreshToken = tok.RefreshToken
	}
	if tok.IDToken != "" {
		ts.IDToken = tok.IDToken
	}
	ts.LastRefresh = time.Now().UTC().Format(time.RFC3339)
	if tok.ExpiresIn > 0 {
		ts.Expire = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
}
