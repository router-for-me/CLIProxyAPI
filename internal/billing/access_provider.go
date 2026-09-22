package billing

import (
	"context"
	"net/http"
	"strings"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

type AccessProvider struct{ service *Service }

func NewAccessProvider(service *Service) *AccessProvider { return &AccessProvider{service: service} }
func (p *AccessProvider) Identifier() string             { return ProviderName }
func (p *AccessProvider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil || !p.service.Enabled() {
		return nil, sdkaccess.NewNotHandledError()
	}
	token := extractToken(r)
	if token == "" {
		return nil, sdkaccess.NewNoCredentialsError()
	}
	principal, ok := p.service.AuthenticateToken(token)
	if !ok {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return &sdkaccess.Result{Provider: principal.Provider, Principal: principal.UserID, Metadata: map[string]string{"user_id": principal.UserID, "token_id": principal.TokenID, "provider": principal.Provider}}, nil
}

func extractToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	vals := []string{bearer(r.Header.Get("Authorization")), r.Header.Get("X-Goog-Api-Key"), r.Header.Get("X-Api-Key")}
	if r.URL != nil {
		vals = append(vals, r.URL.Query().Get("key"), r.URL.Query().Get("auth_token"))
	}
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func bearer(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		return strings.TrimSpace(parts[1])
	}
	return h
}
