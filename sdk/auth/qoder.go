package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// QoderAuthenticator implements Qoder login for public and Enterprise VPC accounts.
type QoderAuthenticator struct{}

// NewQoderAuthenticator constructs a Qoder authenticator.
func NewQoderAuthenticator() *QoderAuthenticator {
	return &QoderAuthenticator{}
}

func (a *QoderAuthenticator) Provider() string {
	return "qoder"
}

func (a *QoderAuthenticator) RefreshLead() *time.Duration {
	// Qoder device tokens are long-lived, while PAT-derived Job Tokens are
	// short-lived and refreshable. Keep a nominal lead for the shared scheduler;
	// the executor chooses the correct refresh endpoint from storage.AuthMode.
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
	if authSvc.IsEnterpriseVPC() && qoderEnterprisePersonalToken() != "" {
		return a.loginEnterpriseVPC(ctx, authSvc, opts)
	}
	// Without a PAT, keep the VPC browser device flow. InitiateDeviceFlow
	// already derives the enterprise login host and CN client id from config.

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

	// Resolve user info (best effort). The organization id is required by the
	// current enterprise COSY protocol, so retain it whenever the account API
	// returns it.
	tokenStorage := authSvc.CreateTokenStorage(tokenData, deviceFlow.MachineID)
	name, email := "", ""
	if authSvc.IsEnterpriseVPC() {
		if details, errDetails := authSvc.FetchUserInfoDetails(ctx, tokenData.AccessToken); errDetails == nil {
			if tokenStorage.UserID == "" {
				tokenStorage.UserID = details.UserID
			}
			tokenStorage.OrganizationID = details.OrganizationID
			tokenStorage.OrganizationTags = details.OrganizationTags
			name = details.Name
			email = details.Email
		}
	} else {
		name, email = authSvc.SaveUserInfo(ctx, tokenData.AccessToken, tokenData.UserID, "", "")
	}

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
		label = strings.TrimSpace(tokenStorage.UserID)
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
		"user_id": tokenStorage.UserID,
	}
	if authSvc.IsEnterpriseVPC() {
		metadata["organization_id"] = tokenStorage.OrganizationID
		metadata["organization_tags"] = tokenStorage.OrganizationTags
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

// loginEnterpriseVPC uses the current Qoder CN credential protocol. A CN PAT
// is exchanged for a Job Token before the gateway and model catalog are used;
// the old public device-token flow does not provide that exchange step.
func (a *QoderAuthenticator) loginEnterpriseVPC(ctx context.Context, authSvc *qoder.QoderAuth, opts *LoginOptions) (*coreauth.Auth, error) {
	personalToken := qoderEnterprisePersonalToken()
	if personalToken == "" {
		return nil, fmt.Errorf("qoder CN Enterprise Personal Access Token is empty")
	}

	fmt.Println("Exchanging Qoder CN Personal Access Token...")
	tokenData, err := authSvc.ExchangePersonalToken(ctx, personalToken)
	if err != nil {
		return nil, fmt.Errorf("qoder CN personal token exchange failed: %w", err)
	}
	tokenStorage := authSvc.CreateTokenStorage(tokenData, qoder.NewMachineID())
	details, err := authSvc.FetchUserInfoDetails(ctx, tokenData.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("qoder CN user info lookup failed: %w", err)
	}
	if tokenStorage.UserID == "" {
		tokenStorage.UserID = details.UserID
	}
	if tokenStorage.UserID == "" {
		return nil, fmt.Errorf("qoder CN user info returned no user id")
	}
	tokenStorage.Name = details.Name
	tokenStorage.Email = details.Email
	tokenStorage.OrganizationID = details.OrganizationID
	tokenStorage.OrganizationTags = details.OrganizationTags
	tokenStorage.AuthMode = "job-token"

	label := strings.TrimSpace(details.Email)
	if label == "" && opts != nil && opts.Metadata != nil {
		label = strings.TrimSpace(opts.Metadata["email"])
		if label == "" {
			label = strings.TrimSpace(opts.Metadata["alias"])
		}
	}
	if label == "" {
		label = tokenStorage.UserID
	}
	if label == "" {
		label = fmt.Sprintf("user-%d", time.Now().UnixMilli())
	}
	tokenStorage.Email = label

	metadata := map[string]any{
		"email":             label,
		"name":              tokenStorage.Name,
		"user_id":           tokenStorage.UserID,
		"organization_id":   tokenStorage.OrganizationID,
		"organization_tags": tokenStorage.OrganizationTags,
		"auth_mode":         tokenStorage.AuthMode,
	}
	fileName := fmt.Sprintf("qoder-%s.json", label)

	fmt.Println("Qoder CN Enterprise authentication successful")
	if tokenStorage.Name != "" {
		fmt.Printf("Logged in as %s <%s>\n", tokenStorage.Name, label)
	}
	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}

func qoderEnterprisePersonalToken() string {
	return strings.TrimSpace(os.Getenv("QODERCN_PERSONAL_ACCESS_TOKEN"))
}
