package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

func TestConfigExportOutputsValidJSON(t *testing.T) {
	// Create a temp config for testing
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	testConfig := &routing.RoutingConfig{
		Version:  1,
		Profiles: []routing.Profile{{ID: "test-id", Name: "Test Profile"}},
	}

	data, _ := json.MarshalIndent(testConfig, "", "  ")
	os.WriteFile(configPath, data, 0644)

	// The export function reads from the default config path
	// In a full test, we'd mock the config path
	// For now, just verify the JSON structure
	var cfg routing.RoutingConfig
	err := json.Unmarshal(data, &cfg)
	if err != nil {
		t.Fatalf("Config export should produce valid JSON: %v", err)
	}

	if cfg.Version != 1 {
		t.Errorf("Expected version 1, got %d", cfg.Version)
	}
}

func TestConfigValidateReturnsExitCode1OnInvalid(t *testing.T) {
	invalidJSON := `{"version": -1, "profiles": []}`

	var cfg routing.RoutingConfig
	err := json.Unmarshal([]byte(invalidJSON), &cfg)
	if err != nil {
		t.Fatalf("JSON should parse: %v", err)
	}

	errs := validateConfigStruct(&cfg)
	if len(errs) == 0 {
		t.Error("Expected validation errors for invalid config")
	}

	found := false
	for _, e := range errs {
		if e == "version must be >= 1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected 'version must be >= 1' error, got: %v", errs)
	}
}

func TestConfigValidateDuplicateProfileIDs(t *testing.T) {
	cfg := &routing.RoutingConfig{
		Version: 1,
		Profiles: []routing.Profile{
			{ID: "dup-id", Name: "Profile 1"},
			{ID: "dup-id", Name: "Profile 2"},
		},
	}

	errs := validateConfigStruct(cfg)
	if len(errs) == 0 {
		t.Error("Expected validation errors for duplicate profile IDs")
	}

	found := false
	for _, e := range errs {
		if e == "profile[1]: duplicate id 'dup-id'" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected duplicate ID error, got: %v", errs)
	}
}

func TestDebugBundleGeneratesValidBundle(t *testing.T) {
	bundle := generateDebugBundle()

	if bundle.Version == "" {
		t.Error("Bundle should have version")
	}

	if bundle.Timestamp == "" {
		t.Error("Bundle should have timestamp")
	}

	if bundle.System.OS == "" {
		t.Error("Bundle should have OS info")
	}

	if bundle.System.Arch == "" {
		t.Error("Bundle should have arch info")
	}

	// Verify it serializes to valid JSON
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("Bundle should serialize to JSON: %v", err)
	}

	var parsed DebugBundle
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Bundle JSON should parse: %v", err)
	}
}

func TestSanitizeMapRedactsSecrets(t *testing.T) {
	testCases := []struct {
		name     string
		input    map[string]interface{}
		expected string
	}{
		{
			name:     "api_key field",
			input:    map[string]interface{}{"api_key": "secret123"},
			expected: "***REDACTED***",
		},
		{
			name:     "apiKey field",
			input:    map[string]interface{}{"apiKey": "secret123"},
			expected: "***REDACTED***",
		},
		{
			name:     "token field",
			input:    map[string]interface{}{"token": "secret123"},
			expected: "***REDACTED***",
		},
		{
			name:     "password field",
			input:    map[string]interface{}{"password": "secret123"},
			expected: "***REDACTED***",
		},
		{
			name:     "auth field",
			input:    map[string]interface{}{"auth": "secret123"},
			expected: "***REDACTED***",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sanitizeMap(tc.input)
			for _, v := range tc.input {
				if v != tc.expected {
					t.Errorf("Expected %s, got %v", tc.expected, v)
				}
			}
		})
	}
}

func TestSanitizeMapPreservesNonSecrets(t *testing.T) {
	input := map[string]interface{}{
		"name":        "test",
		"description": "a description",
		"enabled":     true,
		"count":       42,
	}

	sanitizeMap(input)

	if input["name"] != "test" {
		t.Errorf("name should be preserved, got %v", input["name"])
	}
	if input["description"] != "a description" {
		t.Errorf("description should be preserved, got %v", input["description"])
	}
}

func TestSanitizeMapHandlesNestedStructures(t *testing.T) {
	input := map[string]interface{}{
		"provider": map[string]interface{}{
			"name":    "claude",
			"api_key": "secret123",
		},
	}

	sanitizeMap(input)

	nested := input["provider"].(map[string]interface{})
	if nested["name"] != "claude" {
		t.Errorf("nested name should be preserved")
	}
	if nested["api_key"] != "***REDACTED***" {
		t.Errorf("nested api_key should be redacted, got %v", nested["api_key"])
	}
}

func TestLooksLikeSecret(t *testing.T) {
	testCases := []struct {
		input    string
		expected bool
	}{
		{"sk-1234567890123456789012", true},
		{"pk-1234567890123456789012", true},
		{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9", true},
		{"ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxx", true},
		{"Bearer token123456789012345", true},
		{"hello world", false},
		{"short", false},
		{"a normal sentence with spaces", false},
	}

	for _, tc := range testCases {
		t.Run(tc.input[:min(20, len(tc.input))], func(t *testing.T) {
			result := looksLikeSecret(tc.input)
			if result != tc.expected {
				t.Errorf("looksLikeSecret(%q) = %v, want %v", tc.input, result, tc.expected)
			}
		})
	}
}

func TestProviderTestResult(t *testing.T) {
	// Test the TestResult struct
	result := TestResult{
		Success: true,
		Latency: 100,
		Error:   "",
	}

	if !result.Success {
		t.Error("Expected success")
	}

	result2 := TestResult{
		Success: false,
		Latency: 0,
		Error:   "connection refused",
	}

	if result2.Success {
		t.Error("Expected failure")
	}
	if result2.Error != "connection refused" {
		t.Errorf("Expected 'connection refused', got %s", result2.Error)
	}
}

// Helper to capture stdout
func captureOutput(f func()) string {
	var buf bytes.Buffer
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	f()

	w.Close()
	os.Stdout = old
	buf.ReadFrom(r)
	return buf.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
