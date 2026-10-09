package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type nativeCodexAuth struct {
	LastRefresh string `json:"last_refresh"`
	Tokens      struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// DoCodexImport imports a Codex CLI auth.json without modifying the source file.
func DoCodexImport(cfg *config.Config, authPath string) {
	path, errImport := importCodexAuth(cfg, authPath)
	if errImport != nil {
		log.Errorf("codex-import: %v", errImport)
		return
	}
	fmt.Printf("Codex credentials imported: %s\n", path)
}

func importCodexAuth(cfg *config.Config, authPath string) (string, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if resolved, errResolve := util.ResolveAuthDir(cfg.AuthDir); errResolve == nil {
		cfg.AuthDir = resolved
	}

	rawPath := strings.TrimSpace(authPath)
	if rawPath == "" {
		return "", fmt.Errorf("missing Codex auth.json path")
	}
	data, errRead := os.ReadFile(rawPath)
	if errRead != nil {
		return "", fmt.Errorf("read file: %w", errRead)
	}

	var source nativeCodexAuth
	if errUnmarshal := json.Unmarshal(data, &source); errUnmarshal != nil {
		return "", fmt.Errorf("invalid Codex auth.json: %w", errUnmarshal)
	}
	if strings.TrimSpace(source.Tokens.AccessToken) == "" {
		return "", fmt.Errorf("access token missing in Codex auth.json")
	}
	if strings.TrimSpace(source.Tokens.IDToken) == "" {
		return "", fmt.Errorf("ID token missing in Codex auth.json")
	}
	if strings.TrimSpace(source.Tokens.RefreshToken) == "" {
		return "", fmt.Errorf("refresh token missing in Codex auth.json")
	}

	email := ""
	planType := ""
	accountID := strings.TrimSpace(source.Tokens.AccountID)
	expire := ""
	if claims, errParse := codex.ParseJWTToken(source.Tokens.IDToken); errParse == nil && claims != nil {
		email = strings.TrimSpace(claims.Email)
		planType = strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
		if accountID == "" {
			accountID = strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID)
		}
		if claims.Exp > 0 {
			expire = time.Unix(int64(claims.Exp), 0).UTC().Format(time.RFC3339)
		}
	}
	if accountID == "" {
		return "", fmt.Errorf("account ID missing in Codex auth.json")
	}
	if source.LastRefresh == "" {
		source.LastRefresh = time.Now().UTC().Format(time.RFC3339)
	}
	// Same default as the login paths, so an import and a later login of one
	// account resolve to one credential file.
	if planType == "" {
		planType = codex.DefaultPlanType
	}

	digest := sha256.Sum256([]byte(accountID))
	accountHash := hex.EncodeToString(digest[:])[:8]
	fileName := codex.CredentialFileName(email, planType, accountHash, true)
	storage := &codex.CodexTokenStorage{
		IDToken:      source.Tokens.IDToken,
		AccessToken:  source.Tokens.AccessToken,
		RefreshToken: source.Tokens.RefreshToken,
		AccountID:    accountID,
		LastRefresh:  source.LastRefresh,
		Email:        email,
		Expire:       expire,
		PlanType:     planType,
	}
	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "codex",
		FileName: fileName,
		Storage:  storage,
		// The identity fields are set here so that the merge below can never take
		// them from an older file at the same path.
		Metadata: map[string]any{
			"type":       "codex",
			"account_id": accountID,
			"email":      email,
			"plan_type":  planType,
		},
		Attributes: map[string]string{
			"plan_type": planType,
		},
	}

	store := sdkAuth.GetTokenStore()
	if setter, ok := store.(interface{ SetBaseDir(string) }); ok {
		setter.SetBaseDir(cfg.AuthDir)
	}
	// Importing an account that already has a credential file must keep what the
	// user configured on it (disabled, proxy_url, excluded models), as a login does.
	if strings.TrimSpace(cfg.AuthDir) != "" {
		if existing := readExistingCredential(filepath.Join(cfg.AuthDir, fileName)); len(existing) > 0 {
			coreauth.MergeExistingAuthMetadata(record, existing)
		}
	}
	path, errSave := store.Save(context.Background(), record)
	if errSave != nil {
		return "", fmt.Errorf("save credential: %w", errSave)
	}
	return path, nil
}

// readExistingCredential returns the JSON object stored in an existing credential
// file, or nil when there is none. Only a regular file is read: Save refuses a
// symlink or a special file at this path, and reading through one first would
// defeat that refusal.
func readExistingCredential(path string) map[string]any {
	info, errStat := os.Lstat(path)
	if errStat != nil || !info.Mode().IsRegular() {
		return nil
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil || len(raw) == 0 {
		return nil
	}
	var existing map[string]any
	if errUnmarshal := json.Unmarshal(raw, &existing); errUnmarshal != nil {
		return nil
	}
	return existing
}
