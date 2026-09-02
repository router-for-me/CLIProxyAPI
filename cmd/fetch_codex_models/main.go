// Command fetch_codex_models connects to the Codex API using stored auth
// credentials and saves the dynamically fetched Codex client model catalog to a
// JSON file for inspection or offline use.
//
// Usage:
//
//	go run ./cmd/fetch_codex_models [flags]
//
// Flags:
//
//	--auths-dir       <path>  Directory containing auth JSON files (default: config auth-dir)
//	--config          <path>  Config file path                 (default: "config.yaml")
//	--output          <path>  Output JSON file path             (default: "codex_client_models.json")
//	--client-version <ver>   Codex client_version query value  (default: "0.144.1")
//	--pretty                 Pretty-print the output JSON      (default: true)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	codexModelsBaseURL       = "https://chatgpt.com/backend-api/codex"
	codexModelsPath          = "/models"
	defaultClientVersion     = "0.144.1"
	defaultCodexUserAgent    = "codex_cli_rs/0.144.1 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9"
	defaultCodexOriginator   = "codex_cli_rs"
	accessTokenRefreshLeeway = 30 * time.Second
)

func init() {
	logging.SetupBaseLogger()
	log.SetLevel(log.InfoLevel)
}

func main() {
	var authsDir string
	var configPath string
	var outputPath string
	var clientVersion string
	var pretty bool

	flag.StringVar(&authsDir, "auths-dir", "", "Directory containing auth JSON files (overrides config auth-dir)")
	flag.StringVar(&configPath, "config", "", "Configure File Path")
	flag.StringVar(&outputPath, "output", "codex_client_models.json", "Output JSON file path")
	flag.StringVar(&clientVersion, "client-version", defaultClientVersion, "Codex client_version query value")
	flag.BoolVar(&pretty, "pretty", true, "Pretty-print the output JSON")
	flag.Parse()
	authsDirOverridden := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "auths-dir" {
			authsDirOverridden = true
		}
	})

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	if strings.TrimSpace(configPath) == "" {
		configPath = filepath.Join(wd, "config.yaml")
	}
	cfg, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to load config file %s: %v\n", configPath, err)
		os.Exit(1)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	if !authsDirOverridden {
		authsDir = cfg.AuthDir
	} else if strings.TrimSpace(authsDir) != "" && !strings.HasPrefix(strings.TrimSpace(authsDir), "~") && !filepath.IsAbs(authsDir) {
		authsDir = filepath.Join(wd, authsDir)
	}
	if authsDir, err = util.ResolveAuthDir(authsDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to resolve auth directory: %v\n", err)
		os.Exit(1)
	}
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(wd, outputPath)
	}

	fmt.Printf("Scanning auth files in: %s\n", authsDir)

	fileStore := sdkauth.NewFileTokenStore()
	fileStore.SetBaseDir(authsDir)

	ctx := context.Background()
	auths, err := fileStore.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to list auth files: %v\n", err)
		os.Exit(1)
	}
	if len(auths) == 0 {
		fmt.Fprintf(os.Stderr, "error: no auth files found in %s\n", authsDir)
		os.Exit(1)
	}

	chosen := findCodexAuth(auths)
	if chosen == nil {
		fmt.Fprintf(os.Stderr, "error: no enabled codex auth found in %s\n", authsDir)
		os.Exit(1)
	}

	fmt.Printf("Using auth: id=%s label=%s\n", chosen.ID, chosen.Label)

	accessToken, refreshed, err := ensureAccessToken(ctx, fileStore, chosen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to prepare codex access token: %v\n", err)
		os.Exit(1)
	}
	if refreshed {
		fmt.Println("Refreshed Codex access token.")
	}

	fmt.Println("Fetching Codex model list from upstream...")

	raw, count, err := fetchModels(ctx, chosen, accessToken, clientVersion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to fetch codex models: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Fetched %d models.\n", count)

	if pretty {
		raw, err = prettyJSON(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to format JSON: %v\n", err)
			os.Exit(1)
		}
	}

	if err = os.WriteFile(outputPath, raw, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to write output file %s: %v\n", outputPath, err)
		os.Exit(1)
	}

	fmt.Printf("Model list saved to: %s\n", outputPath)
}

func findCodexAuth(auths []*coreauth.Auth) *coreauth.Auth {
	for _, auth := range auths {
		if auth == nil || auth.Disabled {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		if metaStringValue(auth.Metadata, "access_token") == "" && metaStringValue(auth.Metadata, "refresh_token") == "" {
			continue
		}
		return auth
	}
	return nil
}

func ensureAccessToken(ctx context.Context, store *sdkauth.FileTokenStore, auth *coreauth.Auth) (string, bool, error) {
	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken != "" {
		if expiresAt, ok := auth.ExpirationTime(); !ok || time.Now().Add(accessTokenRefreshLeeway).Before(expiresAt) {
			return accessToken, false, nil
		}
	}

	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		if accessToken != "" {
			return accessToken, false, nil
		}
		return "", false, fmt.Errorf("missing access_token and refresh_token")
	}

	svc := codexauth.NewCodexAuthWithProxyURL(nil, auth.ProxyURL)
	tokenData, errRefresh := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if errRefresh != nil {
		return "", false, errRefresh
	}
	if strings.TrimSpace(tokenData.AccessToken) == "" {
		return "", false, fmt.Errorf("refresh response did not include access_token")
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = tokenData.IDToken
	auth.Metadata["access_token"] = tokenData.AccessToken
	if tokenData.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenData.RefreshToken
	}
	if tokenData.AccountID != "" {
		auth.Metadata["account_id"] = tokenData.AccountID
	}
	if tokenData.Email != "" {
		auth.Metadata["email"] = tokenData.Email
	}
	auth.Metadata["expired"] = tokenData.Expire
	auth.Metadata["type"] = "codex"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)

	if _, errSave := store.Save(ctx, auth); errSave != nil {
		return "", false, fmt.Errorf("failed to save refreshed auth: %w", errSave)
	}

	return tokenData.AccessToken, true, nil
}

func fetchModels(ctx context.Context, auth *coreauth.Auth, accessToken, clientVersion string) ([]byte, int, error) {
	modelsURL, errURL := codexModelsURL(clientVersion)
	if errURL != nil {
		return nil, 0, errURL
	}

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if errReq != nil {
		return nil, 0, errReq
	}
	httpReq.Close = true
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Originator", defaultCodexOriginator)
	httpReq.Header.Set("User-Agent", defaultCodexUserAgent)
	if accountID := metaStringValue(auth.Metadata, "account_id"); accountID != "" {
		httpReq.Header.Set("Chatgpt-Account-Id", accountID)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}

	httpClient := &http.Client{}
	if auth != nil {
		if transport, _, errProxy := proxyutil.BuildHTTPTransport(auth.ProxyURL); errProxy == nil && transport != nil {
			httpClient.Transport = transport
		}
	}

	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		return nil, 0, errDo
	}

	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errClose := httpResp.Body.Close(); errClose != nil && errRead == nil {
		errRead = errClose
	}
	if errRead != nil {
		return nil, 0, errRead
	}

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil, 0, fmt.Errorf("models request failed with status %d: %s", httpResp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	count, errCount := countModels(bodyBytes)
	if errCount != nil {
		return nil, 0, errCount
	}
	return bodyBytes, count, nil
}

func codexModelsURL(clientVersion string) (string, error) {
	u, err := url.Parse(codexModelsBaseURL + codexModelsPath)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(clientVersion) != "" {
		q := u.Query()
		q.Set("client_version", strings.TrimSpace(clientVersion))
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func countModels(raw []byte) (int, error) {
	var payload struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, fmt.Errorf("failed to parse response JSON: %w", err)
	}
	// Keep this check intentionally loose: fetch_codex_models dumps the upstream
	// Codex API payload. Strict CPA catalog validation belongs in
	// cmd/validate_codex_models and registry.ValidateCodexClientModelsJSON.
	if payload.Models == nil {
		return 0, fmt.Errorf("response JSON does not contain models array")
	}
	return len(payload.Models), nil
}

func prettyJSON(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func metaStringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	default:
		return ""
	}
}
