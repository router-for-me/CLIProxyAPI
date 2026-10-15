package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// SnapshotAuthFile re-synthesizes one source file without consulting cached auth state.
// Parse errors are surfaced so deletion reconciliation never relies on fallback auths.
func (w *Watcher) SnapshotAuthFile(path string) ([]*coreauth.Auth, error) {
	if w == nil {
		return nil, fmt.Errorf("file auth snapshot is unavailable")
	}
	w.clientsMutex.RLock()
	cfg := w.config
	authDir := w.authDir
	parser := w.pluginAuthParser
	w.clientsMutex.RUnlock()
	if cfg == nil || authDir == "" {
		return nil, fmt.Errorf("file auth snapshot is not configured")
	}
	// Management uploads use absolute paths even when authDir is configured
	// relatively. Keep both absolute so synthesis retains file-relative IDs.
	absoluteAuthDir, errAbsAuthDir := filepath.Abs(authDir)
	if errAbsAuthDir != nil {
		return nil, fmt.Errorf("resolve auth directory: %w", errAbsAuthDir)
	}
	absolutePath, errAbsPath := filepath.Abs(path)
	if errAbsPath != nil {
		return nil, fmt.Errorf("resolve auth file path: %w", errAbsPath)
	}
	authDir = filepath.Clean(absoluteAuthDir)
	path = filepath.Clean(absolutePath)
	data, errReadFile := os.ReadFile(path)
	if errReadFile != nil {
		return nil, fmt.Errorf("read auth file: %w", errReadFile)
	}
	return synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
		Config:            cfg,
		AuthDir:           authDir,
		Now:               time.Now(),
		IDGenerator:       synthesizer.NewStableIDGenerator(),
		PluginAuthParser:  parser,
		StrictFileParsing: true,
	}, path, data)
}
