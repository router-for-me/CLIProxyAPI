package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// QoderAuthenticator implements the device flow login for Qoder accounts.
type QoderAuthenticator struct{}

// NewQoderAuthenticator constructs a Qoder authenticator.
func NewQoderAuthenticator() *QoderAuthenticator {
	return &QoderAuthenticator{}
}

func (a *QoderAuthenticator) Provider() string {
	return "qoder"
}

func (a *QoderAuthenticator) RefreshLead() *time.Duration {
	// Refresh 10 minutes before expiry (matching Python implementation)
	d := 10 * time.Minute
	return &d
}

func (a *QoderAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := qoder.NewQoderAuth(cfg)

	// Initiate device flow
	deviceFlow, err := authSvc.InitiateDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("qoder device flow initiation failed: %w", err)
	}

	authURL := deviceFlow.VerificationURIComplete

	// Open browser or display URL
	if !opts.NoBrowser {
		fmt.Println("Opening browser for Qoder authentication")
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

	fmt.Println("Waiting for Qoder authentication...")

	// Poll for token
	tokenData, err := authSvc.PollForToken(ctx, deviceFlow)
	if err != nil {
		return nil, fmt.Errorf("qoder authentication failed: %w", err)
	}

	// Fetch user info (best effort)
	name, email, err := authSvc.FetchUserInfo(ctx, tokenData.AccessToken)
	if err != nil {
		log.Warnf("Failed to fetch user info: %v", err)
	}

	// Create token storage
	tokenStorage := authSvc.CreateTokenStorage(tokenData, deviceFlow.MachineID)
	if tokenData.UserID != "" {
		_, _ = authSvc.SaveUserInfo(ctx, tokenData.AccessToken, tokenData.UserID, name, email)
	}

	// Get email from options if not fetched
	if email == "" && opts.Metadata != nil {
		email = opts.Metadata["email"]
		if email == "" {
			email = opts.Metadata["alias"]
		}
	}

	if email == "" && opts.Prompt != nil {
		email, err = opts.Prompt("Please input your email address or alias for Qoder:")
		if err != nil {
			return nil, err
		}
	}

	email = strings.TrimSpace(email)
	if email == "" {
		return nil, &EmailRequiredError{Prompt: "Please provide an email address or alias for Qoder."}
	}

	tokenStorage.Email = email
	tokenStorage.Name = name

	// Generate file name
	fileName := fmt.Sprintf("qoder-%s.json", tokenStorage.Email)
	metadata := map[string]any{
		"email":   tokenStorage.Email,
		"name":    tokenStorage.Name,
		"user_id": tokenData.UserID,
	}

	fmt.Println("Qoder authentication successful")
	if name != "" {
		fmt.Printf("Logged in as %s <%s>\n", name, email)
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
