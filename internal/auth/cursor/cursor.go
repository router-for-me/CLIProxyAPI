package cursor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cursorwire "github.com/router-for-me/CLIProxyAPI/v7/internal/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// loginPageURL is the deep-link page the user opens to authorize the proxy.
	loginPageURL = "https://www.cursor.com/loginDeepControl"
	// loginControlURL exchanges an existing browser session cookie for a client token.
	loginControlURL = "https://www.cursor.com/api/auth/loginDeepCallbackControl"
	// authPollURL is polled until the user has approved the login.
	authPollURL = "https://api2.cursor.sh/auth/poll"

	// defaultPollInterval is how often the callback is checked.
	defaultPollInterval = 2 * time.Second
	// MaxPollDuration bounds how long the proxy waits for the user to approve.
	MaxPollDuration = 10 * time.Minute
	// verifierBytes is the PKCE verifier length used by the Cursor client.
	verifierBytes = 43
)

// LoginFlow holds the state of one Cursor deep-link login: the user opens
// LoginURL in a browser, approves, and the poll endpoint then returns the token.
type LoginFlow struct {
	// LoginURL is the page the user must open to authorize.
	LoginURL string
	// UUID identifies the flow to loginDeepCallbackControl and auth/poll.
	UUID string
	// Challenge is the PKCE challenge derived from Verifier.
	Challenge string
	// Verifier is the PKCE verifier presented when validating the callback.
	Verifier string
	// ExpiresIn is how long, in seconds, the flow stays valid.
	ExpiresIn int
}

// CursorAuth drives the Cursor login flow.
type CursorAuth struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewCursorAuth creates a new Cursor authentication service.
func NewCursorAuth(cfg *config.Config) *CursorAuth {
	return NewCursorAuthWithProxy(cfg, "")
}

// NewCursorAuthWithProxy creates a Cursor authentication service with a proxy
// override. proxyURL takes precedence over cfg.ProxyURL when non-empty.
func NewCursorAuthWithProxy(cfg *config.Config, proxyURL string) *CursorAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	client = util.SetProxy(&sdkCfg, client)
	return &CursorAuth{httpClient: client, cfg: cfg}
}

// StartLoginFlow builds the authorization URL the user has to open. No network
// call is made: the challenge/uuid pair is generated locally and validated later
// by PollOnce.
func (a *CursorAuth) StartLoginFlow(_ context.Context) (*LoginFlow, error) {
	raw := make([]byte, verifierBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("cursor: generate PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	id := uuid.NewString()
	return &LoginFlow{
		LoginURL:  fmt.Sprintf("%s?challenge=%s&uuid=%s&mode=login", loginPageURL, challenge, id),
		UUID:      id,
		Challenge: challenge,
		Verifier:  verifier,
		ExpiresIn: int(MaxPollDuration / time.Second),
	}, nil
}

// PollOnce performs a single callback validation attempt. ok is false while the
// user has not completed the login yet.
func (a *CursorAuth) PollOnce(ctx context.Context, flow *LoginFlow) (*LoginResult, bool, error) {
	if flow == nil {
		return nil, false, fmt.Errorf("cursor: login flow is nil")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s?uuid=%s&verifier=%s", authPollURL, flow.UUID, flow.Verifier), nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", cursorwire.UserAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("cursor login: close poll body error: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		// Cursor answers 404 while the callback has not been validated yet.
		return nil, false, nil
	}

	var data struct {
		AccessToken string `json:"accessToken"`
		AuthID      string `json:"authId"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, false, fmt.Errorf("cursor: decode poll response: %w", err)
	}
	if data.AccessToken == "" {
		return nil, false, nil
	}

	return newLoginResult(data.AccessToken, data.AuthID), true, nil
}

// WaitForAuthorization polls until the login callback has been validated, the
// context is cancelled, or the flow times out.
func (a *CursorAuth) WaitForAuthorization(ctx context.Context, flow *LoginFlow) (*CursorAuthBundle, error) {
	if flow == nil {
		return nil, fmt.Errorf("cursor: login flow is nil")
	}
	deadline := time.Now().Add(MaxPollDuration)
	if flow.ExpiresIn > 0 {
		if flowDeadline := time.Now().Add(time.Duration(flow.ExpiresIn) * time.Second); flowDeadline.Before(deadline) {
			deadline = flowDeadline
		}
	}

	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("cursor: context cancelled: %w", ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("cursor: timeout waiting for authorization")
			}
			result, ok, err := a.PollOnce(ctx, flow)
			if err != nil {
				return nil, err
			}
			if ok {
				return &CursorAuthBundle{Login: result}, nil
			}
		}
	}
}

// ExchangeSessionToken converts a WorkosCursorSessionToken taken from the
// cursor.com browser cookie into a client token, without the interactive page.
func (a *CursorAuth) ExchangeSessionToken(ctx context.Context, sessionToken string) (*CursorAuthBundle, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return nil, fmt.Errorf("cursor: session token is required")
	}
	flow, err := a.StartLoginFlow(ctx)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(map[string]string{"uuid": flow.UUID, "challenge": flow.Challenge})
	if err != nil {
		return nil, fmt.Errorf("cursor: encode deep control payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginControlURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", cursorwire.UserAgent)
	req.Header.Set("Cookie", "WorkosCursorSessionToken="+sessionToken)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cursor: deep control request failed: %w", err)
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("cursor login: close deep control body error: %v", errClose)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cursor: deep control returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}

	return a.WaitForAuthorization(ctx, flow)
}

// CreateTokenStorage creates a new CursorTokenStorage from an auth bundle.
func (a *CursorAuth) CreateTokenStorage(bundle *CursorAuthBundle) *CursorTokenStorage {
	storage := &CursorTokenStorage{Type: "cursor"}
	if bundle != nil && bundle.Login != nil {
		storage.AccessToken = bundle.Login.AccessToken
		storage.AuthID = bundle.Login.AuthID
		storage.Cookie = bundle.Login.Cookie
	}
	return storage
}

// newLoginResult assembles a LoginResult, deriving the Cursor cookie form.
func newLoginResult(accessToken, authID string) *LoginResult {
	result := &LoginResult{AccessToken: accessToken, AuthID: authID}
	if parts := strings.SplitN(authID, "|", 2); len(parts) > 1 && parts[1] != "" {
		result.Cookie = parts[1] + "%3A%3A" + accessToken
	} else {
		result.Cookie = accessToken
	}
	return result
}
