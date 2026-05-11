package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/joycode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type JoyCodeAuthenticator struct{}

func NewJoyCodeAuthenticator() Authenticator { return &JoyCodeAuthenticator{} }

func (JoyCodeAuthenticator) Provider() string { return "joycode" }

func (JoyCodeAuthenticator) RefreshLead() *time.Duration { return nil }

type joycodeCallbackResult struct {
	PTKey string
	Error string
}

func (a JoyCodeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
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
		return nil, fmt.Errorf("joycode: failed to find free port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	authKey := generateJoyCodeAuthKey()
	cbChan := make(chan joycodeCallbackResult, 1)

	mux := http.NewServeMux()
	callbackHandler := func(w http.ResponseWriter, r *http.Request) {
		receivedAuthKey := r.URL.Query().Get("authKey")
		if receivedAuthKey != "" && receivedAuthKey != authKey {
			cbChan <- joycodeCallbackResult{Error: "authKey mismatch"}
			w.WriteHeader(http.StatusForbidden)
			return
		}

		ptKey := r.URL.Query().Get("pt_key")
		if ptKey == "" {
			ptKey = r.URL.Query().Get("ptKey")
		}

		if ptKey != "" {
			cbChan <- joycodeCallbackResult{PTKey: ptKey}
		} else {
			cbChan <- joycodeCallbackResult{Error: "missing pt_key"}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>JoyCode Login</title></head>` +
			`<body style="display:flex;justify-content:center;align-items:center;height:100vh;` +
			`font-family:system-ui;background:#1a1a2e;color:#e0e0e0">` +
			`<div style="text-align:center">` +
			`<h1 style="color:#2ecc71">&#10003; Authorization Successful</h1>` +
			`<p>Credential captured, syncing. Please return to the command line.</p>` +
			`</div></body></html>`))
	}
	mux.HandleFunc("/", callbackHandler)
	mux.HandleFunc("/joycode/callback", callbackHandler)

	srv := &http.Server{Handler: mux}
	go func() {
		if errServe := srv.Serve(listener); errServe != nil && !strings.Contains(errServe.Error(), "Server closed") {
			log.Warnf("joycode callback server error: %v", errServe)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	authURL := fmt.Sprintf("https://joycode.jd.com/login/?ideAppName=JoyCode&fromIde=ide&redirect=0&authPort=%d&authKey=%s", port, authKey)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for JoyCode authentication")
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

	fmt.Println("Waiting for JoyCode authentication callback...")

	var cbRes joycodeCallbackResult
	timeoutTimer := time.NewTimer(5 * time.Minute)
	defer timeoutTimer.Stop()

	select {
	case cbRes = <-cbChan:
	case <-timeoutTimer.C:
		return nil, fmt.Errorf("joycode: authentication timed out")
	}

	if cbRes.Error != "" {
		return nil, fmt.Errorf("joycode: authentication failed: %s", cbRes.Error)
	}
	if cbRes.PTKey == "" {
		return nil, fmt.Errorf("joycode: missing pt_key in callback")
	}

	fmt.Println("Callback received, verifying token...")

	verifyCtx, verifyCancel := context.WithTimeout(ctx, 30*time.Second)
	defer verifyCancel()

	jcAuth := joycode.NewJoyCodeAuth(nil)
	tokenData, err := jcAuth.VerifyToken(verifyCtx, cbRes.PTKey)
	if err != nil {
		fmt.Printf("Token verification failed: %v\n", err)
		fmt.Println("Saving raw token for manual use")
		tokenData = &joycode.JoyCodeTokenData{
			PTKey:     cbRes.PTKey,
			LoginType: "IDE",
		}
	}

	label := tokenData.UserID
	if label == "" {
		label = "joycode"
	}

	fmt.Println("JoyCode authentication successful")

	return &coreauth.Auth{
		ID:       fmt.Sprintf("joycode-%s.json", tokenData.UserID),
		Provider: "joycode",
		FileName: fmt.Sprintf("joycode-%s.json", tokenData.UserID),
		Label:    label,
		Metadata: map[string]any{
			"type":        "joycode",
			"ptKey":       tokenData.PTKey,
			"userId":      tokenData.UserID,
			"tenant":      tokenData.Tenant,
			"orgFullName": tokenData.OrgFullName,
			"loginType":   tokenData.LoginType,
		},
	}, nil
}

func generateJoyCodeAuthKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
