package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	cliproxycmd "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/cmd"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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
			details["error"] = runErr.Error()
			writeEnvelope(stdout, now, "setup", false, details)
			return 1
		}
		writeEnvelope(stdout, now, "setup", true, details)
		return 0
	}

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
			details["error"] = runErr.Error()
			writeEnvelope(stdout, now, "login", false, details)
			return 1
		}
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

func escapeForJSON(in string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return replacer.Replace(in)
}
