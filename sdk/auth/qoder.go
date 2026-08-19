package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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
	// Qoder device tokens are long-lived (~30 days), and we don't have
	// a working refresh path (see QoderExecutor.Refresh comment). Use a
	// short non-zero lead so the auto-refresh loop still revisits the
	// auth periodically — but never within the same minute it just ran.
	// Returning nil disables scheduled refresh entirely; we keep a
	// nominal 24h lead so admins can observe through the management API.
	d := 24 * time.Hour
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

	// Resolve user info (best effort). FetchUserInfo only needs the access
	// token, so we always attempt it — UserID is informational here.
	tokenStorage := authSvc.CreateTokenStorage(tokenData, deviceFlow.MachineID)
	name, email := authSvc.SaveUserInfo(ctx, tokenData.AccessToken, tokenData.UserID, "", "")

	// Resolve a label for the auth file name. Preference order:
	//   1. email returned by /userinfo
	//   2. opts.Metadata[email|alias] supplied by the caller
	//   3. tokenData.UserID — stable per account, deterministic file name
	//   4. timestamp — last-resort unique fallback so non-interactive
	//      flows (Docker, management API, scripts) never block on a prompt
	//
	// We never prompt: prompting would deadlock callers that have no TTY,
	// and we already have enough information to write a unique file.
	label := strings.TrimSpace(email)
	if label == "" && opts.Metadata != nil {
		label = strings.TrimSpace(opts.Metadata["email"])
		if label == "" {
			label = strings.TrimSpace(opts.Metadata["alias"])
		}
	}
	if label == "" {
		label = strings.TrimSpace(tokenData.UserID)
	}
	if label == "" {
		label = fmt.Sprintf("user-%d", time.Now().UnixMilli())
	}

	tokenStorage.Email = label
	tokenStorage.Name = name

	// Generate file name
	fileName := fmt.Sprintf("qoder-%s.json", label)
	metadata := map[string]any{
		"email":   label,
		"name":    name,
		"user_id": tokenData.UserID,
	}

	fmt.Println("Qoder authentication successful")
	if name != "" {
		fmt.Printf("Logged in as %s <%s>\n", name, label)
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
