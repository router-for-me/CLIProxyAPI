package kiro

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// DesktopAuthURL is the Kiro desktop auth service used for social login and refresh.
const DesktopAuthURL = "https://prod.us-east-1.auth.desktop.kiro.dev"

// SocialCallbackPorts are the loopback ports registered for the official IDE
// redirect URI; the upstream rejects any other port with redirect_mismatch.
var SocialCallbackPorts = []int{3128, 4649, 6588, 8008, 9091, 49153, 50153, 51153, 52153, 53153}

// SocialLoginTimeout mirrors the official IDE authentication flow timeout.
const SocialLoginTimeout = 10 * time.Minute

const socialCallbackHTML = `<html><head><meta charset="utf-8"><title>Kiro</title></head><body><h1>%s</h1><p>You can close this window.</p></body></html>`

// SocialProvider maps a login method to the Kiro identity provider name.
func SocialProvider(method string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "google":
		return "Google", true
	case "github":
		return "Github", true
	}
	return "", false
}

type SocialCallback struct {
	Code  string
	State string
	Error string
}

// SocialLogin is one pending Google/GitHub authorization with its local callback server.
type SocialLogin struct {
	Provider    string
	State       string
	Verifier    string
	RedirectURI string
	AuthURL     string
	MachineID   string
	server      *http.Server
}

func (c *Client) desktopAuthURL() string {
	if c.AuthURL != "" {
		return strings.TrimRight(c.AuthURL, "/")
	}
	return DesktopAuthURL
}

// StartSocialLogin binds the first free registered callback port and builds the
// browser authorization URL. onCallback receives every callback request.
func (c *Client) StartSocialLogin(provider, state string, onCallback func(SocialCallback)) (*SocialLogin, error) {
	loginOption := strings.ToLower(provider)
	if _, ok := SocialProvider(loginOption); !ok {
		return nil, fmt.Errorf("kiro: unsupported social provider %q", provider)
	}
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	var listener net.Listener
	var errListen error
	for _, port := range SocialCallbackPorts {
		if listener, errListen = net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port)); errListen == nil {
			break
		}
	}
	if listener == nil {
		return nil, fmt.Errorf("kiro: no registered callback port is available: %w", errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	login := &SocialLogin{
		Provider:    provider,
		State:       state,
		Verifier:    verifier,
		RedirectURI: fmt.Sprintf("http://localhost:%d/oauth/callback?login_option=%s", port, loginOption),
		MachineID:   uuid.NewString(),
	}
	query := url.Values{
		"idp": {provider}, "redirect_uri": {login.RedirectURI}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "state": {state},
	}
	login.AuthURL = c.desktopAuthURL() + "/login?" + query.Encode()

	mux := http.NewServeMux()
	handler := func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		callback := SocialCallback{Code: strings.TrimSpace(q.Get("code")), State: strings.TrimSpace(q.Get("state")),
			Error: strings.TrimSpace(firstNonEmpty(q.Get("error_description"), q.Get("error")))}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if callback.Code == "" && callback.Error == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, socialCallbackHTML, "Missing authorization code")
			return
		}
		onCallback(callback)
		title := "Authentication successful"
		if callback.Error != "" {
			title = "Authentication failed"
		}
		_, _ = fmt.Fprintf(w, socialCallbackHTML, title)
	}
	mux.HandleFunc("/oauth/callback", handler)
	mux.HandleFunc("/signin/callback", handler)
	login.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if errServe := login.server.Serve(listener); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.WithError(errServe).Warn("kiro: social callback server stopped")
		}
	}()
	return login, nil
}

// Close stops the local callback server.
func (l *SocialLogin) Close() {
	if l == nil || l.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := l.server.Shutdown(ctx); err != nil {
		log.WithError(err).Debug("kiro: social callback server shutdown failed")
	}
}

// ExchangeSocialCode trades the authorization code for Kiro tokens.
func (c *Client) ExchangeSocialCode(ctx context.Context, login *SocialLogin, code string) (*Token, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("kiro: authorization code is missing")
	}
	headers := map[string]string{"User-Agent": DesktopAuthUserAgent(map[string]any{"machine_id": login.MachineID})}
	var token Token
	err := c.jsonRequest(ctx, http.MethodPost, c.desktopAuthURL()+"/oauth/token", "", headers, map[string]any{
		"code": code, "code_verifier": login.Verifier, "redirect_uri": login.RedirectURI,
	}, &token)
	if err != nil {
		return nil, err
	}
	if token.AccessToken == "" || token.RefreshToken == "" {
		return nil, fmt.Errorf("kiro: token exchange returned incomplete tokens")
	}
	return &token, nil
}

// SocialTokenMetadata builds a credential in the same shape as social imports.
func SocialTokenMetadata(token *Token, login *SocialLogin) map[string]any {
	metadata := map[string]any{"type": "kiro", "provider": login.Provider, "auth_method": "social",
		"region": DefaultRegion, "machine_id": login.MachineID}
	if email := idTokenEmail(token.IDToken); email != "" {
		metadata["email"] = email
	}
	ApplyToken(metadata, token)
	return metadata
}

// idTokenEmail reads the display email from an unverified ID token payload.
func idTokenEmail(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return strings.TrimSpace(claims.Email)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
