package executor

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestParseCommandAuthBearerToken(t *testing.T) {
	token, err := helps.ParseCommandAuthBearerToken([]byte("\nBearer test-token\n"))
	if err != nil {
		t.Fatalf("ParseCommandAuthBearerToken error: %v", err)
	}
	if token != "test-token" {
		t.Fatalf("token = %q, want test-token", token)
	}
}

func TestOpenAICompatPrepareRequestUsesCommandAuthMetadata(t *testing.T) {
	exec := NewOpenAICompatExecutor("openai-compatibility", nil)
	req, err := http.NewRequest(http.MethodGet, "https://example.com/v1/models", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": "https://example.com/v1"},
		Metadata:   map[string]any{"access_token": "dynamic-token"},
	}
	if err := exec.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer dynamic-token" {
		t.Fatalf("Authorization = %q, want Bearer dynamic-token", got)
	}
}

func TestCommandAuthPrepareExecutesCommandAndCaches(t *testing.T) {
	script := writeTokenScript(t, "Bearer dynamic-token")
	auth := &cliproxyauth.Auth{
		ID:         "command-auth",
		Provider:   "openai-compatibility",
		Attributes: commandAuthAttrs(script, 60000),
	}

	if !helps.ShouldPrepareCommandAuth(auth) {
		t.Fatal("expected command auth to require preparation")
	}
	updated, err := helps.PrepareCommandAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("PrepareCommandAuth error: %v", err)
	}
	if got, _ := updated.Metadata["access_token"].(string); got != "dynamic-token" {
		t.Fatalf("access_token = %q, want dynamic-token", got)
	}
	if !updated.NextRefreshAfter.After(time.Now()) {
		t.Fatalf("NextRefreshAfter = %v, want future", updated.NextRefreshAfter)
	}
	if helps.ShouldPrepareCommandAuth(updated) {
		t.Fatal("expected cached command auth to skip preparation")
	}
}

func TestCommandAuthPrepareFindsUserNPMGlobalBin(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".npm-global", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	script := filepath.Join(binDir, "command-auth-token")
	body := "#!/bin/sh\nprintf '%s\\n' 'Bearer user-bin-token'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write token script: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")

	auth := &cliproxyauth.Auth{
		ID:         "command-auth",
		Provider:   "openai-compatibility",
		Attributes: commandAuthAttrs("command-auth-token", 60000),
	}
	updated, err := helps.PrepareCommandAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("PrepareCommandAuth error: %v", err)
	}
	if got, _ := updated.Metadata["access_token"].(string); got != "user-bin-token" {
		t.Fatalf("access_token = %q, want user-bin-token", got)
	}
}

func TestManagerPrepareHttpRequestRunsCommandAuth(t *testing.T) {
	script := writeTokenScript(t, "manager-token")
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewOpenAICompatExecutor("openai-compatibility", nil))

	auth := &cliproxyauth.Auth{
		ID:         "command-auth",
		Provider:   "openai-compatibility",
		Attributes: commandAuthAttrs(script, 60000),
	}
	if _, err := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "https://example.com/v1/models", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := manager.PrepareHttpRequest(context.Background(), auth, req); err != nil {
		t.Fatalf("PrepareHttpRequest error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer manager-token" {
		t.Fatalf("Authorization = %q, want Bearer manager-token", got)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("expected auth in manager")
	}
	if got, _ := current.Metadata["access_token"].(string); got != "manager-token" {
		t.Fatalf("manager access_token = %q, want manager-token", got)
	}
}

func writeTokenScript(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "token.sh")
	body := "#!/bin/sh\nprintf '%s\\n' " + shellSingleQuote(token) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write token script: %v", err)
	}
	return path
}

func commandAuthAttrs(command string, refreshMS int) map[string]string {
	return map[string]string{
		"base_url":                                "https://example.com/v1",
		cliproxyauth.AttrAuthKind:                 cliproxyauth.AttrAuthKindAPIKey,
		cliproxyauth.AttrAuthSource:               cliproxyauth.AttrAuthSourceCommand,
		cliproxyauth.AttrAuthCommand:              command,
		cliproxyauth.AttrAuthArgsJSON:             "[]",
		cliproxyauth.AttrAuthTimeoutMS:            "5000",
		cliproxyauth.AttrAuthRefreshIntervalMS:    strconv.Itoa(refreshMS),
		cliproxyauth.AttrAuthInvalidatesOnNext401: "true",
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
