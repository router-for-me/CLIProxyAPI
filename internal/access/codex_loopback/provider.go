// Package codexloopback provides the opt-in loopback trust boundary used by Codex Integration.
package codexloopback

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const (
	DefaultProviderName = "codex-loopback"
	LoopbackPrincipal   = "codex-loopback-client"
)

// Register follows the enabled state of the Codex Integration loopback mode.
func Register(cfg *sdkconfig.SDKConfig) {
	if cfg == nil || !cfg.CodexIntegration.Enabled || !cfg.CodexIntegration.LoopbackAccess {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeCodexLoopback)
		return
	}
	sdkaccess.RegisterProvider(sdkaccess.AccessProviderTypeCodexLoopback, NewProvider())
}

// NewProvider returns a stateless loopback authentication provider.
func NewProvider() sdkaccess.Provider {
	return &provider{}
}

type provider struct{}

func (p *provider) Identifier() string { return DefaultProviderName }

func (p *provider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil || r == nil || !remoteAddrIsLoopback(r.RemoteAddr) {
		return nil, sdkaccess.NewNotHandledError()
	}

	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if authorization == "" {
		return nil, sdkaccess.NewNoCredentialsError()
	}
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return nil, sdkaccess.NewInvalidCredentialError()
	}

	return &sdkaccess.Result{
		Provider:  DefaultProviderName,
		Principal: LoopbackPrincipal,
		Metadata: map[string]string{
			"source": "loopback-bearer",
		},
	}, nil
}

// ValidateListenerAddr proves that the actual bound listener is loopback-only.
func ValidateListenerAddr(addr net.Addr) error {
	if addr == nil {
		return fmt.Errorf("Codex loopback access: listener address is unavailable")
	}
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		if tcpAddr.IP != nil && tcpAddr.IP.IsLoopback() {
			return nil
		}
		return fmt.Errorf("Codex loopback access: listener %q is not loopback-only", addr.String())
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr.String()))
	if err != nil {
		return fmt.Errorf("Codex loopback access: parse listener %q: %w", addr.String(), err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("Codex loopback access: listener %q is not loopback-only", addr.String())
	}
	return nil
}

func remoteAddrIsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
