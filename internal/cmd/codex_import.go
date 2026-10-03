package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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
	}
	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "codex",
		FileName: fileName,
		Storage:  storage,
		Metadata: map[string]any{"email": email},
		Attributes: map[string]string{
			"plan_type": planType,
		},
	}

	store := sdkAuth.GetTokenStore()
	if setter, ok := store.(interface{ SetBaseDir(string) }); ok {
		setter.SetBaseDir(cfg.AuthDir)
	}
	path, errSave := store.Save(context.Background(), record)
	if errSave != nil {
		return "", fmt.Errorf("save credential: %w", errSave)
	}
	return path, nil
}
