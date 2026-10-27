package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// DoClaudeImport imports a Claude OAuth credential JSON file and persists it
// using the configured auth store. A path of "-" reads the JSON from stdin.
func DoClaudeImport(cfg *config.Config, credentialPath string) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if resolved, errResolve := util.ResolveAuthDir(cfg.AuthDir); errResolve == nil {
		cfg.AuthDir = resolved
	}
	credentialPath = strings.TrimSpace(credentialPath)
	if credentialPath == "" {
		log.Error("claude-import: credential JSON path is required")
		return
	}

	raw, errRead := readClaudeCredential(credentialPath)
	if errRead != nil {
		log.Errorf("claude-import: read credential failed: %v", errRead)
		return
	}
	credential, errParse := claude.ParseImportedCredential(raw)
	if errParse != nil {
		log.Errorf("claude-import: %v", errParse)
		return
	}
	record, errBuild := claude.BuildImportedAuth(credential)
	if errBuild != nil {
		log.Errorf("claude-import: %v", errBuild)
		return
	}

	store := sdkAuth.GetTokenStore()
	if setter, ok := store.(interface{ SetBaseDir(string) }); ok {
		setter.SetBaseDir(cfg.AuthDir)
	}
	savedPath, errSave := store.Save(coreauth.WithAuthCreationIntent(context.Background()), record)
	if errSave != nil {
		log.Errorf("claude-import: save credential failed: %v", errSave)
		return
	}
	fmt.Printf("Claude credentials imported: %s\n", savedPath)
}

func readClaudeCredential(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
