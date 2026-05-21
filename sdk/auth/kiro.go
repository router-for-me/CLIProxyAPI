package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var kiroRefreshLead = 20 * time.Minute

// KiroAuthenticator imports a successfully acquired Kiro/AWS SSO token.
type KiroAuthenticator struct{}

func NewKiroAuthenticator() Authenticator {
	return &KiroAuthenticator{}
}

func (KiroAuthenticator) Provider() string {
	return kiroauth.Provider
}

func (KiroAuthenticator) RefreshLead() *time.Duration {
	return &kiroRefreshLead
}

func (a KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	tokenFile := ""
	if opts.Metadata != nil {
		tokenFile = strings.TrimSpace(opts.Metadata["token-file"])
	}
	if tokenFile == "" {
		tokenFile = strings.TrimSpace(os.Getenv("KIRO_SSO_TOKEN_FILE"))
	}
	if tokenFile == "" && opts.Prompt != nil {
		input, err := opts.Prompt("Path to Kiro SSO token JSON file: ")
		if err != nil {
			return nil, err
		}
		tokenFile = strings.TrimSpace(input)
	}
	if tokenFile == "" {
		return nil, fmt.Errorf("kiro auth: token file is required; pass --kiro-sso-token <path> or set KIRO_SSO_TOKEN_FILE")
	}

	td, err := kiroauth.LoadTokenFile(tokenFile)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(td.AccessToken) == "" {
		refreshed, errRefresh := kiroauth.NewKiroAuth(cfg).Refresh(ctx, td)
		if errRefresh != nil {
			return nil, errRefresh
		}
		td = refreshed
	}
	if strings.TrimSpace(td.ProfileARN) == "" {
		return nil, fmt.Errorf("kiro auth: profile_arn is required for runtime API calls")
	}

	now := time.Now().UTC()
	fileName := kiroauth.CredentialFileName(td)
	label := firstKiroLabel(td.Email, td.ProfileARN)
	metadata := kiroauth.MetadataFromTokenData(td)
	metadata["last_import"] = now.Format(time.RFC3339)

	return &coreauth.Auth{
		ID:        fileName,
		Provider:  a.Provider(),
		FileName:  fileName,
		Label:     label,
		Status:    coreauth.StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Storage: &kiroauth.TokenStorage{
			Data:     *td,
			Metadata: metadata,
		},
		Metadata: metadata,
		Attributes: map[string]string{
			"source":      tokenFile,
			"profile_arn": td.ProfileARN,
			"region":      td.APIRegion,
			"auth_kind":   "oauth",
		},
		NextRefreshAfter: nextKiroRefreshAfter(td.ExpiresAt, now),
	}, nil
}

func firstKiroLabel(email, profileARN string) string {
	if strings.TrimSpace(email) != "" {
		return strings.TrimSpace(email)
	}
	if strings.TrimSpace(profileARN) != "" {
		return strings.TrimSpace(profileARN)
	}
	return "Kiro"
}

func nextKiroRefreshAfter(expiry string, fallback time.Time) time.Time {
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
		if ts, err := time.Parse(layout, strings.TrimSpace(expiry)); err == nil {
			return ts.Add(-kiroRefreshLead)
		}
	}
	return fallback.Add(40 * time.Minute)
}
