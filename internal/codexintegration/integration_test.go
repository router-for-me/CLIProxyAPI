package codexintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCodexIntegrationReleaseGateSetupCatalogRouteDoctorRestore(t *testing.T) {
	modelFixture, err := os.ReadFile(filepath.Join("testdata", "e2e", "models.json"))
	if err != nil {
		t.Fatalf("read model fixture: %v", err)
	}
	var modelRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		gotAuthorization := request.Header.Get("Authorization")
		if gotAuthorization != "Bearer codex-integration-local" && gotAuthorization != "Bearer codex-doctor-local" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		modelRequests.Add(1)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(modelFixture)
	}))
	defer server.Close()

	lifecycle := doctorLifecycle(t, server.URL)
	writeDoctorCredentials(t, lifecycle.Config.AuthDir)
	originalConfig := []byte("# user-owned configuration\n\n[features]\nmulti_agent = true\n")
	if err = os.MkdirAll(lifecycle.Paths.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(lifecycle.Paths.ConfigFile, originalConfig, 0o600); err != nil {
		t.Fatal(err)
	}

	var setupOutput bytes.Buffer
	exitCode, err := RunCommand(context.Background(), lifecycle.Config, CommandOptions{
		Action: CommandSetup, Apply: true, JSON: true,
		CodexHome: lifecycle.Paths.Home, HTTPClient: server.Client(),
	}, &setupOutput)
	if err != nil || exitCode != 0 {
		t.Fatalf("setup command = (%d, %v), output=%s", exitCode, err, setupOutput.String())
	}
	var setup CommandOutput
	if err = json.Unmarshal(setupOutput.Bytes(), &setup); err != nil {
		t.Fatalf("decode setup output: %v", err)
	}
	if !setup.Changed || !setup.Applied || !setup.RestartRequired || setup.CatalogRevision == "" || setup.MappingRevision == "" {
		t.Fatalf("setup output = %#v", setup)
	}
	if got := modelRequests.Load(); got != 1 {
		t.Fatalf("model list requests = %d, want 1", got)
	}

	configured, err := os.ReadFile(lifecycle.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(configured, []byte("# user-owned configuration")) ||
		!bytes.Contains(configured, []byte("multi_agent = true")) ||
		!bytes.Contains(configured, []byte(ManagedConfigBegin)) {
		t.Fatalf("managed config did not preserve user configuration:\n%s", configured)
	}
	catalogData, err := os.ReadFile(lifecycle.Paths.CatalogFile)
	if err != nil {
		t.Fatal(err)
	}
	if err = registry.ValidateCodexClientModelsJSON(catalogData); err != nil {
		t.Fatalf("written catalog invalid: %v", err)
	}
	var catalogPayload struct {
		Models []map[string]any `json:"models"`
	}
	if err = json.Unmarshal(catalogData, &catalogPayload); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	bySlug := catalogBySlug(catalogPayload.Models)
	wantFeatured := []string{
		"gpt-5.6-sol",
		"xai/grok-4.5",
		"antigravity/gemini-3.6-flash",
		"antigravity/gemini-3.1-pro",
		"antigravity/claude-opus-4-6-thinking",
	}
	for index, slug := range wantFeatured {
		if got := catalogString(catalogPayload.Models[index], "slug"); got != slug {
			t.Fatalf("featured model %d = %q, want %q", index, got, slug)
		}
	}
	if build := bySlug["xai/grok-build-0.1"]; build == nil || catalogInt(build, "priority") <= len(wantFeatured) {
		t.Fatalf("Grok Build must be visible after featured five: %#v", build)
	}
	if got := catalogInt(bySlug["antigravity/gemini-3.1-pro"], "context_window"); got != 1048576 {
		t.Fatalf("Gemini 3.1 Pro context_window = %d, want 1048576", got)
	}

	policy, err := NewModelPolicy(lifecycle.Config.CodexIntegration.Models)
	if err != nil {
		t.Fatalf("NewModelPolicy() error = %v", err)
	}
	wantRoutes := map[string]struct {
		provider string
		upstream string
	}{
		"xai/grok-4.5":                         {provider: "xai", upstream: "grok-4.5"},
		"xai/grok-build-0.1":                   {provider: "xai", upstream: "grok-build-0.1"},
		"antigravity/gemini-3.6-flash":         {provider: "antigravity", upstream: "gemini-3.6-flash"},
		"antigravity/gemini-3.1-pro":           {provider: "antigravity", upstream: "gemini-pro-agent"},
		"antigravity/claude-opus-4-6-thinking": {provider: "antigravity", upstream: "claude-opus-4-6-thinking"},
	}
	for slug, want := range wantRoutes {
		mapping, ok := policy.Resolve(slug)
		if !ok || mapping.Provider != want.provider || mapping.UpstreamModel != want.upstream {
			t.Fatalf("route %q = %#v, %t; want %s/%s", slug, mapping, ok, want.provider, want.upstream)
		}
	}
	aliases := lifecycle.Config.EffectiveOAuthModelAlias()
	for slug, want := range wantRoutes {
		if !hasForcedAlias(aliases[want.provider], slug, want.upstream) {
			t.Fatalf("forced alias missing for %q -> %s/%s: %#v", slug, want.provider, want.upstream, aliases[want.provider])
		}
	}

	models, providers := commandCatalogSource(context.Background(), lifecycle, server.Client())
	report := RunDoctor(context.Background(), lifecycle, models, providers, DoctorOptions{
		HTTPClient: server.Client(), SkipOpenCodexCheck: true,
	})
	if report.ExitCode() != 0 || report.Status != "clean" {
		t.Fatalf("doctor report = %#v", report)
	}

	var restoreOutput bytes.Buffer
	exitCode, err = RunCommand(context.Background(), lifecycle.Config, CommandOptions{
		Action: CommandRestore, Apply: true, JSON: true,
		CodexHome: lifecycle.Paths.Home, HTTPClient: server.Client(),
	}, &restoreOutput)
	if err != nil || exitCode != 0 {
		t.Fatalf("restore command = (%d, %v), output=%s", exitCode, err, restoreOutput.String())
	}
	restoredConfig, err := os.ReadFile(lifecycle.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restoredConfig, originalConfig) {
		t.Fatalf("restored config = %q, want %q", restoredConfig, originalConfig)
	}
	if _, err = os.Stat(lifecycle.Paths.CatalogFile); !os.IsNotExist(err) {
		t.Fatalf("restore left managed catalog: %v", err)
	}
}

func hasForcedAlias(aliases []config.OAuthModelAlias, slug, upstream string) bool {
	for _, alias := range aliases {
		if alias.Alias == slug && alias.Name == upstream && alias.Fork && alias.ForceMapping {
			return true
		}
	}
	return false
}
