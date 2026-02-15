package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qwen"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	// legacy client removed
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// QwenAuthenticator implements the device flow login for Qwen accounts.
type QwenAuthenticator struct{}

// NewQwenAuthenticator constructs a Qwen authenticator.
func NewQwenAuthenticator() *QwenAuthenticator {
	return &QwenAuthenticator{}
}

func (a *QwenAuthenticator) Provider() string {
	return "qwen"
}

func (a *QwenAuthenticator) RefreshLead() *time.Duration {
	return new(3 * time.Hour)
}

func (a *QwenAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := qwen.NewQwenAuth(cfg)

	deviceFlow, err := authSvc.InitiateDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("qwen device flow initiation failed: %w", err)
	}

	authURL := deviceFlow.VerificationURIComplete

	if !opts.NoBrowser {
		fmt.Println("Opening browser for Qwen authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Qwen authentication...")

	tokenData, err := authSvc.PollForToken(deviceFlow.DeviceCode, deviceFlow.CodeVerifier)
	if err != nil {
		return nil, fmt.Errorf("qwen authentication failed: %w", err)
	}

	tokenStorage := authSvc.CreateTokenStorage(tokenData)

	email := ""
	if opts.Metadata != nil {
		email = opts.Metadata["email"]
		if email == "" {
			email = opts.Metadata["alias"]
		}
	}

	if email == "" && opts.Prompt != nil {
		email, err = opts.Prompt("Please input your email address or alias for Qwen:")
		if err != nil {
			return nil, err
		}
	}

	email = strings.TrimSpace(email)
	if email == "" {
		return nil, &EmailRequiredError{Prompt: "Please provide an email address or alias for Qwen."}
	}

	tokenStorage.Email = email

	// no legacy client construction

	fileName := fmt.Sprintf("qwen-%s.json", tokenStorage.Email)
	metadata := map[string]any{
		"email": tokenStorage.Email,
	}

	fmt.Println("Qwen authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
