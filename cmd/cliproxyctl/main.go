package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	cliproxycmd "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/cmd"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

const responseSchemaVersion = "cliproxyctl.response.v1"

type responseEnvelope struct {
	SchemaVersion string         `json:"schema_version"`
	Command       string         `json:"command"`
	OK            bool           `json:"ok"`
	Timestamp     string         `json:"timestamp"`
	Details       map[string]any `json:"details"`
}

type commandExecutor struct {
	setup  func(*config.Config, *cliproxycmd.SetupOptions)
	login  func(*config.Config, string, *cliproxycmd.LoginOptions)
	doctor func(string) (map[string]any, error)
}

func defaultCommandExecutor() commandExecutor {
	return commandExecutor{
		setup: cliproxycmd.DoSetupWizard,
		login: cliproxycmd.DoLogin,
		doctor: func(configPath string) (map[string]any, error) {
			details := map[string]any{
				"config_path": configPath,
			}

			info, err := os.Stat(configPath)
			if err != nil {
				details["config_exists"] = false
				return details, fmt.Errorf("config file is not accessible: %w", err)
			}
			if info.IsDir() {
				details["config_exists"] = false
				return details, fmt.Errorf("config path %q is a directory", configPath)
			}
			details["config_exists"] = true

			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				return details, fmt.Errorf("failed to load config: %w", err)
			}

			authDir := strings.TrimSpace(cfg.AuthDir)
			details["auth_dir"] = authDir
			details["auth_dir_set"] = authDir != ""
			details["provider_counts"] = map[string]int{
				"codex":             len(cfg.CodexKey),
				"claude":            len(cfg.ClaudeKey),
				"gemini":            len(cfg.GeminiKey),
				"kiro":              len(cfg.KiroKey),
				"cursor":            len(cfg.CursorKey),
				"openai_compatible": len(cfg.OpenAICompatibility),
			}
			details["status"] = "ok"
			return details, nil
		},
	}
}

	case "roo-code":
		return "roo"
	case "roocode":
		return "roo"
	case "droid":
		return "gemini"
	case "droid-cli":
		return "gemini"
	case "droidcli":
		return "gemini"
	case "factoryapi":
		return "factory-api"
	case "openai-compatible":
		return "factory-api"
	default:
		return normalized
	}
}

=======
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now, defaultCommandExecutor()))
}

func run(args []string, stdout io.Writer, stderr io.Writer, now func() time.Time, exec commandExecutor) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: cliproxyctl <setup|login|doctor> [flags]")
		return 2
	}

	command := strings.TrimSpace(args[0])
	switch command {
	case "setup":
		return runSetup(args[1:], stdout, stderr, now, exec)
	case "login":
		return runLogin(args[1:], stdout, stderr, now, exec)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr, now, exec)
	default:
		if hasJSONFlag(args[1:]) {
			writeEnvelope(stdout, now, command, false, map[string]any{
				"error": "unknown command",
			})
			return 2
		}
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n", command)
		return 2
	}
}

func runSetup(args []string, stdout io.Writer, stderr io.Writer, now func() time.Time, exec commandExecutor) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var jsonOutput bool
	var configPathFlag string
	fs.BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON response")
	fs.StringVar(&configPathFlag, "config", "", "Path to config file")
	if err := fs.Parse(args); err != nil {
		return renderError(stdout, stderr, jsonOutput, now, "setup", err)
	}

	configPath := resolveConfigPath(strings.TrimSpace(configPathFlag))
	cfg, err := loadConfig(configPath, true)
	if err != nil {
		return renderError(stdout, stderr, jsonOutput, now, "setup", err)
	}

	details := map[string]any{
		"config_path":   configPath,
		"config_exists": configFileExists(configPath),
	}

	if jsonOutput {
		capturedStdout, capturedStderr, runErr := captureStdIO(func() error {
			exec.setup(cfg, &cliproxycmd.SetupOptions{ConfigPath: configPath})
			return nil
		})
		details["stdout"] = capturedStdout
		if capturedStderr != "" {
			details["stderr"] = capturedStderr
		}
		if runErr != nil {
			if hint := rateLimitHint(runErr); hint != "" {
				details["hint"] = hint
			}
=======
			details["error"] = runErr.Error()
			writeEnvelope(stdout, now, "setup", false, details)
			return 1
		}
		writeEnvelope(stdout, now, "setup", true, details)
		return 0
	}

				if hint := rateLimitHint(err); hint != "" {
					_, _ = fmt.Fprintln(stderr, hint)
				}
				return 1
			}
		}
	}
	if seedKiroAlias {
		if err := persistDefaultKiroAliases(configPath); err != nil {
			_, _ = fmt.Fprintf(stderr, "setup failed to seed kiro aliases: %v\n", err)
			return 1
		}
	}
=======
	exec.setup(cfg, &cliproxycmd.SetupOptions{ConfigPath: configPath})
	return 0
}

func runLogin(args []string, stdout io.Writer, stderr io.Writer, now func() time.Time, exec commandExecutor) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var jsonOutput bool
	var configPathFlag string
	var projectID string
	var noBrowser bool
	var callbackPort int
	fs.BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON response")
	fs.StringVar(&configPathFlag, "config", "", "Path to config file")
	fs.StringVar(&projectID, "project-id", "", "Optional Gemini project ID")
	fs.BoolVar(&noBrowser, "no-browser", false, "Do not open browser for OAuth login")
	fs.IntVar(&callbackPort, "oauth-callback-port", 0, "Override OAuth callback port")
	if err := fs.Parse(args); err != nil {
		return renderError(stdout, stderr, jsonOutput, now, "login", err)
	}

	configPath := resolveConfigPath(strings.TrimSpace(configPathFlag))
	cfg, err := loadConfig(configPath, true)
	if err != nil {
		return renderError(stdout, stderr, jsonOutput, now, "login", err)
	}

	details := map[string]any{
		"config_path":   configPath,
		"config_exists": configFileExists(configPath),
		"project_id":    strings.TrimSpace(projectID),
	}

	if jsonOutput {
		capturedStdout, capturedStderr, runErr := captureStdIO(func() error {
			exec.login(cfg, strings.TrimSpace(projectID), &cliproxycmd.LoginOptions{
				NoBrowser:    noBrowser,
				CallbackPort: callbackPort,
				ConfigPath:   configPath,
			})
			return nil
		})
		details["stdout"] = capturedStdout
		if capturedStderr != "" {
			details["stderr"] = capturedStderr
		}
		if runErr != nil {
			if hint := rateLimitHint(runErr); hint != "" {
				details["hint"] = hint
			}
=======
			details["error"] = runErr.Error()
			writeEnvelope(stdout, now, "login", false, details)
			return 1
		}
		if hint := rateLimitHint(err); hint != "" {
			_, _ = fmt.Fprintln(stderr, hint)
		}
		return 1
	}
=======
		ok := strings.Contains(capturedStdout, "Gemini authentication successful!")
		if !ok {
			details["error"] = "login flow did not report success"
		}
		writeEnvelope(stdout, now, "login", ok, details)
		if !ok {
			return 1
		}
		return 0
	}

	exec.login(cfg, strings.TrimSpace(projectID), &cliproxycmd.LoginOptions{
		NoBrowser:    noBrowser,
		CallbackPort: callbackPort,
		ConfigPath:   configPath,
	})
	return 0
}

func runDoctor(args []string, stdout io.Writer, stderr io.Writer, now func() time.Time, exec commandExecutor) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var jsonOutput bool
	var configPathFlag string
	fs.BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON response")
	fs.StringVar(&configPathFlag, "config", "", "Path to config file")
	if err := fs.Parse(args); err != nil {
		return renderError(stdout, stderr, jsonOutput, now, "doctor", err)
	}

	configPath := resolveConfigPath(strings.TrimSpace(configPathFlag))
	details, err := exec.doctor(configPath)
	if err != nil {
		if details == nil {
			details = map[string]any{}
		}
		details["error"] = err.Error()
		if jsonOutput {
			writeEnvelope(stdout, now, "doctor", false, details)
		} else {
			_, _ = fmt.Fprintf(stderr, "doctor failed: %v\n", err)
		}
		return 1
	}

	if details == nil {
		details = map[string]any{}
	}
	if jsonOutput {
		writeEnvelope(stdout, now, "doctor", true, details)
	} else {
		_, _ = fmt.Fprintf(stdout, "doctor ok (config=%s)\n", configPath)
	}
	return 0
}

		"profile_file":             path,
		"hint":                     fmt.Sprintf("process-compose -f %s up", path),
		"tool_failure_remediation": gemini3ProPreviewToolUsageRemediationHint(path),
	}
	info, err := os.Stat(path)
	if err != nil {
		details["profile_exists"] = false
		if jsonOutput {
			details["error"] = err.Error()
			writeEnvelope(stdout, now, "dev", false, details)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "dev profile missing: %v\n", err)
		return 1
	}
	if info.IsDir() {
		msg := fmt.Sprintf("dev profile path %q is a directory", path)
		details["profile_exists"] = false
		details["error"] = msg
		if jsonOutput {
			writeEnvelope(stdout, now, "dev", false, details)
			return 1
		}
		_, _ = fmt.Fprintln(stderr, msg)
		return 1
	}
	details["profile_exists"] = true

	if jsonOutput {
		writeEnvelope(stdout, now, "dev", true, details)
	} else {
		_, _ = fmt.Fprintf(stdout, "dev profile ok: %s\n", path)
		_, _ = fmt.Fprintf(stdout, "run: process-compose -f %s up\n", path)
		_, _ = fmt.Fprintf(stdout, "tool-failure triage hint: %s\n", gemini3ProPreviewToolUsageRemediationHint(path))
	}
	return 0
}

func gemini3ProPreviewToolUsageRemediationHint(profilePath string) string {
	profilePath = strings.TrimSpace(profilePath)
	if profilePath == "" {
		profilePath = "examples/process-compose.dev.yaml"
	}
	return fmt.Sprintf(
		"for gemini-3-pro-preview tool-use failures: touch config.yaml; process-compose -f %s down; process-compose -f %s up; curl -sS http://localhost:8317/v1/models -H \"Authorization: Bearer <client-key>\" | jq '.data[].id' | rg 'gemini-3-pro-preview'; curl -sS -X POST http://localhost:8317/v1/chat/completions -H \"Authorization: Bearer <client-key>\" -H \"Content-Type: application/json\" -d '{\"model\":\"gemini-3-pro-preview\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}],\"stream\":false}'",
		profilePath,
		profilePath,
	)
}

=======
func renderError(stdout io.Writer, stderr io.Writer, jsonOutput bool, now func() time.Time, command string, err error) int {
	if jsonOutput {
		writeEnvelope(stdout, now, command, false, map[string]any{
			"error": err.Error(),
		})
	} else {
		_, _ = fmt.Fprintln(stderr, err.Error())
	}
	return 2
}

func writeEnvelope(out io.Writer, now func() time.Time, command string, ok bool, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	envelope := responseEnvelope{
		SchemaVersion: responseSchemaVersion,
		Command:       command,
		OK:            ok,
		Timestamp:     now().UTC().Format(time.RFC3339Nano),
		Details:       details,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		fallback := fmt.Sprintf(
			`{"schema_version":"%s","command":"%s","ok":false,"timestamp":"%s","details":{"error":"json marshal failed: %s"}}`,
			responseSchemaVersion,
			command,
			now().UTC().Format(time.RFC3339Nano),
			escapeForJSON(err.Error()),
		)
		_, _ = io.WriteString(out, fallback+"\n")
		return
	}
	_, _ = out.Write(append(encoded, '\n'))
}

func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}

	lookup := []string{
		"CLIPROXY_CONFIG",
		"CLIPROXY_CONFIG_PATH",
		"CONFIG",
		"CONFIG_PATH",
	}
	for _, key := range lookup {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		return "config.yaml"
	}
	primary := filepath.Join(wd, "config.yaml")
	if configFileExists(primary) {
		return primary
	}

	nested := filepath.Join(wd, "config", "config.yaml")
	if configFileExists(nested) {
		return nested
	}
	return primary
}

func loadConfig(configPath string, allowMissing bool) (*config.Config, error) {
	cfg, err := config.LoadConfig(configPath)
	if err == nil {
		return cfg, nil
	}
	if allowMissing {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) && os.IsNotExist(pathErr.Err) {
			return &config.Config{}, nil
		}
	}
	return nil, err
}

func configFileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := ensureDirectoryWritable(configDir); err != nil {
		return fmt.Errorf("config directory not writable: %w", err)
	}

	templatePath := "config.example.yaml"
	payload, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", templatePath, err)
	}
	if err := os.WriteFile(configPath, payload, 0o644); err != nil {
		if errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return fmt.Errorf("write config file: %w; %s", err, readOnlyRemediationHint(configPath))
		}
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

func persistDefaultKiroAliases(configPath string) error {
	if err := ensureConfigFile(configPath); err != nil {
		return err
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config for alias seeding: %w", err)
	}
	cfg.SanitizeOAuthModelAlias()
	if err := config.SaveConfigPreserveComments(configPath, cfg); err != nil {
		return fmt.Errorf("save config with kiro aliases: %w", err)
	}
	return nil
}

func readOnlyRemediationHint(configPath string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return fmt.Sprintf("use --config to point to a writable file path instead of %q", configPath)
	}
	suggested := filepath.Join(home, ".cliproxy", "config.yaml")
	return fmt.Sprintf("use --config to point to a writable file path (for example %q)", suggested)
}

=======
func captureStdIO(runFn func() error) (string, string, error) {
	origStdout := os.Stdout
	origStderr := os.Stderr

	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return "", "", err
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		return "", "", err
	}

	os.Stdout = stdoutWrite
	os.Stderr = stderrWrite

	runErr := runFn()

	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()
	os.Stdout = origStdout
	os.Stderr = origStderr

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, stdoutRead)
	_ = stdoutRead.Close()
	var errBuf bytes.Buffer
	_, _ = io.Copy(&errBuf, stderrRead)
	_ = stderrRead.Close()

	return outBuf.String(), errBuf.String(), runErr
}

func hasJSONFlag(args []string) bool {
	for _, arg := range args {
		if strings.TrimSpace(arg) == "--json" {
			return true
		}
	}
	return false
}

const rateLimitHintMessage = "Provider returned HTTP 429 (too many requests). Pause or rotate credentials, run `cliproxyctl doctor`, and consult docs/troubleshooting.md#429 before retrying."

type statusCoder interface {
	StatusCode() int
}

func rateLimitHint(err error) string {
	if err == nil {
		return ""
	}
	var coder statusCoder
	if errors.As(err, &coder) && coder.StatusCode() == http.StatusTooManyRequests {
		return rateLimitHintMessage
	}
	return ""
}

func normalizeProviders(raw string) []string {
	parts := strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool {
		return r == ',' || r == ' '
	})
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		provider := normalizeProvider(strings.TrimSpace(part))
		if provider == "" || seen[provider] {
			continue
		}
		seen[provider] = true
		out = append(out, provider)
	}
	return out
}

func resolveLoginProvider(raw string) (string, map[string]any, error) {
	rawProvider := strings.TrimSpace(raw)
	if rawProvider == "" {
		return "", map[string]any{
			"provider_input":  rawProvider,
			"supported_count": len(supportedProviders()),
			"error":           "missing provider",
		}, errors.New("missing provider")
	}
	normalized := normalizeProvider(rawProvider)
	supported := supportedProviders()
	if !isSupportedProvider(normalized) {
		return "", map[string]any{
			"provider_input":     rawProvider,
			"provider_alias":     normalized,
			"provider_supported": false,
			"supported":          supported,
			"error":              fmt.Sprintf("unsupported provider %q", rawProvider),
		}, fmt.Errorf("unsupported provider %q (supported: %s)", rawProvider, strings.Join(supported, ", "))
	}
	return normalized, map[string]any{
		"provider_input":     rawProvider,
		"provider_alias":     normalized,
		"provider_supported": true,
		"provider_aliased":   rawProvider != normalized,
	}, nil
}

func isSupportedProvider(provider string) bool {
	_, ok := providerLoginHandlers()[provider]
	return ok
}

func supportedProviders() []string {
	handlers := providerLoginHandlers()
	out := make([]string, 0, len(handlers))
	for provider := range handlers {
		out = append(out, provider)
	}
	sort.Strings(out)
	return out
}

func providerLoginHandlers() map[string]struct{} {
	return map[string]struct{}{
		"gemini":      {},
		"claude":      {},
		"codex":       {},
		"kiro":        {},
		"cursor":      {},
		"copilot":     {},
		"minimax":     {},
		"kimi":        {},
		"deepseek":    {},
		"groq":        {},
		"mistral":     {},
		"siliconflow": {},
		"openrouter":  {},
		"together":    {},
		"fireworks":   {},
		"novita":      {},
		"roo":         {},
		"antigravity": {},
		"iflow":       {},
		"qwen":        {},
		"kilo":        {},
		"cline":       {},
		"amp":         {},
		"factory-api": {},
	}
}

func ensureDirectoryWritable(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("directory path is required")
	}
	probe, err := os.CreateTemp(dir, ".cliproxyctl-write-test-*")
	if err != nil {
		return err
	}
	probePath := probe.Name()
	_ = probe.Close()
	return os.Remove(probePath)
}

=======
func escapeForJSON(in string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return replacer.Replace(in)
}
