package auth

import (
	"context"
	"fmt"
	"time"

	qoderauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var qoderRefreshLead = 30 * time.Minute

// QoderAuthenticator implements Qoder's PKCE device authorization flow.
type QoderAuthenticator struct {
	region string
}

func NewQoderAuthenticator() Authenticator {
	return &QoderAuthenticator{region: qoderauth.RegionGlobal}
}

func NewQoderCNAuthenticator() Authenticator {
	return &QoderAuthenticator{region: qoderauth.RegionCN}
}

func (a QoderAuthenticator) Provider() string {
	return qoderauth.ProviderForRegion(a.region)
}

func (a QoderAuthenticator) RefreshLead() *time.Duration {
	return &qoderRefreshLead
}

func (a QoderAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	provider := a.Provider()
	client := qoderauth.NewClient(cfg, a.region)
	flow, err := client.StartDeviceFlow()
	if err != nil {
		return nil, err
	}
	fmt.Printf("Starting %s authentication...\n", provider)
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", flow.VerificationURI)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(flow.VerificationURI); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		} else {
			fmt.Println("Browser opened automatically.")
		}
	}
	fmt.Println("Waiting for authorization...")
	storage, err := client.PollForToken(ctx, flow)
	if err != nil {
		return nil, fmt.Errorf("qoder: authorization failed: %w", err)
	}
	record, err := qoderauth.NewAuthRecord(storage)
	if err != nil {
		return nil, err
	}
	fmt.Printf("\n%s authentication successful!\n", provider)
	return record, nil
}
