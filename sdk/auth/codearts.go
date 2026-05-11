package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codearts"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var codeartsRefreshLead = 4 * time.Hour

type CodeArtsAuthenticator struct{}

func NewCodeArtsAuthenticator() Authenticator { return &CodeArtsAuthenticator{} }

func (CodeArtsAuthenticator) Provider() string { return "codearts" }

func (CodeArtsAuthenticator) RefreshLead() *time.Duration {
	return &codeartsRefreshLead
}

type codeartsCallbackResult struct {
	Identifier string
	Redirect   string
	Error      string
}

func (a CodeArtsAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("codearts: failed to find free port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	cbChan := make(chan codeartsCallbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		identifier := r.URL.Query().Get("identifier")
		redirect := r.URL.Query().Get("redirect")
		cbChan <- codeartsCallbackResult{
			Identifier: identifier,
			Redirect:   redirect,
		}
		if redirect != "" {
			http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>CodeArts Login</title></head>` +
			`<body style="display:flex;justify-content:center;align-items:center;height:100vh;` +
			`font-family:system-ui;background:#1a1a2e;color:#e0e0e0">` +
			`<div style="text-align:center">` +
			`<h1 style="color:#4CAF50">&#10003; Login Successful</h1>` +
			`<p>You can close this window and return to the terminal.</p>` +
			`</div></body></html>`))
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if errServe := srv.Serve(listener); errServe != nil && !strings.Contains(errServe.Error(), "Server closed") {
			log.Warnf("codearts callback server error: %v", errServe)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	ticketID := generateCodeArtsTicketID()
	codeartsAuth := codearts.NewCodeArtsAuth(nil)
	authURL := codeartsAuth.AuthorizationURL(ticketID, port)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for CodeArts authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(port)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
			util.PrintSSHTunnelInstructions(port)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(port)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for CodeArts authentication callback...")

	var cbRes codeartsCallbackResult
	timeoutTimer := time.NewTimer(5 * time.Minute)
	defer timeoutTimer.Stop()

	select {
	case cbRes = <-cbChan:
	case <-timeoutTimer.C:
		return nil, fmt.Errorf("codearts: authentication timed out")
	}

	if cbRes.Error != "" {
		return nil, fmt.Errorf("codearts: authentication failed: %s", cbRes.Error)
	}
	if cbRes.Identifier == "" {
		return nil, fmt.Errorf("codearts: missing identifier in callback")
	}

	fmt.Println("Callback received, polling for login result...")

	pollCtx, pollCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer pollCancel()

	authResult, err := codeartsAuth.PollForLoginResult(pollCtx, ticketID, cbRes.Identifier)
	if err != nil {
		return nil, fmt.Errorf("codearts: %w", err)
	}

	tokenData, err := codeartsAuth.ProcessLoginResult(ctx, authResult)
	if err != nil {
		return nil, fmt.Errorf("codearts: %w", err)
	}

	label := tokenData.UserName
	if label == "" {
		label = "codearts"
	}

	fmt.Println("CodeArts authentication successful")

	return &coreauth.Auth{
		ID:       fmt.Sprintf("codearts-%s.json", tokenData.UserName),
		Provider: "codearts",
		FileName: fmt.Sprintf("codearts-%s.json", tokenData.UserName),
		Label:    label,
		Metadata: map[string]any{
			"type":           "codearts",
			"ak":             tokenData.AK,
			"sk":             tokenData.SK,
			"security_token": tokenData.SecurityToken,
			"x_auth_token":   tokenData.XAuthToken,
			"expires_at":     tokenData.ExpiresAt.Format(time.RFC3339),
			"user_id":        tokenData.UserID,
			"user_name":      tokenData.UserName,
			"domain_id":      tokenData.DomainID,
			"email":          tokenData.Email,
		},
	}, nil
}

func generateCodeArtsTicketID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
