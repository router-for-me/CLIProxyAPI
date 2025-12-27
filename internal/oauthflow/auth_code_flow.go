package oauthflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// AuthCodeFlowOptions controls loopback-based OAuth authorization code flows.
type AuthCodeFlowOptions struct {
	DesiredPort    int
	CallbackPath   string
	Timeout        time.Duration
	SkipStateCheck bool

	// OnAuthURL is called after the callback server is started and the provider auth URL is built.
	// Callers typically open the browser and/or print instructions here.
	OnAuthURL func(authURL string, callbackPort int, redirectURI string)
}

// AuthCodeFlowResult captures the output of an authorization code flow.
type AuthCodeFlowResult struct {
	AuthURL       string
	RedirectURI   string
	CallbackPort  int
	Session       OAuthSession
	Token         *TokenResult
	CallbackError string
}

// GenerateState returns a cryptographically secure random state string.
func GenerateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GeneratePKCE returns RFC 7636 PKCE verifier/challenge values.
func GeneratePKCE() (verifier, challenge string, err error) {
	// 96 random bytes -> 128 base64url chars without padding (same as existing provider implementations).
	b := make([]byte, 96)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b)
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(hash[:])
	return verifier, challenge, nil
}

// RunAuthCodeFlow runs a loopback OAuth authorization code flow (RFC 8252).
// It starts a loopback-only callback server, builds the provider authorization URL, waits for the callback,
// validates state, then exchanges the code for tokens.
func RunAuthCodeFlow(ctx context.Context, provider ProviderOAuth, opts AuthCodeFlowOptions) (*AuthCodeFlowResult, error) {
	if provider == nil {
		return nil, fmt.Errorf("oauthflow: provider is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}

	desiredPort := opts.DesiredPort
	server := NewLoopbackServer(desiredPort, opts.CallbackPath)
	if err := server.Start(); err != nil {
		// Port in use: fall back to port 0 when a non-zero port was requested.
		if errors.Is(err, syscall.EADDRINUSE) && desiredPort != 0 {
			server = NewLoopbackServer(0, opts.CallbackPath)
			if err2 := server.Start(); err2 != nil {
				return nil, &FlowError{Kind: FlowErrorKindPortInUse, Err: err2}
			}
		} else if errors.Is(err, syscall.EADDRINUSE) {
			return nil, &FlowError{Kind: FlowErrorKindPortInUse, Err: err}
		} else {
			return nil, &FlowError{Kind: FlowErrorKindServerStartFailed, Err: err}
		}
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Stop(stopCtx)
	}()

	callbackPort := server.Port()
	callbackPath := server.CallbackPath()
	redirectURI := fmt.Sprintf("http://localhost:%d%s", callbackPort, callbackPath)

	state, err := GenerateState()
	if err != nil {
		return nil, fmt.Errorf("oauthflow: state generation failed: %w", err)
	}
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("oauthflow: pkce generation failed: %w", err)
	}
	session := OAuthSession{
		State:         state,
		RedirectURI:   redirectURI,
		CodeVerifier:  verifier,
		CodeChallenge: challenge,
	}

	authURL, session, err := provider.AuthorizeURL(session)
	if err != nil {
		return nil, &FlowError{Kind: FlowErrorKindAuthorizeURLFailed, Err: err}
	}

	if opts.OnAuthURL != nil {
		opts.OnAuthURL(authURL, callbackPort, redirectURI)
	}

	cb, err := server.WaitForCallback(ctx, opts.Timeout)
	if err != nil {
		if errors.Is(err, ErrCallbackTimeout) {
			return nil, &FlowError{Kind: FlowErrorKindCallbackTimeout, Err: err}
		}
		return nil, err
	}

	if cb.Error != "" {
		return &AuthCodeFlowResult{
			AuthURL:       authURL,
			RedirectURI:   redirectURI,
			CallbackPort:  callbackPort,
			Session:       session,
			CallbackError: cb.Error,
		}, &FlowError{Kind: FlowErrorKindProviderError, Err: fmt.Errorf("%s", cb.Error)}
	}

	if !opts.SkipStateCheck && strings.TrimSpace(session.State) != "" && cb.State != session.State {
		return nil, &FlowError{Kind: FlowErrorKindInvalidState, Err: fmt.Errorf("state mismatch")}
	}

	code := strings.TrimSpace(cb.Code)
	if code == "" {
		return nil, &FlowError{Kind: FlowErrorKindProviderError, Err: fmt.Errorf("missing authorization code")}
	}

	token, err := provider.ExchangeCode(ctx, session, code)
	if err != nil {
		return nil, &FlowError{Kind: FlowErrorKindCodeExchangeFailed, Err: err}
	}
	if token != nil && strings.TrimSpace(token.TokenType) == "" {
		token.TokenType = http.CanonicalHeaderKey("Bearer")
	}

	return &AuthCodeFlowResult{
		AuthURL:      authURL,
		RedirectURI:  redirectURI,
		CallbackPort: callbackPort,
		Session:      session,
		Token:        token,
	}, nil
}

