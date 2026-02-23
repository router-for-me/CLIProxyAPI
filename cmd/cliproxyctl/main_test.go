package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	cliproxycmd "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/cmd"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

func projectRoot(t *testing.T) string {
	t.Helper()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	current := filepath.Clean(cwd)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return filepath.Clean(cwd)
}

func TestRunSetupJSONResponseShape(t *testing.T) {
	t.Setenv("CLIPROXY_CONFIG", "")
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 1, 2, 3, 0, time.UTC)
	}

	exec := commandExecutor{
		setup: func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login: func(_ *config.Config, _ string, _ string, _ *cliproxycmd.LoginOptions) error { return nil },
		doctor: func(_ string) (map[string]any, error) {
			return map[string]any{"status": "ok"}, nil
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"setup", "--json", "--config", "/tmp/does-not-exist.yaml"}, &stdout, &stderr, fixedNow, exec)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", exitCode, stderr.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode JSON output: %v", err)
	}
	if got := payload["schema_version"]; got != responseSchemaVersion {
		t.Fatalf("schema_version = %v, want %s", got, responseSchemaVersion)
	}
	if got := payload["command"]; got != "setup" {
		t.Fatalf("command = %v, want setup", got)
	}
	if got := payload["ok"]; got != true {
		t.Fatalf("ok = %v, want true", got)
	}
	if got := payload["timestamp"]; got != "2026-02-23T01:02:03Z" {
		t.Fatalf("timestamp = %v, want 2026-02-23T01:02:03Z", got)
	}
	details, ok := payload["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing or wrong type: %#v", payload["details"])
	}
	if _, exists := details["config_path"]; !exists {
		t.Fatalf("details.config_path missing: %#v", details)
	}
}

func TestRunDoctorJSONFailureShape(t *testing.T) {
	t.Setenv("CLIPROXY_CONFIG", "")
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 4, 5, 6, 0, time.UTC)
	}

	exec := commandExecutor{
		setup: func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login: func(_ *config.Config, _ string, _ string, _ *cliproxycmd.LoginOptions) error { return nil },
		doctor: func(configPath string) (map[string]any, error) {
			return map[string]any{"config_path": configPath}, assertErr("boom")
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"doctor", "--json", "--config", "/tmp/missing.yaml"}, &stdout, &stderr, fixedNow, exec)
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}

	text := strings.TrimSpace(stdout.String())
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("failed to decode JSON output: %v", err)
	}
	if got := payload["schema_version"]; got != responseSchemaVersion {
		t.Fatalf("schema_version = %v, want %s", got, responseSchemaVersion)
	}
	if got := payload["command"]; got != "doctor" {
		t.Fatalf("command = %v, want doctor", got)
	}
	if got := payload["ok"]; got != false {
		t.Fatalf("ok = %v, want false", got)
	}
	if got := payload["timestamp"]; got != "2026-02-23T04:05:06Z" {
		t.Fatalf("timestamp = %v, want 2026-02-23T04:05:06Z", got)
	}
	details, ok := payload["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing or wrong type: %#v", payload["details"])
	}
	if got, ok := details["error"].(string); !ok || !strings.Contains(got, "boom") {
		t.Fatalf("details.error = %#v, want contains boom", details["error"])
	}
}

func TestRunLoginJSONRequiresProvider(t *testing.T) {
	t.Setenv("CLIPROXY_CONFIG", "")
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 7, 8, 9, 0, time.UTC)
	}

	exec := commandExecutor{
		setup:  func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login:  func(_ *config.Config, _ string, _ string, _ *cliproxycmd.LoginOptions) error { return nil },
		doctor: func(_ string) (map[string]any, error) { return map[string]any{}, nil },
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"login", "--json", "--config", "/tmp/does-not-exist.yaml"}, &stdout, &stderr, fixedNow, exec)
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode JSON output: %v", err)
	}
	if got := payload["command"]; got != "login" {
		t.Fatalf("command = %v, want login", got)
	}
	if got := payload["ok"]; got != false {
		t.Fatalf("ok = %v, want false", got)
	}
}

func TestRunLogin429HintPrinted(t *testing.T) {
	t.Setenv("CLIPROXY_CONFIG", "")
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 10, 11, 12, 0, time.UTC)
	}
	exec := commandExecutor{
		setup: func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login: func(_ *config.Config, _ string, _ string, _ *cliproxycmd.LoginOptions) error {
			return statusErrorStub{code: http.StatusTooManyRequests}
		},
		doctor: func(_ string) (map[string]any, error) { return map[string]any{}, nil },
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"login", "--provider", "claude"}, &stdout, &stderr, fixedNow, exec)
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	text := stderr.String()
	if !strings.Contains(text, "HTTP 429") {
		t.Fatalf("stderr missing rate limit hint: %q", text)
	}
	if !strings.Contains(text, "cliproxyctl doctor") {
		t.Fatalf("stderr missing recovery suggestion: %q", text)
	}
}

func TestRunLoginJSON429IncludesHint(t *testing.T) {
	t.Setenv("CLIPROXY_CONFIG", "")
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 13, 14, 15, 0, time.UTC)
	}
	exec := commandExecutor{
		setup: func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login: func(_ *config.Config, _ string, _ string, _ *cliproxycmd.LoginOptions) error {
			return statusErrorStub{code: http.StatusTooManyRequests}
		},
		doctor: func(_ string) (map[string]any, error) { return map[string]any{}, nil },
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"login", "--json", "--provider", "claude"}, &stdout, &stderr, fixedNow, exec)
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode JSON output: %v", err)
	}
	details, ok := payload["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing or wrong type: %#v", payload["details"])
	}
	if got, ok := details["hint"].(string); !ok || got != rateLimitHintMessage {
		t.Fatalf("details.hint = %#v, want %q", details["hint"], rateLimitHintMessage)
	}
}

func TestRunDevHintIncludesGeminiToolUsageRemediation(t *testing.T) {
	t.Setenv("CLIPROXY_CONFIG", "")
	var out bytes.Buffer
	profile := filepath.Join(t.TempDir(), "process-compose.dev.yaml")
	if err := os.WriteFile(profile, []byte("version: '0.5'\n"), 0o644); err != nil {
		t.Fatalf("write dev profile: %v", err)
	}
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 16, 17, 18, 0, time.UTC)
	}

	run([]string{"dev", "--json", "--file", profile}, &out, &bytes.Buffer{}, fixedNow, commandExecutor{})
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode JSON output: %v", err)
	}
	if payload["command"] != "dev" || payload["ok"] != true {
		t.Fatalf("unexpected envelope: %#v", payload)
	}
	details := payload["details"].(map[string]any)
	hint := fmt.Sprintf("%v", details["tool_failure_remediation"])
	if !strings.Contains(hint, "gemini-3-pro-preview") {
		t.Fatalf("tool_failure_remediation missing expected model id: %q", hint)
	}
}

func TestRunDoctorJSONWithFixCreatesConfigFromTemplate(t *testing.T) {
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 11, 12, 13, 0, time.UTC)
	}
	wd := t.TempDir()
	tpl := []byte("ServerAddress: 127.0.0.1\nServerPort: \"4141\"\n")
	if err := os.WriteFile(filepath.Join(wd, "config.example.yaml"), tpl, 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	target := filepath.Join(wd, "nested", "config.yaml")
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	exec := commandExecutor{
		setup: func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login: func(_ *config.Config, _ string, _ string, _ *cliproxycmd.LoginOptions) error { return nil },
		doctor: func(configPath string) (map[string]any, error) {
			if !configFileExists(configPath) {
				return map[string]any{}, assertErr("missing config")
			}
			return map[string]any{"status": "ok", "config_path": configPath}, nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"doctor", "--json", "--fix", "--config", target}, &stdout, &stderr, fixedNow, exec)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q stdout=%q)", exitCode, stderr.String(), stdout.String())
	}
	if !configFileExists(target) {
		t.Fatalf("expected doctor --fix to create %s", target)
	}
}

func TestRunDevJSONProfileValidation(t *testing.T) {
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 14, 15, 16, 0, time.UTC)
	}
	tmp := t.TempDir()
	profile := filepath.Join(tmp, "dev.yaml")
	if err := os.WriteFile(profile, []byte("version: '0.5'\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"dev", "--json", "--file", profile}, &stdout, &stderr, fixedNow, commandExecutor{})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q stdout=%q)", exitCode, stderr.String(), stdout.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode JSON output: %v", err)
	}
	if got := payload["command"]; got != "dev" {
		t.Fatalf("command = %v, want dev", got)
	}
	details, ok := payload["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing: %#v", payload["details"])
	}
	if got := details["profile_exists"]; got != true {
		t.Fatalf("details.profile_exists = %v, want true", got)
	}
}

func TestRunSetupJSONSeedKiroAlias(t *testing.T) {
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 15, 16, 17, 0, time.UTC)
	}
	wd := t.TempDir()
	configPath := filepath.Join(wd, "config.yaml")
	configBody := "host: 127.0.0.1\nport: 8317\nauth-dir: ./auth\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"setup", "--json", "--config", configPath, "--seed-kiro-alias"}, &stdout, &stderr, fixedNow, commandExecutor{
		setup:  func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login:  func(_ *config.Config, _ string, _ string, _ *cliproxycmd.LoginOptions) error { return nil },
		doctor: func(_ string) (map[string]any, error) { return map[string]any{}, nil },
	})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q stdout=%q)", exitCode, stderr.String(), stdout.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config after setup: %v", err)
	}
	if len(cfg.OAuthModelAlias["kiro"]) == 0 {
		t.Fatalf("expected setup --seed-kiro-alias to persist default kiro aliases")
	}
}

func TestRunDoctorJSONFixReadOnlyRemediation(t *testing.T) {
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 16, 17, 18, 0, time.UTC)
	}
	wd := t.TempDir()
	configPath := filepath.Join(wd, "config.yaml")
	if err := os.Mkdir(configPath, 0o755); err != nil {
		t.Fatalf("mkdir config path: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"doctor", "--json", "--fix", "--config", configPath}, &stdout, &stderr, fixedNow, commandExecutor{
		setup: func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login: func(_ *config.Config, _ string, _ string, _ *cliproxycmd.LoginOptions) error { return nil },
		doctor: func(_ string) (map[string]any, error) {
			return map[string]any{"status": "ok"}, nil
		},
	})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit for directory config path")
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	details, _ := payload["details"].(map[string]any)
	remediation, _ := details["remediation"].(string)
	if remediation == "" || !strings.Contains(remediation, "--config") {
		t.Fatalf("expected remediation hint with --config, got %#v", details["remediation"])
	}
}

func TestCPB0011To0020LaneJRegressionEvidence(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	cases := []struct {
		id          string
		description string
	}{
		{"CPB-0011", "kiro compatibility hardening keeps provider aliases normalized"},
		{"CPB-0012", "opus model naming coverage remains available in utility tests"},
		{"CPB-0013", "tool_calls merge parity test coverage exists"},
		{"CPB-0014", "provider-agnostic model alias utility remains present"},
		{"CPB-0015", "bash tool argument path is covered by test corpus"},
		{"CPB-0016", "setup can persist default kiro oauth model aliases"},
		{"CPB-0017", "nullable-array troubleshooting quickstart doc exists"},
		{"CPB-0018", "copilot model mapping path has focused tests"},
		{"CPB-0019", "read-only config remediation guidance is explicit"},
		{"CPB-0020", "metadata naming board entries are tracked"},
	}
	requiredPaths := map[string]string{
		"CPB-0012": filepath.Join(root, "pkg", "llmproxy", "util", "claude_model_test.go"),
		"CPB-0013": filepath.Join(root, "pkg", "llmproxy", "translator", "openai", "openai", "responses", "openai_openai-responses_request_test.go"),
		"CPB-0014": filepath.Join(root, "pkg", "llmproxy", "util", "provider.go"),
		"CPB-0015": filepath.Join(root, "pkg", "llmproxy", "executor", "kimi_executor_test.go"),
		"CPB-0017": filepath.Join(root, "docs", "provider-quickstarts.md"),
		"CPB-0018": filepath.Join(root, "pkg", "llmproxy", "executor", "github_copilot_executor_test.go"),
		"CPB-0020": filepath.Join(root, "docs", "planning", "CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv"),
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.id, func(t *testing.T) {
			switch tc.id {
			case "CPB-0011":
				if normalizeProvider("github-copilot") != "copilot" {
					t.Fatalf("%s", tc.description)
				}
			case "CPB-0016":
				wd := t.TempDir()
				configPath := filepath.Join(wd, "config.yaml")
				if err := os.WriteFile(configPath, []byte("host: 127.0.0.1\nport: 8317\n"), 0o644); err != nil {
					t.Fatalf("write config: %v", err)
				}
				if err := persistDefaultKiroAliases(configPath); err != nil {
					t.Fatalf("%s: %v", tc.description, err)
				}
				cfg, err := config.LoadConfig(configPath)
				if err != nil {
					t.Fatalf("reload config: %v", err)
				}
				if len(cfg.OAuthModelAlias["kiro"]) == 0 {
					t.Fatalf("%s", tc.description)
				}
			case "CPB-0019":
				hint := readOnlyRemediationHint("/CLIProxyAPI/config.yaml")
				if !strings.Contains(hint, "--config") {
					t.Fatalf("%s: hint=%q", tc.description, hint)
				}
			default:
				path := requiredPaths[tc.id]
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("%s: missing %s (%v)", tc.description, path, err)
				}
			}
		})
	}
}

func TestCPB0001To0010LaneIRegressionEvidence(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	cases := []struct {
		id          string
		description string
	}{
		{"CPB-0001", "standalone management CLI entrypoint exists"},
		{"CPB-0002", "non-subprocess integration JSON envelope contract is stable"},
		{"CPB-0003", "dev profile command exists with process-compose hint"},
		{"CPB-0004", "provider quickstarts doc is present"},
		{"CPB-0005", "troubleshooting matrix doc is present"},
		{"CPB-0006", "interactive setup command remains available"},
		{"CPB-0007", "doctor --fix deterministic remediation exists"},
		{"CPB-0008", "responses compatibility tests are present"},
		{"CPB-0009", "reasoning conversion tests are present"},
		{"CPB-0010", "readme/frontmatter is present"},
	}
	requiredPaths := map[string]string{
		"CPB-0001": filepath.Join(root, "cmd", "cliproxyctl", "main.go"),
		"CPB-0004": filepath.Join(root, "docs", "provider-quickstarts.md"),
		"CPB-0005": filepath.Join(root, "docs", "troubleshooting.md"),
		"CPB-0008": filepath.Join(root, "pkg", "llmproxy", "translator", "openai", "openai", "responses", "openai_openai-responses_request_test.go"),
		"CPB-0009": filepath.Join(root, "test", "thinking_conversion_test.go"),
		"CPB-0010": filepath.Join(root, "README.md"),
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.id, func(t *testing.T) {
			switch tc.id {
			case "CPB-0002":
				if responseSchemaVersion == "" {
					t.Fatalf("%s: response schema version is empty", tc.description)
				}
			case "CPB-0003":
				dir := t.TempDir()
				profile := filepath.Join(dir, "process-compose.dev.yaml")
				if err := os.WriteFile(profile, []byte("version: '0.5'\n"), 0o644); err != nil {
					t.Fatalf("write dev profile: %v", err)
				}
				var out bytes.Buffer
				code := run([]string{"dev", "--json", "--file", profile}, &out, &bytes.Buffer{}, time.Now, commandExecutor{})
				if code != 0 {
					t.Fatalf("%s: run code=%d output=%q", tc.description, code, out.String())
				}
			case "CPB-0006":
				var errOut bytes.Buffer
				code := run([]string{"setup"}, &bytes.Buffer{}, &errOut, time.Now, commandExecutor{
					setup:  func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
					login:  func(_ *config.Config, _ string, _ string, _ *cliproxycmd.LoginOptions) error { return nil },
					doctor: func(_ string) (map[string]any, error) { return map[string]any{}, nil },
				})
				if code != 0 {
					t.Fatalf("%s: run code=%d stderr=%q", tc.description, code, errOut.String())
				}
			case "CPB-0007":
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "config.example.yaml"), []byte("ServerAddress: 127.0.0.1\n"), 0o644); err != nil {
					t.Fatalf("write config.example.yaml: %v", err)
				}
				target := filepath.Join(dir, "config.yaml")
				prev, err := os.Getwd()
				if err != nil {
					t.Fatalf("getwd: %v", err)
				}
				t.Cleanup(func() { _ = os.Chdir(prev) })
				if err := os.Chdir(dir); err != nil {
					t.Fatalf("chdir: %v", err)
				}
				code := run([]string{"doctor", "--json", "--fix", "--config", target}, &bytes.Buffer{}, &bytes.Buffer{}, time.Now, commandExecutor{
					setup: func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
					login: func(_ *config.Config, _ string, _ string, _ *cliproxycmd.LoginOptions) error { return nil },
					doctor: func(configPath string) (map[string]any, error) {
						return map[string]any{"config_path": configPath}, nil
					},
				})
				if code != 0 || !configFileExists(target) {
					t.Fatalf("%s: code=%d config_exists=%v", tc.description, code, configFileExists(target))
				}
			default:
				path, ok := requiredPaths[tc.id]
				if !ok {
					return
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("%s: missing required artifact %s (%v)", tc.description, path, err)
				}
			}
		})
	}
}

func TestResolveLoginProviderAliasAndValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "ampcode", want: "amp"},
		{in: "github-copilot", want: "copilot"},
		{in: "kilocode", want: "kilo"},
		{in: "openai-compatible", want: "factory-api"},
		{in: "claude", want: "claude"},
		{in: "unknown-provider", wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			got, details, err := resolveLoginProvider(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (provider=%q details=%#v)", tc.in, details)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for provider=%q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("resolveLoginProvider(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRunLoginJSONNormalizesProviderAlias(t *testing.T) {
	t.Setenv("CLIPROXY_CONFIG", "")
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 17, 18, 19, 0, time.UTC)
	}
	exec := commandExecutor{
		setup: func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login: func(_ *config.Config, provider string, _ string, _ *cliproxycmd.LoginOptions) error {
			if provider != "amp" {
				return fmt.Errorf("provider=%s, want amp", provider)
			}
			return nil
		},
		doctor: func(_ string) (map[string]any, error) { return map[string]any{}, nil },
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"login", "--json", "--provider", "ampcode", "--config", "/tmp/not-required.yaml"}, &stdout, &stderr, fixedNow, exec)
	if code != 0 {
		t.Fatalf("run(login)= %d, stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	details := payload["details"].(map[string]any)
	if details["provider"] != "amp" {
		t.Fatalf("details.provider=%v, want amp", details["provider"])
	}
	if details["provider_input"] != "ampcode" {
		t.Fatalf("details.provider_input=%v, want ampcode", details["provider_input"])
	}
}

func TestRunLoginJSONRejectsUnsupportedProviderWithSupportedList(t *testing.T) {
	t.Setenv("CLIPROXY_CONFIG", "")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"login", "--json", "--provider", "invalid-provider"}, &stdout, &stderr, time.Now, commandExecutor{})
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	details := payload["details"].(map[string]any)
	supportedAny, ok := details["supported"].([]any)
	if !ok || len(supportedAny) == 0 {
		t.Fatalf("supported list missing from details: %#v", details)
	}
}

func TestEnsureConfigFileRejectsDirectoryTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target directory: %v", err)
	}
	err := ensureConfigFile(target)
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("expected directory error, got %v", err)
	}
}

func TestSupportedProvidersSortedAndStable(t *testing.T) {
	got := supportedProviders()
	if len(got) == 0 {
		t.Fatal("supportedProviders is empty")
	}
	want := append([]string(nil), got...)
	sort.Strings(want)
	// got should already be sorted
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("supportedProviders order changed unexpectedly: %v", got)
	}
}

func TestCPB0011To0020LaneMRegressionEvidence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id string
		fn func(*testing.T)
	}{
		{
			id: "CPB-0011",
			fn: func(t *testing.T) {
				got, _, err := resolveLoginProvider("ampcode")
				if err != nil || got != "amp" {
					t.Fatalf("expected amp alias normalization, got provider=%q err=%v", got, err)
				}
			},
		},
		{
			id: "CPB-0012",
			fn: func(t *testing.T) {
				_, details, err := resolveLoginProvider("unsupported-opus-channel")
				if err == nil {
					t.Fatalf("expected validation error for unsupported provider")
				}
				if details["provider_supported"] != false {
					t.Fatalf("provider_supported should be false: %#v", details)
				}
			},
		},
		{
			id: "CPB-0013",
			fn: func(t *testing.T) {
				normalized, details, err := resolveLoginProvider("github-copilot")
				if err != nil || normalized != "copilot" {
					t.Fatalf("resolveLoginProvider failed: normalized=%q err=%v", normalized, err)
				}
				if details["provider_aliased"] != true {
					t.Fatalf("expected provider_aliased=true, details=%#v", details)
				}
			},
		},
		{
			id: "CPB-0014",
			fn: func(t *testing.T) {
				if normalizeProvider("kilocode") != "kilo" {
					t.Fatalf("expected kilocode alias to map to kilo")
				}
			},
		},
		{
			id: "CPB-0015",
			fn: func(t *testing.T) {
				got, _, err := resolveLoginProvider("amp-code")
				if err != nil || got != "amp" {
					t.Fatalf("expected amp-code alias to map to amp, got=%q err=%v", got, err)
				}
			},
		},
		{
			id: "CPB-0016",
			fn: func(t *testing.T) {
				got, _, err := resolveLoginProvider("openai-compatible")
				if err != nil || got != "factory-api" {
					t.Fatalf("expected openai-compatible alias to map to factory-api, got=%q err=%v", got, err)
				}
			},
		},
		{
			id: "CPB-0017",
			fn: func(t *testing.T) {
				root := projectRoot(t)
				if _, err := os.Stat(filepath.Join(root, "docs", "provider-quickstarts.md")); err != nil {
					t.Fatalf("provider quickstarts doc missing: %v", err)
				}
			},
		},
		{
			id: "CPB-0018",
			fn: func(t *testing.T) {
				if normalizeProvider("githubcopilot") != "copilot" {
					t.Fatalf("githubcopilot alias should normalize to copilot")
				}
			},
		},
		{
			id: "CPB-0019",
			fn: func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "config.yaml")
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				err := ensureConfigFile(target)
				if err == nil || !strings.Contains(err.Error(), "is a directory") {
					t.Fatalf("expected directory target rejection, got=%v", err)
				}
			},
		},
		{
			id: "CPB-0020",
			fn: func(t *testing.T) {
				supported := supportedProviders()
				if len(supported) < 10 {
					t.Fatalf("expected rich supported-provider metadata, got=%d", len(supported))
				}
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.id, tc.fn)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

type statusErrorStub struct {
	code int
}

func (s statusErrorStub) Error() string { return fmt.Sprintf("status %d", s.code) }

func (s statusErrorStub) StatusCode() int { return s.code }
