package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/copilot"
)

// DoCopilotQuotaLogin initiates a GitHub Device Code OAuth flow to obtain a
// GitHub access token for viewing Copilot premium request quota in the management UI.
//
// Note: This login is for viewing your GitHub Copilot quota only.
// It is separate from the Codex/Copilot API login.
func DoCopilotQuotaLogin(cfg *config.Config) {
	fmt.Println("Starting GitHub Copilot quota authentication...")
	fmt.Println("Note: This login is for viewing your GitHub Copilot quota only.")
	fmt.Println("      It is separate from the Codex/Copilot API login.")
	fmt.Println()

	ctx := context.Background()
	svc := copilot.NewService(cfg)

	deviceResp, err := svc.StartDeviceFlow(ctx)
	if err != nil {
		fmt.Printf("Failed to start device flow: %v\n", err)
		return
	}

	fmt.Printf("Visit: %s\n", deviceResp.VerificationURI)
	fmt.Printf("Enter code: %s\n", deviceResp.UserCode)
	fmt.Println()
	fmt.Println("Waiting for authorization...")

	token, err := svc.CompleteDeviceFlow(ctx, deviceResp.DeviceCode, 0)
	if err != nil {
		fmt.Printf("Authentication failed: %v\n", err)
		return
	}

	fmt.Printf("Successfully authenticated as %s\n", token.Email)
	fmt.Println("Token saved for Copilot quota tracking.")
}
