// Package main demonstrates how to embed CLIProxyAPI in an external Go application.
//
// This example shows:
//   - Using the public EmbedConfig API (no internal package dependencies)
//   - Configuring essential server options
//   - OAuth authentication flows for Claude and Gemini
//   - Loading provider configurations from a YAML file
//   - Interactive streaming chat with conversation history
//   - Response verification using Gemini to fact-check Claude's responses
//   - Making test requests to verify authentication
//   - Starting and gracefully shutting down the service
//
// To run this example:
//  1. Run Claude OAuth login first: go run main.go -claude-login
//  2. (Optional) Run Gemini OAuth for verification: go run main.go -gemini-login
//  3. Start interactive chat: go run main.go -chat
//  4. Or just start server: go run main.go
//  5. Send SIGINT (Ctrl+C) to gracefully shutdown
//
// Verification feature:
//   - When Gemini is authenticated, Claude responses are automatically verified
//   - Use -verify=false to disable verification
//   - In chat, use 'verify on/off' to toggle, 'verify' to check status
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/chzyer/readline"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
	_ "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator/builtin" // Register all translators
	"github.com/sirupsen/logrus"
)

// ANSI color codes for terminal output
const (
	colorReset   = "\033[0m"
	colorCyan    = "\033[36m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorGray    = "\033[90m"
	colorRed     = "\033[31m"
	colorMagenta = "\033[35m"
)

// Default inactivity timeout before auto-shutdown
const defaultInactivityTimeout = 15 * time.Minute

// Verification constants
const (
	defaultVerificationTimeout = 30 * time.Second
	defaultCacheTTL            = 5 * time.Minute
	rateLimitCooldown          = 60 * time.Second
	defaultVerificationModel   = "gemini-2.5-flash"
	maxCorrectionAttempts      = 1 // Number of correction attempts after failed verification
)

// correctionPrompt is the template for asking Claude to correct based on verification feedback
const correctionPrompt = `The fact-checker found issues with your previous response:

%s

Please provide a corrected response that addresses these issues. Be accurate and concise.`

// Verification status emojis
const (
	statusVerified   = "✅"
	statusPartial    = "⚠️"
	statusInaccurate = "❌"
	statusUnable     = "ℹ️"
	statusCached     = "📋"
)

// verificationResult holds the result of a Gemini verification
type verificationResult struct {
	status    string // The status emoji
	text      string // The verification text
	cached    bool   // Whether this result came from cache
	timestamp time.Time
}

// verificationCache stores verification results with TTL
type verificationCache struct {
	entries map[string]*verificationResult
	ttl     time.Duration
	mu      sync.RWMutex
}

// newVerificationCache creates a new verification cache with the given TTL
func newVerificationCache(ttl time.Duration) *verificationCache {
	return &verificationCache{
		entries: make(map[string]*verificationResult),
		ttl:     ttl,
	}
}

// hashResponse creates a SHA-256 hash of the response text for cache key
func hashResponse(response string) string {
	h := sha256.Sum256([]byte(response))
	return hex.EncodeToString(h[:])
}

// get retrieves a cached verification result if it exists and hasn't expired
func (vc *verificationCache) get(responseHash string) (*verificationResult, bool) {
	vc.mu.RLock()
	defer vc.mu.RUnlock()

	entry, exists := vc.entries[responseHash]
	if !exists {
		return nil, false
	}

	// Check if entry has expired
	if time.Since(entry.timestamp) > vc.ttl {
		return nil, false
	}

	// Return a copy with cached flag set
	return &verificationResult{
		status:    entry.status,
		text:      entry.text,
		cached:    true,
		timestamp: entry.timestamp,
	}, true
}

// set stores a verification result in the cache
func (vc *verificationCache) set(responseHash string, result *verificationResult) {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	result.timestamp = time.Now()
	vc.entries[responseHash] = result
}

// cleanup removes expired entries from the cache
func (vc *verificationCache) cleanup() {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	now := time.Now()
	for key, entry := range vc.entries {
		if now.Sub(entry.timestamp) > vc.ttl {
			delete(vc.entries, key)
		}
	}
}

// rateLimiter tracks rate limit cooldowns
type rateLimiter struct {
	cooldownUntil time.Time
	mu            sync.RWMutex
}

// newRateLimiter creates a new rate limiter
func newRateLimiter() *rateLimiter {
	return &rateLimiter{}
}

// isLimited checks if we're currently rate limited
func (rl *rateLimiter) isLimited() bool {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	return time.Now().Before(rl.cooldownUntil)
}

// remainingCooldown returns the remaining cooldown time
func (rl *rateLimiter) remainingCooldown() time.Duration {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	remaining := time.Until(rl.cooldownUntil)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// triggerCooldown starts a rate limit cooldown
func (rl *rateLimiter) triggerCooldown() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.cooldownUntil = time.Now().Add(rateLimitCooldown)
}

// verificationState holds the state of verification during a chat session
type verificationState struct {
	enabled     bool
	proxyURL    string // URL of the local proxy (e.g., http://127.0.0.1:8317)
	model       string // Gemini model to use for verification
	httpClient  *http.Client
	cache       *verificationCache
	rateLimiter *rateLimiter
	timeout     time.Duration
}

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

// hasGeminiAuth checks if Gemini OAuth tokens exist in the auth directory
func hasGeminiAuth(authDir string) bool {
	// Look for gemini auth files (pattern: gemini-*.json)
	pattern := filepath.Join(authDir, "gemini-*.json")
	matches, err := filepath.Glob(pattern)
	return err == nil && len(matches) > 0
}

// initVerificationState sets up verification state for the chat session.
// Verification is enabled if Gemini auth exists and verifyFlag is true.
func initVerificationState(authDir string, host string, port int, verifyFlag bool) *verificationState {
	state := &verificationState{
		enabled:     false,
		proxyURL:    fmt.Sprintf("http://%s:%d", host, port),
		model:       defaultVerificationModel,
		httpClient:  &http.Client{Timeout: defaultVerificationTimeout},
		cache:       newVerificationCache(defaultCacheTTL),
		rateLimiter: newRateLimiter(),
		timeout:     defaultVerificationTimeout,
	}

	// If -verify=false was passed, don't enable verification
	if !verifyFlag {
		return state
	}

	// Check if Gemini auth exists
	if !hasGeminiAuth(authDir) {
		return state
	}

	state.enabled = true
	return state
}

// OpenAI-compatible request/response types for Gemini via proxy
type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type openAIChoice struct {
	Index   int           `json:"index"`
	Message openAIMessage `json:"message"`
	Delta   openAIMessage `json:"delta"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// verificationPrompt is the template for fact-checking requests
const verificationPrompt = `You are a fact-checker. Verify the accuracy of the following AI response.
Use web search to check any factual claims. Be concise.

Response to verify:
"""
%s
"""

Provide:
1. A brief verification status (start your response with exactly one of: ✅ Verified, ⚠️ Partially Verified, ❌ Inaccurate, or ℹ️ Unable to Verify)
2. Key findings from your verification (2-3 sentences max)
3. Only mention specific corrections if something is wrong

Do not repeat the original response. Focus only on verification.`

// verifyWithGemini sends the response to Gemini for verification via the local proxy.
// It handles caching, rate limiting, and error handling.
func verifyWithGemini(ctx context.Context, vs *verificationState, claudeResponse string, activity *activityTracker) *verificationResult {
	if !vs.enabled {
		return nil
	}

	// Check rate limit
	if vs.rateLimiter.isLimited() {
		remaining := vs.rateLimiter.remainingCooldown()
		fmt.Printf("%s⏳ Verification paused (rate limit cooldown: %ds remaining)%s\n",
			colorYellow, int(remaining.Seconds()), colorReset)
		return nil
	}

	// Check cache first
	responseHash := hashResponse(claudeResponse)
	if cached, found := vs.cache.get(responseHash); found {
		return cached
	}

	// Show verification in progress
	fmt.Printf("\n%s🔍 Verifying...%s", colorYellow, colorReset)

	// Create verification request
	prompt := fmt.Sprintf(verificationPrompt, claudeResponse)
	reqBody := openAIRequest{
		Model: vs.model,
		Messages: []openAIMessage{
			{Role: "user", Content: prompt},
		},
		Stream: true,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Printf("\r%s⚠️  Verification failed: %v%s\n", colorYellow, err, colorReset)
		return nil
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", vs.proxyURL+"/v1/chat/completions", strings.NewReader(string(jsonBody)))
	if err != nil {
		fmt.Printf("\r%s⚠️  Verification failed: %v%s\n", colorYellow, err, colorReset)
		return nil
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key") // Must match api-keys in config.yaml

	// Send request
	resp, err := vs.httpClient.Do(req)
	if err != nil {
		fmt.Printf("\r%s⚠️  Verification failed: %v%s\n", colorYellow, err, colorReset)
		return nil
	}
	defer resp.Body.Close()

	// Check for rate limit
	if resp.StatusCode == 429 {
		vs.rateLimiter.triggerCooldown()
		fmt.Printf("\r%s⚠️  Rate limited - verification paused for 60s%s\n", colorYellow, colorReset)
		return nil
	}

	// Check for other errors
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("\r%s⚠️  Verification failed (HTTP %d): %s%s\n", colorYellow, resp.StatusCode, string(body), colorReset)
		return nil
	}

	// Process streaming response
	var fullResponse strings.Builder
	var status string
	firstChunk := true

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}

		line = strings.TrimSpace(line)
		if line == "" || line == "data: [DONE]" {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")
		var streamResp openAIResponse
		if err := json.Unmarshal([]byte(jsonData), &streamResp); err != nil {
			continue
		}

		if len(streamResp.Choices) > 0 {
			content := streamResp.Choices[0].Delta.Content

			// On first chunk, clear the "Verifying..." and print header
			if firstChunk && content != "" {
				fmt.Printf("\r%s🔍 Gemini Verification:%s\n", colorYellow, colorReset)
				firstChunk = false

				// Try to extract status from first chunk
				if strings.HasPrefix(content, statusVerified) {
					status = statusVerified
				} else if strings.HasPrefix(content, statusPartial) {
					status = statusPartial
				} else if strings.HasPrefix(content, statusInaccurate) {
					status = statusInaccurate
				} else if strings.HasPrefix(content, statusUnable) {
					status = statusUnable
				}
			}

			// Stream the response text
			fmt.Print(content)
			fullResponse.WriteString(content)
		}
	}

	// Record activity after verification
	if activity != nil {
		activity.recordActivity()
	}

	// If still showing "Verifying...", clear it
	if firstChunk {
		fmt.Printf("\r%s⚠️  No verification response received%s\n", colorYellow, colorReset)
		return nil
	}

	fmt.Println() // New line after verification

	// If we didn't detect status from streaming, try to extract it from full response
	if status == "" {
		fullText := fullResponse.String()
		if strings.Contains(fullText, statusVerified) || strings.Contains(fullText, "Verified") {
			status = statusVerified
		} else if strings.Contains(fullText, statusPartial) || strings.Contains(fullText, "Partially") {
			status = statusPartial
		} else if strings.Contains(fullText, statusInaccurate) || strings.Contains(fullText, "Inaccurate") {
			status = statusInaccurate
		} else {
			status = statusUnable
		}
	}

	// Create and cache the result
	result := &verificationResult{
		status:    status,
		text:      fullResponse.String(),
		cached:    false,
		timestamp: time.Now(),
	}
	vs.cache.set(responseHash, result)

	return result
}

// displayCachedVerification shows a cached verification result
func displayCachedVerification(result *verificationResult) {
	if result == nil || !result.cached {
		return
	}

	fmt.Printf("\n%s🔍 Gemini Verification: %s Cached%s\n", colorYellow, statusCached, colorReset)
	fmt.Println(result.text)
}

// streamClaudeResponse streams a response from Claude and returns the full text.
// It handles the streaming events and prints to terminal in real-time.
func streamClaudeResponse(
	client *anthropic.Client,
	model string,
	systemPrompt string,
	messages []anthropic.MessageParam,
	header string,
) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fmt.Printf("\n%s%s%s ", colorCyan, header, colorReset)

	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.F(model),
		MaxTokens: anthropic.F(int64(4096)),
		System: anthropic.F([]anthropic.TextBlockParam{
			anthropic.NewTextBlock(systemPrompt),
		}),
		Messages: anthropic.F(messages),
	})

	var fullResponse strings.Builder

	for stream.Next() {
		event := stream.Current()

		switch delta := event.Delta.(type) {
		case anthropic.ContentBlockDeltaEventDelta:
			if delta.Type == "text_delta" {
				text := delta.Text
				fmt.Print(text)
				fullResponse.WriteString(text)
			}
		}
	}

	if err := stream.Err(); err != nil {
		return "", err
	}

	fmt.Println() // New line after response
	return fullResponse.String(), nil
}

// needsCorrection returns true if the verification status indicates the response needs correction
func needsCorrection(status string) bool {
	return status == statusInaccurate || status == statusPartial
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
func createEmbedConfig(chatMode bool, authDir string) *cliproxy.EmbedConfig {
	return &cliproxy.EmbedConfig{
		// Server host and port
		Host: "127.0.0.1", // Localhost-only for security
		Port: 8317,        // Default port

		// Authentication directory for OAuth tokens
		AuthDir: authDir,

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

// doGeminiLogin performs the Gemini OAuth authentication flow.
// This creates an auth file in the auth directory that contains the OAuth tokens.
func doGeminiLogin(noBrowser bool, projectID string, authDir string) error {
	fmt.Println("Starting Gemini OAuth authentication...")
	fmt.Printf("Using project ID: %s\n", projectID)
	fmt.Printf("Auth directory: %s\n", authDir)

	// Create auth manager with Gemini authenticator
	store := auth.GetTokenStore()
	if dirSetter, ok := store.(interface{ SetBaseDir(string) }); ok {
		dirSetter.SetBaseDir(authDir)
	}

	manager := auth.NewManager(store, auth.NewGeminiAuthenticator())

	// Login options
	loginOpts := &auth.LoginOptions{
		NoBrowser: noBrowser,
		ProjectID: projectID,
	}

	// Perform the login - this will open a browser and wait for OAuth callback
	authRecord, savedPath, err := manager.Login(context.Background(), "gemini", nil, loginOpts)
	if err != nil {
		return fmt.Errorf("gemini authentication failed: %w", err)
	}

	if authRecord != nil {
		fmt.Printf("✅ Gemini authentication successful!\n")
		if savedPath != "" {
			fmt.Printf("📁 Auth file saved to: %s\n", savedPath)
		}
		fmt.Println("\nYou can now enable response verification in chat mode with: go run main.go -chat")
	}

	return nil
}

// doClaudeLogin performs the Claude OAuth authentication flow.
// This creates an auth file in the auth directory that contains the OAuth tokens.
func doClaudeLogin(noBrowser bool, authDir string) error {
	fmt.Println("Starting Claude OAuth authentication...")
	fmt.Printf("Auth directory: %s\n", authDir)

	// Create auth manager with Claude authenticator
	store := auth.GetTokenStore()
	if dirSetter, ok := store.(interface{ SetBaseDir(string) }); ok {
		dirSetter.SetBaseDir(authDir)
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
// If verification is enabled, Claude's responses are verified using Gemini.
func runInteractiveChat(host string, port int, model string, activity *activityTracker, vs *verificationState) {
	// Wait for server to be fully started
	time.Sleep(2 * time.Second)

	// Record initial activity
	if activity != nil {
		activity.recordActivity()
	}

	// Format verification status and model info
	verifyStatus := "disabled"
	verifyModel := ""
	if vs != nil && vs.enabled {
		verifyStatus = "enabled"
		verifyModel = vs.model
	}

	fmt.Println()
	fmt.Printf("%s╭─────────────────────────────────────────────────────────╮%s\n", colorCyan, colorReset)
	fmt.Printf("%s│%s   🤖 Interactive Chat with Verification                 %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s   Chat: %-47s %s│%s\n", colorCyan, colorGray, model, colorCyan, colorReset)
	if verifyModel != "" {
		fmt.Printf("%s│%s   Verify: %-45s %s│%s\n", colorCyan, colorGray, verifyModel, colorCyan, colorReset)
	} else {
		fmt.Printf("%s│%s   Verify: %-45s %s│%s\n", colorCyan, colorGray, verifyStatus, colorCyan, colorReset)
	}
	fmt.Printf("%s│%s   Type 'help' for commands, 'quit' to exit              %s│%s\n", colorCyan, colorGray, colorCyan, colorReset)
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

	// Create readline instance with history and arrow key support
	rl, err := readline.NewEx(&readline.Config{
		Prompt:            fmt.Sprintf("%sYou:%s ", colorGreen, colorReset),
		HistoryFile:       filepath.Join(os.TempDir(), "cliproxy_chat_history"),
		HistoryLimit:      500,
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
		HistorySearchFold: true,
	})
	if err != nil {
		log.Fatalf("Failed to initialize readline: %v", err)
	}
	defer rl.Close()

	for {
		userInput, err := rl.Readline()
		if err != nil {
			// Handle Ctrl+C or Ctrl+D
			if err == readline.ErrInterrupt {
				continue
			}
			break
		}

		userInput = strings.TrimSpace(userInput)
		if userInput == "" {
			continue
		}

		// Record activity on each user interaction
		if activity != nil {
			activity.recordActivity()
		}

		// Handle special commands
		lowerInput := strings.ToLower(userInput)
		switch {
		case lowerInput == "quit" || lowerInput == "exit":
			fmt.Printf("\n%sGoodbye! 👋%s\n", colorYellow, colorReset)
			return
		case lowerInput == "clear":
			conversationHistory = nil
			fmt.Printf("%s🗑️  Conversation cleared%s\n\n", colorGray, colorReset)
			continue
		case lowerInput == "verify":
			// Show verification status
			if vs == nil {
				fmt.Printf("%sVerification: not configured (run -gemini-login first)%s\n\n", colorGray, colorReset)
			} else if vs.enabled {
				fmt.Printf("%sVerification: %senabled%s (using %s)%s\n\n", colorGray, colorGreen, colorGray, vs.model, colorReset)
			} else {
				fmt.Printf("%sVerification: %sdisabled%s\n\n", colorGray, colorYellow, colorReset)
			}
			continue
		case lowerInput == "verify on":
			if vs == nil {
				fmt.Printf("%s⚠️  Cannot enable verification - run 'go run main.go -gemini-login' first%s\n\n", colorYellow, colorReset)
			} else if !hasGeminiAuth(filepath.Dir(vs.proxyURL)) {
				// Check if auth exists using the auth dir from embedConfig
				vs.enabled = true
				fmt.Printf("%s✅ Verification enabled%s\n\n", colorGreen, colorReset)
			} else {
				vs.enabled = true
				fmt.Printf("%s✅ Verification enabled%s\n\n", colorGreen, colorReset)
			}
			continue
		case lowerInput == "verify off":
			if vs != nil {
				vs.enabled = false
				fmt.Printf("%s⏸️  Verification disabled%s\n\n", colorYellow, colorReset)
			} else {
				fmt.Printf("%sVerification is not configured%s\n\n", colorGray, colorReset)
			}
			continue
		case lowerInput == "help":
			fmt.Printf("\n%sCommands:%s\n", colorYellow, colorReset)
			fmt.Printf("  quit, exit   - End the chat session\n")
			fmt.Printf("  clear        - Clear conversation history\n")
			fmt.Printf("  verify       - Show verification status\n")
			fmt.Printf("  verify on    - Enable response verification\n")
			fmt.Printf("  verify off   - Disable response verification\n")
			fmt.Printf("  help         - Show this help message\n")
			fmt.Printf("\n%sFeatures:%s\n", colorYellow, colorReset)
			fmt.Printf("  • Arrow keys for history navigation and line editing\n")
			fmt.Printf("  • Auto-correction: if verification fails, Claude is asked to correct\n")
			if vs != nil && vs.enabled {
				fmt.Printf("\n%sVerification: %senabled%s\n", colorGray, colorGreen, colorReset)
			} else if vs != nil {
				fmt.Printf("\n%sVerification: %sdisabled%s\n", colorGray, colorYellow, colorReset)
			} else {
				fmt.Printf("\n%sVerification: not configured (run -gemini-login)%s\n", colorGray, colorReset)
			}
			fmt.Println()
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

		// Add assistant response to history
		responseText := fullResponse.String()
		if len(responseText) > 0 {
			conversationHistory = append(conversationHistory,
				anthropic.NewAssistantMessage(anthropic.NewTextBlock(responseText)),
			)

			// Verify the response with Gemini if enabled
			if vs != nil && vs.enabled {
				verifyCtx, verifyCancel := context.WithTimeout(context.Background(), vs.timeout)
				result := verifyWithGemini(verifyCtx, vs, responseText, activity)
				verifyCancel()

				// If result came from cache, display it differently
				if result != nil && result.cached {
					displayCachedVerification(result)
				}

				// If verification failed, feed back to Claude for correction
				if result != nil && needsCorrection(result.status) {
					for attempt := 0; attempt < maxCorrectionAttempts; attempt++ {
						fmt.Printf("\n%s🔄 Requesting correction from Claude...%s\n", colorYellow, colorReset)

						// Create correction request using the verification feedback
						correctionFeedback := fmt.Sprintf(correctionPrompt, result.text)
						conversationHistory = append(conversationHistory,
							anthropic.NewUserMessage(anthropic.NewTextBlock(correctionFeedback)),
						)

						// Stream corrected response
						correctedText, err := streamClaudeResponse(
							client,
							model,
							systemPrompt,
							conversationHistory,
							"Claude (corrected):",
						)

						if err != nil {
							fmt.Printf("%s❌ Correction failed: %v%s\n", colorYellow, err, colorReset)
							// Remove the correction request from history
							conversationHistory = conversationHistory[:len(conversationHistory)-1]
							break
						}

						// Add corrected response to history
						if len(correctedText) > 0 {
							conversationHistory = append(conversationHistory,
								anthropic.NewAssistantMessage(anthropic.NewTextBlock(correctedText)),
							)

							// Record activity
							if activity != nil {
								activity.recordActivity()
							}

							// Re-verify the corrected response
							verifyCtx, verifyCancel := context.WithTimeout(context.Background(), vs.timeout)
							result = verifyWithGemini(verifyCtx, vs, correctedText, activity)
							verifyCancel()

							if result != nil && result.cached {
								displayCachedVerification(result)
							}

							// If now verified, break out of correction loop
							if result == nil || !needsCorrection(result.status) {
								break
							}
						} else {
							break
						}
					}
				}
			}
		}

		fmt.Println() // Extra line before next prompt
	}
}

// defaultAuthDir returns the default auth directory, checking local first then home.
// Priority: ./.cli-proxy-api (if exists) > ~/.cli-proxy-api
func defaultAuthDir() string {
	// Check local directory first
	localDir := "./.cli-proxy-api"
	if info, err := os.Stat(localDir); err == nil && info.IsDir() {
		return localDir
	}

	// Fall back to home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return localDir // Use local if can't get home
	}
	homeDir := filepath.Join(home, ".cli-proxy-api")
	if info, err := os.Stat(homeDir); err == nil && info.IsDir() {
		return homeDir
	}

	// Default to local (will be created)
	return localDir
}

func main() {
	// Parse command-line flags
	var claudeLogin bool
	var geminiLogin bool
	var noBrowser bool
	var chatMode bool
	var model string
	var timeoutMinutes int
	var verifyEnabled bool
	var projectID string
	var authDir string

	flag.BoolVar(&claudeLogin, "claude-login", false, "Login to Claude using OAuth")
	flag.BoolVar(&geminiLogin, "gemini-login", false, "Login to Gemini using OAuth (enables response verification)")
	flag.StringVar(&projectID, "project_id", "", "Google Cloud project ID for Gemini (required for -gemini-login)")
	flag.BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically for OAuth")
	flag.BoolVar(&chatMode, "chat", false, "Start interactive chat mode after server starts")
	flag.StringVar(&model, "model", "claude-opus-4-5-20251101", "Model to use for chat (e.g., claude-opus-4-5-20251101, claude-sonnet-4-20250514)")
	flag.IntVar(&timeoutMinutes, "timeout", 15, "Inactivity timeout in minutes before auto-shutdown (0 to disable)")
	flag.BoolVar(&verifyEnabled, "verify", true, "Enable response verification with Gemini (requires -gemini-login)")
	flag.StringVar(&authDir, "auth-dir", defaultAuthDir(), "Directory for OAuth tokens (default: ./.cli-proxy-api or ~/.cli-proxy-api)")
	flag.Parse()

	// Ensure auth directory exists
	if err := os.MkdirAll(authDir, 0700); err != nil {
		log.Fatalf("Failed to create auth directory %s: %v", authDir, err)
	}

	// If login mode, perform OAuth and exit
	if claudeLogin {
		if err := doClaudeLogin(noBrowser, authDir); err != nil {
			log.Fatalf("Login failed: %v", err)
		}
		return
	}
	if geminiLogin {
		if projectID == "" {
			log.Fatal("Gemini login requires -project_id flag. Get your project ID from https://console.cloud.google.com")
		}
		if err := doGeminiLogin(noBrowser, projectID, authDir); err != nil {
			log.Fatalf("Gemini login failed: %v", err)
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
	embedCfg := createEmbedConfig(chatMode, authDir)

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

	// Initialize verification state for chat mode
	var vs *verificationState
	if chatMode {
		vs = initVerificationState(embedCfg.AuthDir, embedCfg.Host, embedCfg.Port, verifyEnabled)
		if vs.enabled {
			// Show a message that verification is enabled
			fmt.Printf("%s🔍 Gemini verification enabled%s\n", colorGreen, colorReset)
		} else if verifyEnabled && !hasGeminiAuth(embedCfg.AuthDir) {
			// User wanted verification but Gemini isn't configured
			fmt.Printf("%sℹ️  Response verification available - run 'go run main.go -gemini-login' to enable%s\n", colorGray, colorReset)
		}
	}

	// Start interactive chat or test message based on mode
	if chatMode {
		// Run interactive chat in a goroutine
		chatDone := make(chan struct{})
		go func() {
			runInteractiveChat(embedCfg.Host, embedCfg.Port, model, activity, vs)
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
