package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

type KiroAuthenticator struct{}

func NewKiroAuthenticator() Authenticator             { return &KiroAuthenticator{} }
func (KiroAuthenticator) Provider() string            { return "kiro" }
func (KiroAuthenticator) RefreshLead() *time.Duration { lead := 5 * time.Minute; return &lead }

func NewKiroAuthRecord(metadata map[string]any) *coreauth.Auth {
	fileName := "kiro-" + uuid.NewString() + ".json"
	return &coreauth.Auth{ID: fileName, FileName: fileName, Provider: "kiro", Label: "Kiro", Metadata: metadata}
}

func (KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	transport, _, err := proxyutil.BuildHTTPTransport(cfg.ProxyURL)
	if err != nil {
		return nil, err
	}
	if transport != nil {
		defer transport.CloseIdleConnections()
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if transport != nil {
		httpClient.Transport = transport
	}
	client := &kiro.Client{HTTP: httpClient}
	if provider, ok := kiro.SocialProvider(opts.Metadata["method"]); ok {
		return kiroSocialLogin(ctx, client, provider, opts)
	}
	device, err := client.StartDeviceFlow(ctx, opts.Metadata["region"], opts.Metadata["start_url"])
	if err != nil {
		return nil, err
	}
	url := device.VerificationURIComplete
	if url == "" {
		url = device.VerificationURI
	}
	fmt.Printf("Open %s\nUser code: %s\n", url, device.UserCode)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(url); errOpen != nil {
			log.WithError(errOpen).Warn("kiro: browser could not be opened")
		}
	}
	token, err := client.WaitForAuthorization(ctx, device)
	if err != nil {
		return nil, err
	}
	return NewKiroAuthRecord(kiro.TokenMetadata(token, device)), nil
}

func kiroSocialLogin(ctx context.Context, client *kiro.Client, provider string, opts *LoginOptions) (*coreauth.Auth, error) {
	callbacks := make(chan kiro.SocialCallback, 1)
	state := uuid.NewString()
	login, err := client.StartSocialLogin(provider, state, func(callback kiro.SocialCallback) {
		select {
		case callbacks <- callback:
		default:
		}
	})
	if err != nil {
		return nil, err
	}
	defer login.Close()
	fmt.Printf("Open %s\n", login.AuthURL)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(login.AuthURL); errOpen != nil {
			log.WithError(errOpen).Warn("kiro: browser could not be opened")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, kiro.SocialLoginTimeout)
	defer cancel()
	var callback kiro.SocialCallback
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case callback = <-callbacks:
	}
	if callback.Error != "" {
		return nil, fmt.Errorf("kiro: authorization failed: %s", callback.Error)
	}
	if callback.State != state {
		return nil, fmt.Errorf("kiro: authorization state mismatch")
	}
	token, err := client.ExchangeSocialCode(ctx, login, callback.Code)
	if err != nil {
		return nil, err
	}
	return NewKiroAuthRecord(kiro.SocialTokenMetadata(token, login)), nil
}
