package codexintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunDoctorCleanWithoutUpstreamProbes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Fatalf("unexpected request path %q", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	lifecycle := doctorLifecycle(t, server.URL)
	writeDoctorCredentials(t, lifecycle.Config.AuthDir)
	models, providers := catalogTestModels()
	if _, err := lifecycle.Setup(models, providers, true, false); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	report := RunDoctor(context.Background(), lifecycle, models, providers, DoctorOptions{
		HTTPClient:         server.Client(),
		SkipOpenCodexCheck: true,
	})
	if report.Status != "clean" || report.ExitCode() != 0 {
		t.Fatalf("RunDoctor() = %#v", report)
	}
	for _, check := range report.Checks {
		if check.Layer == "probe" {
			t.Fatalf("default doctor sent a model probe: %#v", check)
		}
	}
}

func TestRunDoctorClassifiesProbeFailureWithoutLeakingSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = response.Write([]byte(`{"object":"list","data":[]}`))
			return
		}
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{"error":"token-secret user@example.com"}`))
	}))
	defer server.Close()

	lifecycle := doctorLifecycle(t, server.URL)
	writeDoctorCredentials(t, lifecycle.Config.AuthDir)
	models, providers := catalogTestModels()
	if _, err := lifecycle.Setup(models, providers, true, false); err != nil {
		t.Fatal(err)
	}
	report := RunDoctor(context.Background(), lifecycle, models, providers, DoctorOptions{
		ProbeModels:        true,
		HTTPClient:         server.Client(),
		SkipOpenCodexCheck: true,
	})
	if report.ExitCode() != 2 || report.Status != "blocking" {
		t.Fatalf("RunDoctor() = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("token-secret")) || bytes.Contains(encoded, []byte("user@example.com")) {
		t.Fatalf("doctor leaked sensitive response data: %s", encoded)
	}
}

func TestRunDoctorReportsMissingProviderAndStaleCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()
	lifecycle := doctorLifecycle(t, server.URL)
	if err := os.MkdirAll(lifecycle.Config.AuthDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lifecycle.Config.AuthDir, "codex.json"), []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	models, providers := catalogTestModels()
	if _, err := lifecycle.Setup(models, providers, true, false); err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(lifecycle.Paths.CatalogFile)
	if err != nil {
		t.Fatal(err)
	}
	catalog = bytes.Replace(catalog, []byte(`"display_name": "Grok 4.5"`), []byte(`"display_name": "Changed"`), 1)
	if err = os.WriteFile(lifecycle.Paths.CatalogFile, catalog, 0o600); err != nil {
		t.Fatal(err)
	}
	report := RunDoctor(context.Background(), lifecycle, models, providers, DoctorOptions{
		HTTPClient:         server.Client(),
		SkipOpenCodexCheck: true,
	})
	if report.ExitCode() != 2 {
		t.Fatalf("RunDoctor().ExitCode() = %d, want 2: %#v", report.ExitCode(), report)
	}
	if !doctorHasCode(report, "catalog.stale") || !doctorHasCode(report, "oauth.antigravity_missing") || !doctorHasCode(report, "oauth.xai_missing") {
		t.Fatalf("missing expected checks: %#v", report.Checks)
	}
}

func TestExpectedFeaturedModelsFollowConfiguredSlugs(t *testing.T) {
	lifecycle := testLifecycle(t)
	models, providers := catalogTestModels()
	catalog, err := CompileCatalog(models, providers, lifecycle.Config.CodexIntegration)
	if err != nil {
		t.Fatal(err)
	}
	const customSlug = "xai/grok-primary"
	for index := range lifecycle.Config.CodexIntegration.Models {
		if lifecycle.Config.CodexIntegration.Models[index].Slug == "xai/grok-4.5" {
			lifecycle.Config.CodexIntegration.Models[index].Slug = customSlug
		}
	}
	catalog.Models[1]["slug"] = customSlug
	data, err := catalog.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !hasExpectedFeaturedModels(data, lifecycle.Config.CodexIntegration) {
		t.Fatal("configured featured slug was rejected")
	}
}

func TestValidateCommandOptions(t *testing.T) {
	tests := []struct {
		name    string
		options CommandOptions
		wantErr bool
	}{
		{name: "setup", options: CommandOptions{Action: CommandSetup}},
		{name: "doctor probe", options: CommandOptions{Action: CommandDoctor, ProbeModels: true}},
		{name: "doctor apply", options: CommandOptions{Action: CommandDoctor, Apply: true}, wantErr: true},
		{name: "setup probe", options: CommandOptions{Action: CommandSetup, ProbeModels: true}, wantErr: true},
		{name: "sync migration", options: CommandOptions{Action: CommandSync, MigrateOpenCodex: true}, wantErr: true},
		{name: "unknown", options: CommandOptions{Action: "unknown"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCommandOptions(test.options)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateCommandOptions() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestRunCommandSetupPreviewIsReadOnlyAndJSONStable(t *testing.T) {
	models, _ := catalogTestModels()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{"object": "list", "data": models})
	}))
	defer server.Close()
	lifecycle := doctorLifecycle(t, server.URL)
	var output bytes.Buffer
	exitCode, err := RunCommand(context.Background(), lifecycle.Config, CommandOptions{
		Action: CommandSetup, JSON: true, CodexHome: lifecycle.Paths.Home, HTTPClient: server.Client(),
	}, &output)
	if err != nil || exitCode != 0 {
		t.Fatalf("RunCommand() = %d, %v", exitCode, err)
	}
	var envelope CommandOutput
	if err = json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid JSON output %q: %v", output.String(), err)
	}
	if envelope.SchemaVersion != 1 || envelope.Action != CommandSetup || !envelope.Changed || envelope.Applied {
		t.Fatalf("command output = %#v", envelope)
	}
	if _, err = os.Stat(lifecycle.Paths.Home); !os.IsNotExist(err) {
		t.Fatalf("preview touched Codex home: %v", err)
	}
}

func doctorLifecycle(t *testing.T, serverURL string) *Lifecycle {
	t.Helper()
	lifecycle := testLifecycle(t)
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(serverURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Config.Host = host
	lifecycle.Config.Port = port
	lifecycle.Config.AuthDir = filepath.Join(t.TempDir(), "auth")
	return lifecycle
}

func writeDoctorCredentials(t *testing.T, authDir string) {
	t.Helper()
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"codex", "xai", "antigravity"} {
		body := `{"type":"` + provider + `","token":"secret","email":"user@example.com","expired":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`
		if err := os.WriteFile(filepath.Join(authDir, provider+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func doctorHasCode(report DoctorReport, code string) bool {
	for _, check := range report.Checks {
		if check.Code == code {
			return true
		}
	}
	return false
}
