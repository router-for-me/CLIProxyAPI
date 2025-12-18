package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

const debugBundleVersion = "1.0.0"

// DebugBundle represents the structure of a debug bundle
type DebugBundle struct {
	Version   string                 `json:"version"`
	Timestamp string                 `json:"timestamp"`
	System    SystemInfo             `json:"system"`
	Config    map[string]interface{} `json:"config"`
	Providers []ProviderState        `json:"providers"`
	Logs      []LogEntry             `json:"logs"`
	Metrics   MetricsSummary         `json:"metrics"`
}

// SystemInfo contains system information
type SystemInfo struct {
	OS          string `json:"os"`
	Platform    string `json:"platform"`
	Arch        string `json:"arch"`
	GoVersion   string `json:"goVersion"`
	AppVersion  string `json:"appVersion"`
	ConfigPath  string `json:"configPath"`
	NumCPU      int    `json:"numCpu"`
	NumRoutines int    `json:"numGoroutines"`
}

// ProviderState represents the state of a provider
type ProviderState struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	LastError   string `json:"lastError,omitempty"`
	LastSuccess string `json:"lastSuccess,omitempty"`
}

// LogEntry represents a log entry
type LogEntry struct {
	Timestamp     string `json:"timestamp"`
	Level         string `json:"level"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlationId,omitempty"`
}

// MetricsSummary contains metrics summary
type MetricsSummary struct {
	TotalRequests int     `json:"totalRequests"`
	SuccessRate   float64 `json:"successRate"`
	AvgLatencyMs  float64 `json:"avgLatencyMs"`
	TimeRange     string  `json:"timeRange"`
}

func runDebugBundleCommand(args []string) {
	fs := flag.NewFlagSet("debug-bundle", flag.ExitOnError)
	output := fs.String("output", "", "Output file path (default: stdout)")
	setVerboseFlag(fs)
	fs.Parse(args)

	if verbose {
		fmt.Fprintln(os.Stderr, "Generating debug bundle...")
	}

	bundle := generateDebugBundle()

	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		printError("KP-SYS-501", fmt.Sprintf("Failed to marshal bundle: %v", err))
		os.Exit(1)
	}

	if *output == "" {
		fmt.Println(string(data))
	} else {
		if err := os.WriteFile(*output, data, 0644); err != nil {
			printError("KP-SYS-503", fmt.Sprintf("Failed to write file: %v", err))
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Debug bundle saved to %s\n", *output)
	}
}

func generateDebugBundle() *DebugBundle {
	bundle := &DebugBundle{
		Version:   debugBundleVersion,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		System:    getSystemInfo(),
		Config:    getSanitizedConfig(),
		Providers: getProviderStates(),
		Logs:      getRecentLogs(100),
		Metrics:   getMetricsSummary(),
	}

	return bundle
}

func getSystemInfo() SystemInfo {
	return SystemInfo{
		OS:          runtime.GOOS,
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		Arch:        runtime.GOARCH,
		GoVersion:   runtime.Version(),
		AppVersion:  version,
		ConfigPath:  routing.GetConfigPath(),
		NumCPU:      runtime.NumCPU(),
		NumRoutines: runtime.NumGoroutine(),
	}
}

func getSanitizedConfig() map[string]interface{} {
	cfg, err := routing.LoadConfig()
	if err != nil {
		return map[string]interface{}{
			"error": fmt.Sprintf("Failed to load config: %v", err),
		}
	}

	// Convert to JSON and back to get a map
	data, _ := json.Marshal(cfg)
	var configMap map[string]interface{}
	json.Unmarshal(data, &configMap)

	// Sanitize sensitive fields
	sanitizeMap(configMap)

	return configMap
}

// secretPatterns matches common secret field names
var secretPatterns = regexp.MustCompile(`(?i)(api_?key|token|password|secret|auth|credential|bearer)`)

func sanitizeMap(m map[string]interface{}) {
	for key, value := range m {
		// Check if key looks like a secret field
		if secretPatterns.MatchString(key) {
			if str, ok := value.(string); ok && str != "" {
				m[key] = "***REDACTED***"
			}
			continue
		}

		// Recursively sanitize nested maps
		switch v := value.(type) {
		case map[string]interface{}:
			sanitizeMap(v)
		case []interface{}:
			for i, item := range v {
				if nested, ok := item.(map[string]interface{}); ok {
					sanitizeMap(nested)
					v[i] = nested
				}
			}
		case string:
			// Sanitize values that look like secrets
			if looksLikeSecret(v) {
				m[key] = "***REDACTED***"
			}
		}
	}
}

func looksLikeSecret(s string) bool {
	// Check for Bearer token first (can have spaces)
	if strings.HasPrefix(s, "Bearer ") && len(s) > 15 {
		return true
	}

	// Check for common secret patterns in values
	if len(s) > 20 && !strings.Contains(s, " ") {
		// Long strings without spaces might be tokens/keys
		// Check for common prefixes
		prefixes := []string{"sk-", "pk-", "api-", "eyJ", "ghp_", "gho_"}
		for _, prefix := range prefixes {
			if strings.HasPrefix(s, prefix) {
				return true
			}
		}
	}
	return false
}

func getProviderStates() []ProviderState {
	cfg, err := routing.LoadConfig()
	if err != nil {
		return []ProviderState{}
	}

	providers := getConfiguredProviders(cfg)
	states := make([]ProviderState, len(providers))

	for i, p := range providers {
		status := "unknown"
		if p.Enabled {
			status = "enabled"
		} else {
			status = "disabled"
		}

		states[i] = ProviderState{
			ID:     p.ID,
			Status: status,
		}
	}

	return states
}

func getRecentLogs(limit int) []LogEntry {
	// In a full implementation, this would read from the log files
	// For now, return empty array as logs are managed by the Electron app
	return []LogEntry{}
}

func getMetricsSummary() MetricsSummary {
	// In a full implementation, this would query the metrics store
	// For now, return placeholder
	return MetricsSummary{
		TotalRequests: 0,
		SuccessRate:   0,
		AvgLatencyMs:  0,
		TimeRange:     "24h",
	}
}
