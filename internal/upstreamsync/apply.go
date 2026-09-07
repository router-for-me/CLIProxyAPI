package upstreamsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// ProxyPoolLister is the minimal pool access the renderer needs. Satisfied
// by store.ProxyPoolStore; nil = no pool resolution (bindings keep manual
// proxy URLs).
type ProxyPoolLister interface {
	List(ctx context.Context) ([]store.ProxyPool, error)
}

// ApplyArtifacts re-renders config.yaml provider sections + auth-dir JSON
// files from the normalized upstream_providers rows, writing them into the
// spool paths. It returns a populated Config (the provider lists replaced)
// that callers can pass to the existing config-reload hook so the in-memory
// clients pick up the changes.
//
// configPath is the spooled config.yaml path; authDir is the spooled auth
// directory. The provider list fields of cfg are overwritten in place by the
// rendered rows; all other fields (server, plugins, oauth-model-alias, etc.)
// are preserved as-is. poolSource may be nil.
func ApplyArtifacts(ctx context.Context, st store.UpstreamProviderStore, cfg *config.Config, configPath, authDir string, poolSource ProxyPoolLister) (*config.Config, error) {
	if st == nil {
		return nil, fmt.Errorf("upstreamsync: store is nil")
	}
	if cfg == nil {
		return nil, fmt.Errorf("upstreamsync: config is nil")
	}
	providers, err := st.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("upstreamsync: list upstream providers: %w", err)
	}

	// Resolve proxy-pool bindings at render time (nil source = no pools).
	var pools poolLookup
	if poolSource != nil {
		poolRows, errPools := poolSource.List(ctx)
		if errPools != nil {
			return nil, fmt.Errorf("upstreamsync: list proxy pools: %w", errPools)
		}
		pools = buildPoolLookup(poolRows)
	}

	// Render config provider lists from rows.
	rendered := RenderConfigWithPools(providers, pools)
	merged := cfg.CloneForRuntime()
	merged.GeminiKey = rendered.GeminiKey
	merged.InteractionsKey = rendered.InteractionsKey
	merged.CodexKey = rendered.CodexKey
	merged.XAIKey = rendered.XAIKey
	merged.ClaudeKey = rendered.ClaudeKey
	merged.OpenAICompatibility = rendered.OpenAICompatibility
	merged.OpenCodeGo = rendered.OpenCodeGo
	merged.VertexCompatAPIKey = rendered.VertexCompatAPIKey

	// Preserve comments when writing the merged config back to the spool.
	if configPath != "" {
		if err := config.SaveConfigPreserveComments(configPath, merged); err != nil {
			return nil, fmt.Errorf("upstreamsync: write config: %w", err)
		}
	}

	// Render auth-dir JSON files for OAuth providers.
	if err := applyAuthArtifacts(ctx, st, authDir); err != nil {
		// Non-fatal: config reload can still proceed; OAuth providers will be
		// picked up on the next reload once their files are written.
		log.WithError(err).Warn("upstreamsync: apply auth artifacts failed")
	}
	return merged, nil
}

// applyAuthArtifacts re-renders each OAuth provider's auth-dir JSON file from
// its normalized row. Existing files not backed by a row are left untouched
// (operators may have manually-managed auth files); a row whose file_name
// matches an existing file overwrites it.
func applyAuthArtifacts(ctx context.Context, st store.UpstreamProviderStore, authDir string) error {
	providers, err := st.List(ctx)
	if err != nil {
		return err
	}
	if authDir != "" {
		if err := os.MkdirAll(authDir, 0o700); err != nil {
			return fmt.Errorf("upstreamsync: create auth dir: %w", err)
		}
	}
	for _, p := range providers {
		if !IsOAuth(p.ProviderType) {
			continue
		}
		fileName := strings.TrimSpace(p.FileName)
		if fileName == "" {
			continue
		}
		raw, errRender := RenderAuthFile(p)
		if errRender != nil {
			log.WithError(errRender).Warnf("upstreamsync: render auth file %s failed", fileName)
			continue
		}
		if authDir == "" {
			continue
		}
		fullPath := filepath.Join(authDir, fileName+".json")
		tmp := fullPath + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			return fmt.Errorf("upstreamsync: write auth file %s: %w", fileName, err)
		}
		if err := os.Rename(tmp, fullPath); err != nil {
			return fmt.Errorf("upstreamsync: rename auth file %s: %w", fileName, err)
		}
	}
	return nil
}
