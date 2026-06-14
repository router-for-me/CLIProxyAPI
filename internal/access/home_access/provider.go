package homeaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

const (
	ProviderTypeHome = "home-api-key"
	providerName     = "home-api-key"
)

type accessClient interface {
	HeartbeatOK() bool
	ValidateAccess(ctx context.Context, headers http.Header, query url.Values) ([]byte, error)
}

type provider struct {
	client func() accessClient
}

type validationResponse struct {
	Authenticated bool              `json:"authenticated"`
	Provider      string            `json:"provider"`
	Principal     string            `json:"principal"`
	Metadata      map[string]string `json:"metadata"`
	Error         *validationError  `json:"error"`
}

type validationError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// Register ensures the Home-backed provider is available to the access manager.
func Register() {
	sdkaccess.RegisterProvider(ProviderTypeHome, NewProvider(nil))
}

// Unregister removes the Home-backed provider from the access manager registry.
func Unregister() {
	sdkaccess.UnregisterProvider(ProviderTypeHome)
}

// NewProvider constructs a Home-backed access provider.
func NewProvider(client func() accessClient) sdkaccess.Provider {
	if client == nil {
		client = func() accessClient {
			return home.Current()
		}
	}
	return &provider{client: client}
}

func (p *provider) Identifier() string {
	return providerName
}

func (p *provider) Authenticate(ctx context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	candidates, _ := sdkaccess.CredentialCandidatesFromRequest(r)

	client := p.client()
	if client == nil || !client.HeartbeatOK() {
		return nil, sdkaccess.NewServiceUnavailableAuthError("Home control center unavailable", nil)
	}

	raw, errValidate := client.ValidateAccess(ctx, credentialHeadersFromRequest(r), credentialQueryFromRequest(r))
	if errValidate != nil {
		return nil, sdkaccess.NewServiceUnavailableAuthError("Home authentication unavailable", errValidate)
	}

	resp, authErr := parseValidationResponse(raw)
	if authErr != nil {
		return nil, authErr
	}
	if !resp.Authenticated {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	principal := strings.TrimSpace(resp.Principal)
	if principal == "" {
		return nil, sdkaccess.NewServiceUnavailableAuthError("Home returned invalid authentication payload", nil)
	}
	metadata := cloneMetadata(resp.Metadata)
	if strings.TrimSpace(metadata["source"]) == "" {
		if source := sourceForPrincipal(candidates, principal); source != "" {
			if metadata == nil {
				metadata = map[string]string{}
			}
			metadata["source"] = source
		}
	}

	resultProvider := strings.TrimSpace(resp.Provider)
	if resultProvider == "" {
		resultProvider = p.Identifier()
	}
	return &sdkaccess.Result{
		Provider:  resultProvider,
		Principal: principal,
		Metadata:  metadata,
	}, nil
}

func parseValidationResponse(raw []byte) (*validationResponse, *sdkaccess.AuthError) {
	if len(raw) == 0 {
		return nil, sdkaccess.NewServiceUnavailableAuthError("Home returned empty authentication payload", nil)
	}
	var resp validationResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return nil, sdkaccess.NewServiceUnavailableAuthError("Home returned invalid authentication payload", errUnmarshal)
	}
	if resp.Error == nil {
		return &resp, nil
	}

	code := strings.TrimSpace(resp.Error.Type)
	if code == "" {
		code = strings.TrimSpace(resp.Error.Code)
	}
	message := strings.TrimSpace(resp.Error.Message)
	switch strings.ToLower(code) {
	case string(sdkaccess.AuthErrorCodeNoCredentials):
		return nil, sdkaccess.NewNoCredentialsError()
	case string(sdkaccess.AuthErrorCodeInvalidCredential):
		return nil, sdkaccess.NewInvalidCredentialError()
	default:
		return nil, sdkaccess.NewServiceUnavailableAuthError(message, fmt.Errorf("home auth error: %s", code))
	}
}

func credentialHeadersFromRequest(r *http.Request) http.Header {
	headers := http.Header{}
	if r == nil {
		return headers
	}
	for _, key := range []string{"Authorization", "X-Goog-Api-Key", "X-Api-Key"} {
		if value := r.Header.Get(key); value != "" {
			headers.Set(key, value)
		}
	}
	return headers
}

func credentialQueryFromRequest(r *http.Request) url.Values {
	query := url.Values{}
	if r == nil || r.URL == nil {
		return query
	}
	source := r.URL.Query()
	for _, key := range []string{"key", "auth_token"} {
		for _, value := range source[key] {
			query.Add(key, value)
		}
	}
	return query
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sourceForPrincipal(candidates []sdkaccess.CredentialCandidate, principal string) string {
	for _, candidate := range candidates {
		if candidate.Value == principal {
			return strings.TrimSpace(candidate.Source)
		}
	}
	return ""
}
