package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

const (
	TelegramIssuer              = "https://oauth.telegram.org"
	TelegramOAuthFlowContextKey = "telegram_oauth_flow"
)

var (
	ErrTelegramOAuthNotConfigured = errors.New("Telegram OAuth is not configured or enabled. Please contact your administrator.")
	ErrTelegramOAuthConflict      = errors.New("The telegram OAuth provider name is reserved. Ask your administrator to rename the conflicting custom provider.")
	ErrTelegramOAuthFailed        = errors.New("Telegram authorization failed. Please try again.")
	ErrTelegramAccountNotBound    = errors.New("This Telegram account is not linked. Sign in using another method and link it first.")
)

// TelegramOAuthFlow keeps the PKCE secret and original client configuration on
// the server. Only AuthorizationURL is sent to the browser.
type TelegramOAuthFlow struct {
	CodeVerifier string `json:"code_verifier"`
	ClientID     string `json:"client_id"`
	RedirectURI  string `json:"redirect_uri"`
}

func TelegramConfigurationError() error {
	if HasCustomProviderConflict("telegram") {
		return ErrTelegramOAuthConflict
	}
	if !common.TelegramOAuthEnabled || !system_setting.GetTelegramSettings().IsConfigured() {
		return ErrTelegramOAuthNotConfigured
	}
	return nil
}

func NewTelegramOAuthFlow() (*TelegramOAuthFlow, error) {
	if err := TelegramConfigurationError(); err != nil {
		return nil, err
	}
	redirectURI := strings.TrimRight(system_setting.ServerAddress, "/") + "/oauth/telegram"
	parsed, err := url.Parse(redirectURI)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, ErrTelegramOAuthNotConfigured
	}
	return &TelegramOAuthFlow{
		CodeVerifier: oauth2.GenerateVerifier(),
		ClientID:     strings.TrimSpace(system_setting.GetTelegramSettings().ClientID),
		RedirectURI:  redirectURI,
	}, nil
}

func (flow *TelegramOAuthFlow) AuthorizationURL(state string) string {
	values := url.Values{
		"client_id":             {flow.ClientID},
		"redirect_uri":          {flow.RedirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid profile"},
		"state":                 {state},
		"code_challenge":        {oauth2.S256ChallengeFromVerifier(flow.CodeVerifier)},
		"code_challenge_method": {"S256"},
	}
	return TelegramIssuer + "/auth?" + values.Encode()
}

type TelegramProvider struct {
	client *http.Client
	keys   oidc.KeySet
}

func init() {
	Register("telegram", NewTelegramProvider(&http.Client{Timeout: 20 * time.Second}))
}

// NewTelegramProvider shares the HTTP client with a long-lived, cached JWKS
// verifier. Tests can exercise the real protocol using a local HTTP transport.
func NewTelegramProvider(client *http.Client) *TelegramProvider {
	return &TelegramProvider{
		client: client,
		keys: oidc.NewRemoteKeySet(
			oidc.ClientContext(context.Background(), client),
			TelegramIssuer+"/.well-known/jwks.json",
		),
	}
}

func (p *TelegramProvider) GetName() string { return "Telegram" }

func (p *TelegramProvider) IsEnabled() bool { return TelegramConfigurationError() == nil }

func (p *TelegramProvider) ExchangeToken(ctx context.Context, code string, c *gin.Context) (*OAuthToken, error) {
	if err := TelegramConfigurationError(); err != nil {
		return nil, err
	}
	value, _ := c.Get(TelegramOAuthFlowContextKey)
	flow, ok := value.(*TelegramOAuthFlow)
	settings := system_setting.GetTelegramSettings()
	if !ok || flow == nil || flow.CodeVerifier == "" || code == "" ||
		flow.ClientID != strings.TrimSpace(settings.ClientID) ||
		flow.RedirectURI != strings.TrimRight(system_setting.ServerAddress, "/")+"/oauth/telegram" {
		return nil, ErrTelegramOAuthFailed
	}
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {flow.ClientID},
		"redirect_uri":  {flow.RedirectURI},
		"code_verifier": {flow.CodeVerifier},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, TelegramIssuer+"/token", strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTelegramOAuthFailed, err)
	}
	request.SetBasicAuth(flow.ClientID, strings.TrimSpace(settings.ClientSecret))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := p.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTelegramOAuthFailed, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: token endpoint status %d", ErrTelegramOAuthFailed, response.StatusCode)
	}
	var token OAuthToken
	if err := common.DecodeJson(io.LimitReader(response.Body, 1<<20), &token); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTelegramOAuthFailed, err)
	}
	if token.IDToken == "" {
		return nil, ErrTelegramOAuthFailed
	}
	token.ClientID = flow.ClientID
	return &token, nil
}

func (p *TelegramProvider) GetUserInfo(ctx context.Context, token *OAuthToken) (*OAuthUser, error) {
	if err := TelegramConfigurationError(); err != nil {
		return nil, err
	}
	if token == nil || token.ClientID != strings.TrimSpace(system_setting.GetTelegramSettings().ClientID) {
		return nil, ErrTelegramOAuthFailed
	}
	verifier := oidc.NewVerifier(TelegramIssuer, p.keys, &oidc.Config{
		ClientID: token.ClientID, SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256},
	})
	verified, err := verifier.Verify(ctx, token.IDToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTelegramOAuthFailed, err)
	}
	var rawClaims json.RawMessage
	if err := verified.Claims(&rawClaims); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTelegramOAuthFailed, err)
	}
	var claims struct {
		ID       json.Number `json:"id"`
		Name     string      `json:"name"`
		Username string      `json:"preferred_username"`
	}
	if err := common.Unmarshal(rawClaims, &claims); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTelegramOAuthFailed, err)
	}
	id, err := strconv.ParseUint(claims.ID.String(), 10, 64)
	if err != nil || id == 0 || verified.Subject == "" {
		return nil, ErrTelegramOAuthFailed
	}
	return &OAuthUser{
		ProviderUserID: strconv.FormatUint(id, 10),
		Username:       claims.Username, DisplayName: claims.Name,
	}, nil
}

func (p *TelegramProvider) IsUserIDTaken(id string) bool { return model.IsTelegramIdAlreadyTaken(id) }

func (p *TelegramProvider) FillUserByProviderID(user *model.User, id string) error {
	stored, err := model.GetUserByTelegramID(id)
	if err != nil {
		return err
	}
	*user = *stored
	return nil
}

func (p *TelegramProvider) SetProviderUserID(user *model.User, id string) { user.TelegramId = id }

func (p *TelegramProvider) GetProviderPrefix() string { return "telegram_" }

func (p *TelegramProvider) ProviderUserIDColumn() string { return "telegram_id" }
