package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/errors"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

func runProviderCommand(args []string) {
	if len(args) < 1 {
		printProviderUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		runProviderList(args[1:])
	case "test":
		runProviderTest(args[1:])
	case "help", "-h", "--help":
		printProviderUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown provider subcommand: %s\n", args[0])
		printProviderUsage()
		os.Exit(1)
	}
}

func printProviderUsage() {
	fmt.Println(`korproxy provider - Manage providers

Usage:
  korproxy provider <subcommand> [options]

Subcommands:
  list    List all configured providers
  test    Test provider connectivity

Examples:
  korproxy provider list
  korproxy provider test claude
  korproxy provider test --all`)
}

// ProviderInfo represents provider information for display
type ProviderInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
}

func runProviderList(args []string) {
	fs := flag.NewFlagSet("provider list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	setVerboseFlag(fs)
	fs.Parse(args)

	cfg, err := routing.LoadConfig()
	if err != nil {
		printError("KP-CONF-201", fmt.Sprintf("Failed to load config: %v", err))
		os.Exit(1)
	}

	providers := getConfiguredProviders(cfg)

	if *jsonOutput {
		data, _ := json.MarshalIndent(providers, "", "  ")
		fmt.Println(string(data))
		return
	}

	if len(providers) == 0 {
		fmt.Println("No providers configured")
		return
	}

	fmt.Println("Configured Providers:")
	fmt.Println("---------------------")
	for _, p := range providers {
		status := "enabled"
		if !p.Enabled {
			status = "disabled"
		}
		fmt.Printf("  %s (%s) - %s\n", p.Name, p.ID, status)
		if verbose {
			fmt.Printf("    Type: %s\n", p.Type)
		}
	}
}

func getConfiguredProviders(cfg *routing.RoutingConfig) []ProviderInfo {
	var providers []ProviderInfo

	// Extract providers from provider groups
	// Each group has AccountIDs which reference configured accounts
	seenIDs := make(map[string]bool)
	for _, group := range cfg.ProviderGroups {
		for _, accountID := range group.AccountIDs {
			if seenIDs[accountID] {
				continue
			}
			seenIDs[accountID] = true
			providers = append(providers, ProviderInfo{
				ID:      accountID,
				Name:    accountID, // Use ID as name
				Type:    group.Name,
				Enabled: true, // Accounts in groups are enabled by default
				Status:  "unknown",
			})
		}
	}

	return providers
}

func runProviderTest(args []string) {
	fs := flag.NewFlagSet("provider test", flag.ExitOnError)
	testAll := fs.Bool("all", false, "Test all configured providers")
	timeout := fs.Duration("timeout", 30*time.Second, "Timeout for each test")
	setVerboseFlag(fs)
	fs.Parse(args)

	cfg, err := routing.LoadConfig()
	if err != nil {
		printError("KP-CONF-201", fmt.Sprintf("Failed to load config: %v", err))
		os.Exit(1)
	}

	providers := getConfiguredProviders(cfg)

	if *testAll {
		testAllProviders(providers, *timeout)
		return
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: provider ID required")
		fmt.Fprintln(os.Stderr, "Usage: korproxy provider test <provider-id> or korproxy provider test --all")
		os.Exit(1)
	}

	providerID := fs.Arg(0)
	testSingleProvider(providerID, *timeout)
}

func testSingleProvider(providerID string, timeout time.Duration) {
	if verbose {
		fmt.Printf("Testing provider: %s (timeout: %v)\n", providerID, timeout)
	}

	result := testProviderConnection(providerID, timeout)

	if result.Success {
		fmt.Printf("✓ %s: OK (latency: %v)\n", providerID, result.Latency)
	} else {
		fmt.Printf("✗ %s: FAILED - %s\n", providerID, result.Error)
		os.Exit(1)
	}
}

func testAllProviders(providers []ProviderInfo, timeout time.Duration) {
	if len(providers) == 0 {
		fmt.Println("No providers configured to test")
		return
	}

	fmt.Printf("Testing %d providers...\n\n", len(providers))

	successCount := 0
	failCount := 0

	for _, p := range providers {
		if !p.Enabled {
			fmt.Printf("⊘ %s: SKIPPED (disabled)\n", p.ID)
			continue
		}

		result := testProviderConnection(p.ID, timeout)

		if result.Success {
			fmt.Printf("✓ %s: OK (latency: %v)\n", p.ID, result.Latency)
			successCount++
		} else {
			fmt.Printf("✗ %s: FAILED - %s\n", p.ID, result.Error)
			failCount++
		}
	}

	fmt.Printf("\nSummary: %d passed, %d failed\n", successCount, failCount)

	if failCount > 0 {
		os.Exit(1)
	}
}

// TestResult represents the result of a provider test
type TestResult struct {
	Success bool
	Latency time.Duration
	Error   string
}

func testProviderConnection(providerID string, timeout time.Duration) TestResult {
	// Test by making a request to the local proxy for this provider
	// The proxy runs on localhost:1337
	client := &http.Client{Timeout: timeout}

	start := time.Now()

	// Make a simple health check request to the proxy
	// This tests if the provider is configured and accessible
	url := fmt.Sprintf("http://localhost:1337/v1/models")

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return TestResult{Success: false, Error: err.Error()}
	}

	// Add header to specify which provider to test
	req.Header.Set("X-Provider-ID", providerID)
	req.Header.Set("X-Test-Request", "true")

	resp, err := client.Do(req)
	if err != nil {
		// Check for common error types
		kpErr := errors.GetError("KP-NET-301")
		if kpErr != nil {
			return TestResult{Success: false, Error: fmt.Sprintf("%s: %v", kpErr.Message, err)}
		}
		return TestResult{Success: false, Error: err.Error()}
	}
	defer resp.Body.Close()

	latency := time.Since(start)

	if resp.StatusCode == http.StatusUnauthorized {
		return TestResult{Success: false, Error: "Authentication required - check provider credentials"}
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return TestResult{Success: false, Error: "Rate limited - try again later"}
	}

	if resp.StatusCode >= 400 {
		return TestResult{Success: false, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	return TestResult{Success: true, Latency: latency}
}
