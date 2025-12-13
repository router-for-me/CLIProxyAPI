package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

	// Check for duplicate BXAuth before authentication
	bxAuth := iflow.ExtractBXAuth(cookie)
	if existingFile, err := iflow.CheckDuplicateBXAuth(cfg.AuthDir, bxAuth); err != nil {
		fmt.Printf("Failed to check duplicate: %v\n", err)
		return
	} else if existingFile != "" {
		fmt.Printf("Duplicate BXAuth found, authentication already exists: %s\n", filepath.Base(existingFile))
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

	cookie, err := iflow.NormalizeCookie(line)
	if err != nil {
		return "", err
	}

	return cookie, nil
}

// getAuthFilePath returns the auth file path for the given provider and email
func getAuthFilePath(cfg *config.Config, provider, email string) string {
	fileName := iflow.SanitizeIFlowFileName(email)
	return fmt.Sprintf("%s/%s-%s-%d.json", cfg.AuthDir, provider, fileName, time.Now().Unix())
}
