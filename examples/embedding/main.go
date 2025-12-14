// Package main demonstrates how to embed CLIProxyAPI in an external Go application.
//
// This example shows:
//   - Using the public EmbedConfig API (no internal package dependencies)
//   - Configuring essential server options
//   - OAuth authentication flow for Claude
//   - Loading provider configurations from a YAML file
//   - Interactive streaming chat with conversation history
//   - Making test requests to verify authentication
//   - Starting and gracefully shutting down the service
//
// To run this example:
//  1. Run OAuth login first: go run main.go -claude-login
//  2. Start interactive chat: go run main.go -chat
//  3. Or just start server: go run main.go
//  4. Send SIGINT (Ctrl+C) to gracefully shutdown
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
	"github.com/sirupsen/logrus"
)

// ANSI color codes for terminal output
const (
	colorReset  = "\033[0m"
	colorCyan   = "\033[36m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorGray   = "\033[90m"
)

// Default inactivity timeout before auto-shutdown
const defaultInactivityTimeout = 15 * time.Minute

// activityTracker monitors user activity and triggers shutdown on timeout
type activityTracker struct {
	lastActivity time.Time
	timeout      time.Duration
	timer        *time.Timer
	onTimeout    func()
	mu           sync.Mutex
}

// newActivityTracker creates a new activity tracker with the given timeout
func newActivityTracker(timeout time.Duration, onTimeout func()) *activityTracker {
	at := &activityTracker{
		lastActivity: time.Now(),
		timeout:      timeout,
		onTimeout:    onTimeout,
	}
	at.timer = time.AfterFunc(timeout, at.handleTimeout)
	return at
}

// recordActivity resets the inactivity timer
func (at *activityTracker) recordActivity() {
	at.mu.Lock()
	defer at.mu.Unlock()
	at.lastActivity = time.Now()
	at.timer.Reset(at.timeout)
}

// handleTimeout is called when the inactivity timeout expires
func (at *activityTracker) handleTimeout() {
	at.mu.Lock()
	elapsed := time.Since(at.lastActivity)
	at.mu.Unlock()

	// Double-check we've actually been inactive
	if elapsed >= at.timeout {
		if at.onTimeout != nil {
			at.onTimeout()
		}
	} else {
		// Reset timer for remaining time
		at.timer.Reset(at.timeout - elapsed)
	}
}

// stop stops the activity tracker
func (at *activityTracker) stop() {
	at.timer.Stop()
}

// suppressServerLogging configures logging to redirect to a file for chat mode.
// This keeps the terminal clean while still capturing all server logs.
func suppressServerLogging() (*os.File, error) {
	// Create logs directory
	if err := os.MkdirAll("./logs", 0755); err != nil {
		return nil, err
	}

	// Open log file (truncate on each run for cleaner logs)
	logFile, err := os.OpenFile("./logs/server.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}

	// Redirect logrus to file - keep INFO level to capture all server activity
	logrus.SetOutput(logFile)
	logrus.SetLevel(logrus.InfoLevel)

	// Redirect standard logger to file as well
	log.SetOutput(logFile)

	// Redirect Gin's default writers to the log file
	gin.DefaultWriter = logFile
	gin.DefaultErrorWriter = logFile

	return logFile, nil
}

// expandConfigEnvVars reads a config file and expands environment variables in ${VAR} syntax.
// Returns the path to a temporary file with expanded variables.
func expandConfigEnvVars(configPath string, quiet bool) (string, error) {
	// Read the config file
	content, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("failed to read config: %w", err)
	}

	// Expand environment variables
	expanded := os.ExpandEnv(string(content))

	// Create a temporary file
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer tmpFile.Close()

	// Write expanded content
	if _, err := tmpFile.WriteString(expanded); err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write temp config: %w", err)
	}

	if !quiet {
		log.Printf("Created expanded config file: %s", tmpFile.Name())
	}
	return tmpFile.Name(), nil
}

// createEmbedConfig creates and returns the EmbedConfig with all essential settings.
// When chatMode is true, logging is minimized to keep the chat UI clean.
func createEmbedConfig(chatMode bool) *cliproxy.EmbedConfig {
	return &cliproxy.EmbedConfig{
		// Server host and port
		Host: "127.0.0.1", // Localhost-only for security
		Port: 8317,        // Default port

		// Authentication directory for OAuth tokens
		AuthDir: "./auth",

		// Disable debug logging in chat mode to keep output clean
		Debug: !chatMode,

		// Log to file in chat mode to avoid cluttering the terminal
		LoggingToFile: chatMode,

		// Enable usage statistics tracking
		UsageStatisticsEnabled: true,

		// Retry configuration
		RequestRetry:     3,   // Retry failed requests 3 times
		MaxRetryInterval: 300, // Max 5 minutes between retries

		// TLS configuration (optional)
		TLS: cliproxy.TLSConfig{
			Enable: false, // Set to true for HTTPS
		},

		// Remote management configuration
		RemoteManagement: cliproxy.RemoteManagement{
			AllowRemote:         false, // Localhost-only for security
			DisableControlPanel: false, // Enable web UI
		},

		// Quota exceeded behavior
		QuotaExceeded: cliproxy.QuotaExceeded{
			SwitchProject:      false, // Auto-switch projects on quota exceeded
			SwitchPreviewModel: false, // Auto-switch to preview models
		},
	}
}

// doClaudeLogin performs the Claude OAuth authentication flow.
// This creates an auth file in the ./auth directory that contains the OAuth tokens.
func doClaudeLogin(noBrowser bool) error {
	fmt.Println("Starting Claude OAuth authentication...")

	// Create a minimal config for the login flow (not chat mode)
	embedCfg := createEmbedConfig(false)

	// Ensure auth directory exists
	if err := os.MkdirAll(embedCfg.AuthDir, 0755); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}

	// Create auth manager with Claude authenticator
	store := auth.GetTokenStore()
	if dirSetter, ok := store.(interface{ SetBaseDir(string) }); ok {
		dirSetter.SetBaseDir(embedCfg.AuthDir)
	}

	manager := auth.NewManager(store, auth.NewClaudeAuthenticator())

	// Since we're using the public SDK API, we need to create a minimal config
	// We can't import internal/config, so we'll use nil and let the authenticator handle it
	loginOpts := &auth.LoginOptions{
		NoBrowser: noBrowser,
	}

	// Perform the login - this will open a browser and wait for OAuth callback
	authRecord, savedPath, err := manager.Login(context.Background(), "claude", nil, loginOpts)
	if err != nil {
		return fmt.Errorf("claude authentication failed: %w", err)
	}

	if authRecord != nil {
		fmt.Printf("✅ Authentication successful!\n")
		if savedPath != "" {
			fmt.Printf("📁 Auth file saved to: %s\n", savedPath)
		}
		fmt.Println("\nYou can now start the server with: go run main.go")
	}

	return nil
}

// sendTestMessage sends a test message to Claude via the local proxy to verify authentication.
func sendTestMessage(host string, port int) {
	// Wait for server to be fully started
	time.Sleep(2 * time.Second)

	fmt.Println("\n🧪 Testing Claude authentication with a simple request...")

	// Create Anthropic SDK client pointing to our local proxy
	// The API key must match one in config.yaml's api-keys list
	client := anthropic.NewClient(
		option.WithAPIKey("test-key"), // Must match api-keys in config.yaml
		option.WithBaseURL(fmt.Sprintf("http://%s:%d", host, port)),
	)

	// Send a simple test message
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use model name that matches the proxy's registry (not Anthropic SDK constants)
	// Valid model names: claude-opus-4-5-20251101, claude-sonnet-4-5-20250929, claude-3-5-haiku-20241022
	message, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.F("claude-opus-4-5-20251101"),
		MaxTokens: anthropic.F(int64(100)),
		Messages: anthropic.F([]anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Say hello in one sentence!")),
		}),
	})

	if err != nil {
		log.Printf("❌ Test message failed: %v", err)
		log.Printf("   This might mean OAuth authentication is not set up.")
		log.Printf("   Run: go run main.go -claude-login")
		return
	}

	if len(message.Content) > 0 {
		if textBlock, ok := message.Content[0].AsUnion().(anthropic.TextBlock); ok {
			fmt.Printf("✅ Test successful! Claude responded: %s\n", textBlock.Text)
		}
	}
}

// runInteractiveChat starts an interactive streaming chat session with Claude.
// It maintains conversation history across turns and streams responses in real-time.
func runInteractiveChat(host string, port int, model string, activity *activityTracker) {
	// Wait for server to be fully started
	time.Sleep(2 * time.Second)

	// Record initial activity
	if activity != nil {
		activity.recordActivity()
	}

	fmt.Println()
	fmt.Printf("%s╭─────────────────────────────────────────────────────────╮%s\n", colorCyan, colorReset)
	fmt.Printf("%s│%s   🤖 Interactive Claude Chat                            %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s   Model: %-46s %s│%s\n", colorCyan, colorGray, model, colorCyan, colorReset)
	fmt.Printf("%s│%s   Type 'quit' or 'exit' to end, 'clear' to reset        %s│%s\n", colorCyan, colorGray, colorCyan, colorReset)
	fmt.Printf("%s╰─────────────────────────────────────────────────────────╯%s\n", colorCyan, colorReset)
	fmt.Println()

	// Create Anthropic SDK client pointing to our local proxy
	client := anthropic.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(fmt.Sprintf("http://%s:%d", host, port)),
	)

	// Maintain conversation history for multi-turn chat
	var conversationHistory []anthropic.MessageParam

	// System prompt to give Claude some personality
	systemPrompt := `You are a helpful, friendly AI assistant. You're running through CLIProxyAPI,
an open-source proxy that enables local access to AI APIs. Be concise but thorough in your responses.
When writing code, use markdown code blocks with the appropriate language tag.`

	scanner := bufio.NewScanner(os.Stdin)

	for {
		// Prompt for user input
		fmt.Printf("%sYou:%s ", colorGreen, colorReset)

		if !scanner.Scan() {
			break
		}

		userInput := strings.TrimSpace(scanner.Text())
		if userInput == "" {
			continue
		}

		// Record activity on each user interaction
		if activity != nil {
			activity.recordActivity()
		}

		// Handle special commands
		switch strings.ToLower(userInput) {
		case "quit", "exit":
			fmt.Printf("\n%sGoodbye! 👋%s\n", colorYellow, colorReset)
			return
		case "clear":
			conversationHistory = nil
			fmt.Printf("%s🗑️  Conversation cleared%s\n\n", colorGray, colorReset)
			continue
		case "help":
			fmt.Printf("\n%sCommands:%s\n", colorYellow, colorReset)
			fmt.Printf("  quit, exit  - End the chat session\n")
			fmt.Printf("  clear       - Clear conversation history\n")
			fmt.Printf("  help        - Show this help message\n\n")
			continue
		}

		// Add user message to history
		conversationHistory = append(conversationHistory,
			anthropic.NewUserMessage(anthropic.NewTextBlock(userInput)),
		)

		// Create streaming request
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)

		fmt.Printf("\n%sClaude:%s ", colorCyan, colorReset)

		// Use streaming for real-time response
		stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
			Model:     anthropic.F(model),
			MaxTokens: anthropic.F(int64(4096)),
			System: anthropic.F([]anthropic.TextBlockParam{
				anthropic.NewTextBlock(systemPrompt),
			}),
			Messages: anthropic.F(conversationHistory),
		})

		// Collect the full response for history
		var fullResponse strings.Builder

		// Stream the response
		for stream.Next() {
			event := stream.Current()

			// Handle content block delta events (the actual text)
			switch delta := event.Delta.(type) {
			case anthropic.ContentBlockDeltaEventDelta:
				if delta.Type == "text_delta" {
					text := delta.Text
					fmt.Print(text)
					fullResponse.WriteString(text)
				}
			}
		}

		cancel()

		if err := stream.Err(); err != nil {
			fmt.Printf("\n%s❌ Error: %v%s\n", colorYellow, err, colorReset)
			// Remove the failed user message from history
			if len(conversationHistory) > 0 {
				conversationHistory = conversationHistory[:len(conversationHistory)-1]
			}
			continue
		}

		fmt.Println() // New line after response
		fmt.Println()

		// Add assistant response to history
		if fullResponse.Len() > 0 {
			conversationHistory = append(conversationHistory,
				anthropic.NewAssistantMessage(anthropic.NewTextBlock(fullResponse.String())),
			)
		}
	}
}

func main() {
	// Parse command-line flags
	var claudeLogin bool
	var noBrowser bool
	var chatMode bool
	var model string
	var timeoutMinutes int

	flag.BoolVar(&claudeLogin, "claude-login", false, "Login to Claude using OAuth")
	flag.BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically for OAuth")
	flag.BoolVar(&chatMode, "chat", false, "Start interactive chat mode after server starts")
	flag.StringVar(&model, "model", "claude-opus-4-5-20251101", "Model to use for chat (e.g., claude-opus-4-5-20251101, claude-sonnet-4-20250514)")
	flag.IntVar(&timeoutMinutes, "timeout", 15, "Inactivity timeout in minutes before auto-shutdown (0 to disable)")
	flag.Parse()

	// If login mode, perform OAuth and exit
	if claudeLogin {
		if err := doClaudeLogin(noBrowser); err != nil {
			log.Fatalf("Login failed: %v", err)
		}
		return
	}
	// In chat mode, suppress server logging to keep terminal clean
	var logFile *os.File
	if chatMode {
		var err error
		logFile, err = suppressServerLogging()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not redirect logs: %v\n", err)
		}
	}

	// Load environment variables from .env if present
	if err := godotenv.Load(); err != nil {
		if !chatMode {
			log.Println("No .env file found or error loading it:", err)
		}
	} else {
		if !chatMode {
			log.Println("Loaded environment variables from .env")
		}
	}

	// Get the embed configuration (pass chatMode to suppress logging in chat)
	embedCfg := createEmbedConfig(chatMode)

	// Get absolute path to config file
	configPath := "./config.yaml"
	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		log.Fatalf("Failed to get absolute config path: %v", err)
	}

	// Check if config file exists
	if _, err := os.Stat(absConfigPath); os.IsNotExist(err) {
		log.Fatalf("Config file not found: %s\nPlease create config.yaml with your provider settings.", absConfigPath)
	}

	// Expand environment variables in config file (supports ${VAR} syntax)
	expandedConfigPath, err := expandConfigEnvVars(absConfigPath, chatMode)
	if err != nil {
		log.Fatalf("Failed to expand config environment variables: %v", err)
	}
	// Note: We intentionally don't delete the temp file here because the service
	// file watcher needs to keep monitoring it. It will be cleaned up on shutdown.

	if !chatMode {
		fmt.Println("Building CLIProxyAPI service...")
		fmt.Printf("Server configuration:\n")
		fmt.Printf("  Host: %s\n", embedCfg.Host)
		fmt.Printf("  Port: %d\n", embedCfg.Port)
		fmt.Printf("  Auth Directory: %s\n", embedCfg.AuthDir)
		fmt.Printf("  Config File: %s\n", absConfigPath)
		fmt.Println()
	}

	// Build the service using EmbedConfig + YAML config path
	// - EmbedConfig provides server configuration
	// - config.yaml provides provider configurations (API keys, OAuth accounts, etc.)
	svc, err := cliproxy.NewBuilder().
		WithEmbedConfig(embedCfg).
		WithConfigPath(expandedConfigPath).
		Build()
	if err != nil {
		log.Fatalf("Failed to build service: %v", err)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start the service in a goroutine
	errChan := make(chan error, 1)
	go func() {
		if !chatMode {
			fmt.Printf("Starting CLIProxyAPI on %s:%d\n", embedCfg.Host, embedCfg.Port)
			fmt.Printf("Management UI: http://%s:%d/\n", embedCfg.Host, embedCfg.Port)
			fmt.Printf("API Endpoint: http://%s:%d/v1\n", embedCfg.Host, embedCfg.Port)
			fmt.Printf("Press Ctrl+C to shutdown\n\n")
		}

		if err := svc.Run(ctx); err != nil {
			errChan <- fmt.Errorf("service error: %w", err)
		}
	}()

	// Set up inactivity timeout
	var timeout time.Duration
	if timeoutMinutes > 0 {
		timeout = time.Duration(timeoutMinutes) * time.Minute
	}

	// Channel for timeout-triggered shutdown
	timeoutChan := make(chan struct{})

	// Create activity tracker if timeout is enabled
	var activity *activityTracker
	if timeout > 0 {
		activity = newActivityTracker(timeout, func() {
			if chatMode {
				fmt.Printf("\n\n%s⏰ Session timed out after %d minutes of inactivity%s\n", colorYellow, timeoutMinutes, colorReset)
			}
			close(timeoutChan)
		})
		defer activity.stop()
	}

	// Helper function for clean shutdown
	shutdown := func(reason string) {
		if chatMode {
			fmt.Printf("\n%s%s%s\n", colorGray, reason, colorReset)
		} else {
			fmt.Printf("\n%s\n", reason)
		}
		cancel()
		if err := svc.Shutdown(ctx); err != nil {
			if !chatMode {
				log.Printf("Error during shutdown: %v", err)
			}
		}
		if logFile != nil {
			logFile.Close()
		}
		if !chatMode {
			fmt.Println("Service stopped gracefully")
		}
	}

	// Start interactive chat or test message based on mode
	if chatMode {
		// Run interactive chat in a goroutine
		chatDone := make(chan struct{})
		go func() {
			runInteractiveChat(embedCfg.Host, embedCfg.Port, model, activity)
			close(chatDone)
		}()

		// Wait for chat to finish, shutdown signal, timeout, or error
		select {
		case <-chatDone:
			shutdown("Chat session ended, stopping service...")

		case <-sigChan:
			shutdown("Received shutdown signal, stopping service...")

		case <-timeoutChan:
			shutdown("Shutting down due to inactivity...")

		case err := <-errChan:
			cancel()
			if logFile != nil {
				logFile.Close()
			}
			fmt.Fprintf(os.Stderr, "Service failed: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Send a test message after server starts (in a goroutine)
		// This verifies that OAuth authentication is working correctly
		go sendTestMessage(embedCfg.Host, embedCfg.Port)

		// Record activity after test message
		if activity != nil {
			go func() {
				time.Sleep(3 * time.Second)
				activity.recordActivity()
			}()
		}

		// Wait for shutdown signal, timeout, or error
		select {
		case <-sigChan:
			shutdown("Received shutdown signal, stopping service...")

		case <-timeoutChan:
			shutdown(fmt.Sprintf("Shutting down after %d minutes of inactivity...", timeoutMinutes))

		case err := <-errChan:
			cancel()
			log.Fatalf("Service failed: %v", err)
		}
	}
}
