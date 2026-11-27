package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// When a client lists models the moment the proxy accepts connections, it sees
// the models of the credentials stored in the auth directory. Before the fix,
// the server listened before those models were registered, so early requests
// got "unknown provider for model".
func TestRunServesStoredCredentialModelsFromTheFirstConnection(t *testing.T) {
	// The prefix keeps this credential's model ids unique in the global registry.
	body := firstModelsListingWithWatcherHeld(t, map[string]string{
		"claude-startup@example.com.json": `{"type":"claude","email":"startup@example.com","access_token":"fake","expired":"2099-01-01T00:00:00Z","prefix":"startuptest"}`,
	})

	if !strings.Contains(body, `"id":"startuptest/claude-opus-5"`) {
		t.Fatalf("first /v1/models listing lacks the stored credential's model startuptest/claude-opus-5:\n%s", body)
	}
}

func TestRunFirstModelListingHonorsStoredCredentialExcludedModels(t *testing.T) {
	body := firstModelsListingWithWatcherHeld(t, map[string]string{
		"claude-excluded@example.com.json": `{"type":"claude","email":"excluded@example.com","access_token":"fake","expired":"2099-01-01T00:00:00Z","prefix":"startupexcluded","excluded_models":["claude-opus-5"]}`,
	})

	if !strings.Contains(body, `"id":"startupexcluded/`) {
		t.Fatalf("first /v1/models listing lacks the stored credential's models:\n%s", body)
	}
	if strings.Contains(body, `"id":"startupexcluded/claude-opus-5"`) {
		t.Fatalf("first /v1/models listing exposes startupexcluded/claude-opus-5, which the credential excludes:\n%s", body)
	}
}

func TestRunFirstModelListingHonorsStoredCodexFreePlan(t *testing.T) {
	body := firstModelsListingWithWatcherHeld(t, map[string]string{
		"codex-free@example.com.json": `{"type":"codex","email":"free@example.com","access_token":"fake","expired":"2099-01-01T00:00:00Z","prefix":"startupfree","plan_type":"free"}`,
	})

	if !strings.Contains(body, `"id":"startupfree/gpt-6-luna"`) {
		t.Fatalf("first /v1/models listing lacks the Free model startupfree/gpt-6-luna:\n%s", body)
	}
	if strings.Contains(body, `"id":"startupfree/gpt-6-astra"`) {
		t.Fatalf("first /v1/models listing exposes the Pro-only model startupfree/gpt-6-astra for a Free credential:\n%s", body)
	}
}

func TestRunFirstModelListingHonorsStoredCredentialModelAliases(t *testing.T) {
	body := firstModelsListingWithWatcherHeld(t, map[string]string{
		"claude-alias@example.com.json": `{"type":"claude","email":"alias@example.com","access_token":"fake","expired":"2099-01-01T00:00:00Z","prefix":"startupalias","model_aliases":[{"name":"claude-opus-5","alias":"opus-startup-alias"}]}`,
	})

	if !strings.Contains(body, `"id":"startupalias/opus-startup-alias"`) {
		t.Fatalf("first /v1/models listing lacks the credential's model alias startupalias/opus-startup-alias:\n%s", body)
	}
}

// The Postgres, git, and object stores mirror their credentials into the auth
// directory but set their own source_backend. Their credential rules must hold
// in the first listing too.
func TestRunFirstModelListingHonorsRulesOfNonFileStoreCredentials(t *testing.T) {
	var sourceBackend string
	body := firstModelsListingWithWatcherHeldStore(t, map[string]string{
		"codex-pgfree@example.com.json":      `{"type":"codex","email":"pgfree@example.com","access_token":"fake","expired":"2099-01-01T00:00:00Z","prefix":"startuppgfree","plan_type":"free"}`,
		"claude-pgexcluded@example.com.json": `{"type":"claude","email":"pgexcluded@example.com","access_token":"fake","expired":"2099-01-01T00:00:00Z","prefix":"startuppgexcluded","excluded_models":["claude-opus-5"]}`,
	}, newPostgresLikeStore, func(service *Service) {
		if auth, ok := service.coreManager.GetByID("claude-pgexcluded@example.com.json"); ok {
			sourceBackend = auth.Attributes[coreauth.AttributeSourceBackend]
		}
	})

	if strings.Contains(body, `"id":"startuppgfree/gpt-6-astra"`) {
		t.Fatalf("first /v1/models listing exposes the Pro-only model startuppgfree/gpt-6-astra for a Free postgres credential:\n%s", body)
	}
	if strings.Contains(body, `"id":"startuppgexcluded/claude-opus-5"`) {
		t.Fatalf("first /v1/models listing exposes startuppgexcluded/claude-opus-5, which the postgres credential excludes:\n%s", body)
	}
	if !strings.Contains(body, `"id":"startuppgfree/gpt-6-luna"`) {
		t.Fatalf("first /v1/models listing lacks the Free model startuppgfree/gpt-6-luna of the postgres credential:\n%s", body)
	}
	if !strings.Contains(body, `"id":"startuppgexcluded/`) {
		t.Fatalf("first /v1/models listing lacks the models of the postgres credential:\n%s", body)
	}
	if sourceBackend != coreauth.AuthSourcePostgres {
		t.Fatalf("before the watcher's first scan, source_backend = %q, want %q", sourceBackend, coreauth.AuthSourcePostgres)
	}
}

// The watcher's scan covers only the top level of the auth directory, so the
// startup batch must not publish a credential from a subdirectory, such as an
// archived account.
func TestRunFirstModelListingSkipsNestedCredentialFiles(t *testing.T) {
	// After the watcher starts, the plugin model sync registers every loaded
	// credential and nothing unregisters it at shutdown, so a fixed prefix
	// would leak into the next run under -count.
	archived := fmt.Sprintf("startuparchived%d", time.Now().UnixNano())
	body := firstModelsListingWithWatcherHeld(t, map[string]string{
		"claude-top@example.com.json":   `{"type":"claude","email":"top@example.com","access_token":"fake","expired":"2099-01-01T00:00:00Z","prefix":"startuptop"}`,
		"archive/" + archived + ".json": `{"type":"claude","email":"old@example.com","access_token":"fake","expired":"2099-01-01T00:00:00Z","prefix":"` + archived + `"}`,
	})

	if !strings.Contains(body, `"id":"startuptop/claude-opus-5"`) {
		t.Fatalf("first /v1/models listing lacks the top-level credential's model startuptop/claude-opus-5:\n%s", body)
	}
	if strings.Contains(body, `"id":"`+archived+`/`) {
		t.Fatalf("first /v1/models listing exposes the models of archive/%s.json, which the watcher's scan skips:\n%s", archived, body)
	}
}

// postgresLikeStore reads the auth directory like the Postgres store reads its
// spool, and marks each credential with source_backend postgres.
type postgresLikeStore struct {
	*sdkAuth.FileTokenStore
}

func newPostgresLikeStore(authDir string) coreauth.Store {
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	return postgresLikeStore{FileTokenStore: store}
}

func (s postgresLikeStore) List(ctx context.Context) ([]*coreauth.Auth, error) {
	auths, errList := s.FileTokenStore.List(ctx)
	for _, auth := range auths {
		if auth.Attributes == nil {
			auth.Attributes = make(map[string]string)
		}
		auth.Attributes[coreauth.AttributeSourceBackend] = coreauth.AuthSourcePostgres
	}
	return auths, errList
}

// firstModelsListingWithWatcherHeld starts the service with the given auth
// files and returns the first /v1/models listing. The watcher factory blocks
// until that listing is read, so it shows only what the service published
// before the watcher's first scan.
func firstModelsListingWithWatcherHeld(t *testing.T, authFiles map[string]string) string {
	t.Helper()
	return firstModelsListingWithWatcherHeldStore(t, authFiles, nil, nil)
}

// firstModelsListingWithWatcherHeldStore is firstModelsListingWithWatcherHeld
// with an optional store over the auth directory, and an optional inspect
// callback that runs after the listing is read and before the watcher starts.
func firstModelsListingWithWatcherHeldStore(t *testing.T, authFiles map[string]string, newStore func(authDir string) coreauth.Store, inspect func(*Service)) string {
	t.Helper()
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	for name, content := range authFiles {
		if errMkdir := os.MkdirAll(filepath.Dir(filepath.Join(authDir, name)), 0o700); errMkdir != nil {
			t.Fatal(errMkdir)
		}
		if errWrite := os.WriteFile(filepath.Join(authDir, name), []byte(content), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	port := freeStartupTestPort(t)
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := fmt.Sprintf(`config-version: 8
server:
  host: "127.0.0.1"
  port: %d
access:
  api-keys:
    - "startup-test-key"
requests:
  proxy-url: "http://127.0.0.1:9"
oauth:
  auth-dir: %q
`, port, authDir)
	if errWrite := os.WriteFile(configPath, []byte(configYAML), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("load config: %v", errLoad)
	}
	releaseWatcher := make(chan struct{})
	heldWatcherFactory := func(configPath, authDir string, reload func(*config.Config)) (*WatcherWrapper, error) {
		<-releaseWatcher
		return defaultWatcherFactory(configPath, authDir, reload)
	}
	builder := NewBuilder().WithConfig(cfg).WithConfigPath(configPath).WithWatcherFactory(heldWatcherFactory)
	if newStore != nil {
		builder = builder.WithCoreAuthManager(coreauth.NewManager(newStore(cfg.AuthDir), nil, nil))
	}
	service, errBuild := builder.Build()
	if errBuild != nil {
		t.Fatalf("build service: %v", errBuild)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	released := false
	release := func() {
		if !released {
			released = true
			close(releaseWatcher)
		}
	}
	t.Cleanup(func() {
		release()
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("service did not stop")
		}
	})

	body := firstModelsListing(t, port, done)
	if inspect != nil {
		inspect(service)
	}
	release()
	return body
}

// firstModelsListing sends GET /v1/models as soon as the port accepts a
// connection, the way a client already waiting on the proxy would.
func firstModelsListing(t *testing.T, port int, done <-chan error) string {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/models", port)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case errRun := <-done:
			t.Fatalf("service stopped before serving: %v", errRun)
		default:
		}
		req, errRequest := http.NewRequest(http.MethodGet, url, nil)
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		req.Header.Set("Authorization", "Bearer startup-test-key")
		resp, errDo := client.Do(req)
		if errDo != nil {
			var errOp *net.OpError
			if errors.As(errDo, &errOp) {
				continue
			}
			t.Fatalf("GET /v1/models: %v", errDo)
		}
		raw, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			t.Logf("close body: %v", errClose)
		}
		if errRead != nil {
			t.Fatalf("read /v1/models: %v", errRead)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/models status %d: %s", resp.StatusCode, raw)
		}
		return string(raw)
	}
	t.Fatal("proxy never accepted a connection")
	return ""
}

func freeStartupTestPort(t *testing.T) int {
	t.Helper()
	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if errClose := ln.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	return port
}
