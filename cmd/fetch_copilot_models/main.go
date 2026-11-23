// Command fetch_copilot_models exports an account's upstream /models response for inspection.
//
//	go run ./cmd/fetch_copilot_models --config config.yaml --output copilot_models.json
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Configuration file path")
	authsDir := flag.String("auths-dir", "", "Credential directory (overrides config auth-dir)")
	authID := flag.String("auth-id", "", "Credential file ID relative to auth-dir (default: first enabled Copilot account)")
	output := flag.String("output", "copilot_models.json", "Output JSON file path")
	pretty := flag.Bool("pretty", true, "Indent the exported JSON")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *configPath, *authsDir, *authID, *output, *pretty); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath, authsDir, authID, output string, pretty bool) error {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if authsDir == "" {
		authsDir = cfg.AuthDir
	}
	authsDir, err = util.ResolveAuthDir(authsDir)
	if err != nil {
		return err
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(authsDir)
	accounts, err := store.List(ctx)
	if err != nil {
		return err
	}
	if authID != "" {
		authID = filepath.Clean(authID)
		if runtime.GOOS == "windows" {
			authID = strings.ToLower(authID)
		}
	}
	var account *coreauth.Auth
	for _, a := range accounts {
		if !a.Disabled && strings.EqualFold(a.Provider, copilot.Provider) && (authID == "" || a.ID == authID) {
			account = a
			break
		}
	}
	if account == nil {
		return fmt.Errorf("no matching enabled Copilot credential in %s", authsDir)
	}
	raw, err := fetch(ctx, cfg, account)
	if err != nil {
		return err
	}
	if pretty {
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "", "  "); err != nil {
			return err
		}
		buf.WriteByte('\n')
		raw = buf.Bytes()
	}
	dir := filepath.Dir(output)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".copilot-models-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, errWrite := file.Write(raw)
	errClose := file.Close()
	if err := errors.Join(errWrite, errClose); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), output); err != nil {
		return err
	}
	fmt.Printf("Saved model list for %s to %s\n", account.ID, output)
	return nil
}

func fetch(ctx context.Context, cfg *config.Config, account *coreauth.Auth) (json.RawMessage, error) {
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, account, 0)
	base, _ := account.Metadata["base_url"].(string)
	token, _ := account.Metadata["access_token"].(string)
	grant, _ := account.Metadata["refresh_token"].(string)
	if account.HasValidAccessToken(time.Now()) {
		raw, err := copilot.FetchModels(ctx, client, base, token)
		var status copilot.HTTPError
		if grant == "" || !errors.As(err, &status) || status.Code != http.StatusUnauthorized {
			return raw, err
		}
	}
	if grant == "" {
		return nil, fmt.Errorf("Copilot credential has no usable access token or refresh token")
	}
	// The manual export can exchange an expired token without updating the credential file.
	authClient := *client
	authClient.Timeout = 30 * time.Second
	fresh, err := copilot.Exchange(ctx, &authClient, grant)
	if err != nil {
		return nil, err
	}
	base, err = fresh.API()
	if err != nil {
		return nil, err
	}
	return copilot.FetchModels(ctx, client, base, fresh.Value)
}
