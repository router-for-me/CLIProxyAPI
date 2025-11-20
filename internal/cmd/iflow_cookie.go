package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/iflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// DoIFlowCookieAuth performs the iFlow cookie-based authentication.
func DoIFlowCookieAuth(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	promptFn := options.Prompt
	if promptFn == nil {
		reader := bufio.NewReader(os.Stdin)
		promptFn = func(prompt string) (string, error) {
			fmt.Print(prompt)
			value, err := reader.ReadString('\n')
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(value), nil
		}
	}

	// Prompt user for cookie
	cookie, err := promptForCookie(promptFn)
	if err != nil {
		fmt.Printf("Failed to get cookie: %v\n", err)
		return
	}

	// Authenticate with cookie
	auth := iflow.NewIFlowAuth(cfg)
	ctx := context.Background()

	tokenData, err := auth.AuthenticateWithCookie(ctx, cookie)
	if err != nil {
		fmt.Printf("iFlow cookie authentication failed: %v\n", err)
		return
	}

	// Create token storage
	tokenStorage := auth.CreateCookieTokenStorage(tokenData)

	// Get auth file path using email in filename
	authFilePath := getAuthFilePath(cfg, "iflow", tokenData.Email)

	// Save token to file
	if err := tokenStorage.SaveTokenToFile(authFilePath); err != nil {
		fmt.Printf("Failed to save authentication: %v\n", err)
		return
	}

	fmt.Printf("Authentication successful! API key: %s\n", tokenData.APIKey)
	fmt.Printf("Expires at: %s\n", tokenData.Expire)
	fmt.Printf("Authentication saved to: %s\n", authFilePath)
}

// promptForCookie prompts the user to enter their iFlow cookie
func promptForCookie(promptFn func(string) (string, error)) (string, error) {
	line, err := promptFn("Enter iFlow Cookie (from browser cookies): ")
	if err != nil {
		return "", fmt.Errorf("failed to read cookie: %w", err)
	}

	line = strings.TrimSpace(line)
	if line == "" {
		return "", fmt.Errorf("cookie cannot be empty")
	}

	// Clean up any extra whitespace and join multiple spaces
	cookie := strings.Join(strings.Fields(line), " ")

	// Ensure it ends properly
	if !strings.HasSuffix(cookie, ";") {
		cookie = cookie + ";"
	}

	// Ensure BXAuth is present in the cookie
	if !strings.Contains(cookie, "BXAuth=") {
		return "", fmt.Errorf("BXAuth field not found in cookie")
	}

	return cookie, nil
}

// getAuthFilePath returns the auth file path for the given provider and email
func getAuthFilePath(cfg *config.Config, provider, email string) string {
	// Clean email to make it filename-safe
	cleanEmail := strings.ReplaceAll(email, "*", "x")

	// Remove any unsafe characters, but allow standard email chars (@, ., -)
	var result strings.Builder
	for _, r := range cleanEmail {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '@' || r == '.' || r == '-' {
			result.WriteRune(r)
		}
	}

	return fmt.Sprintf("%s/%s-%s.json", cfg.AuthDir, provider, result.String())
}
