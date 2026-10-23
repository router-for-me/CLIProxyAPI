package management

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

const codexDeviceStartTimeout = 30 * time.Second

// RequestCodexDeviceToken starts the existing SDK device login for the v8 API.
func (h *Handler) RequestCodexDeviceToken(c *gin.Context) {
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}
	RegisterDeviceOAuthSession(state, "codex")

	ctx, cancel := context.WithCancel(PopulateAuthContext(context.Background(), c))
	watchCtx, stopWatch := context.WithCancel(ctx)
	requestCtx := c.Request.Context()
	stopRequestCancel := context.AfterFunc(requestCtx, cancel)
	responseReady := make(chan struct{})
	deviceCodeCh := make(chan sdkAuth.DeviceCodeInfo, 1)
	loginDone := make(chan struct{})
	watchDone := make(chan struct{})
	handedOff := false
	defer func() {
		stopRequestCancel()
		if !handedOff {
			oauthSessions.Discard(state)
			cancel()
			<-loginDone
			<-watchDone
		}
	}()
	go func() {
		defer close(watchDone)
		watchOAuthSessionCancel(watchCtx, cancel, state, "codex")
	}()
	go func() {
		defer close(loginDone)
		defer func() {
			stopWatch()
			<-watchDone
			cancel()
		}()

		authenticator := h.codexDeviceAuthenticator
		if authenticator == nil {
			authenticator = sdkAuth.NewCodexAuthenticator()
		}
		h.mu.Lock()
		cfg := h.cfg.CloneForRuntime()
		h.mu.Unlock()
		record, errLogin := authenticator.Login(ctx, cfg, &sdkAuth.LoginOptions{
			NoBrowser: true,
			Metadata:  map[string]string{"codex_login_mode": "device"},
			OnDeviceCode: func(info sdkAuth.DeviceCodeInfo) {
				select {
				case deviceCodeCh <- info:
				case <-ctx.Done():
				}
			},
		})
		if !IsOAuthSessionPending(state, "codex") {
			return
		}
		if errLogin != nil || record == nil {
			log.Error("Codex device authentication failed")
			SetOAuthSessionError(state, "Authentication failed")
			return
		}
		// A fast login must not persist credentials before the caller receives its session.
		select {
		case <-responseReady:
		case <-ctx.Done():
			return
		}
		if !oauthSessions.BeginSave(state, "codex") {
			return
		}
		if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
			log.Error("Failed to save Codex device authentication")
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}
		CompleteOAuthSession(state)
	}()

	startTimer := time.NewTimer(codexDeviceStartTimeout)
	defer startTimer.Stop()
	info, started, timedOut := waitForCodexDeviceStart(deviceCodeCh, loginDone, startTimer.C, requestCtx.Done())
	if requestCtx.Err() != nil {
		return
	}
	if started {
		userCode := strings.TrimSpace(info.UserCode)
		verificationURL := strings.TrimSpace(info.VerificationURL)
		if userCode == "" || verificationURL == "" || info.ExpiresIn <= 0 {
			CancelOAuthSession(state)
			cancel()
			c.JSON(http.StatusBadGateway, gin.H{"error": "invalid device authorization response"})
			return
		}
		// Once writing starts, delivery is decided by the write and flush results.
		if !stopRequestCancel() || requestCtx.Err() != nil {
			return
		}
		responseErrors := len(c.Errors)
		c.JSON(http.StatusOK, gin.H{
			"status":     "ok",
			"url":        verificationURL,
			"state":      state,
			"flow":       "device",
			"user_code":  userCode,
			"expires_in": info.ExpiresIn,
		})
		if len(c.Errors) > responseErrors {
			return
		}
		if errFlush := httpwire.FlushResponse(c.Writer); errFlush != nil {
			return
		}
		handedOff = true
		close(responseReady)
		return
	}
	if timedOut {
		CancelOAuthSession(state)
		cancel()
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "device authorization flow timed out"})
		return
	}

	oauthSessions.Discard(state)
	c.JSON(http.StatusBadGateway, gin.H{"error": "failed to start device authorization flow"})
}

func waitForCodexDeviceStart(deviceCodeCh <-chan sdkAuth.DeviceCodeInfo, loginDone <-chan struct{}, timeout <-chan time.Time, requestDone <-chan struct{}) (sdkAuth.DeviceCodeInfo, bool, bool) {
	if info, ok := takeReadyCodexDeviceCode(deviceCodeCh); ok {
		return info, true, false
	}
	return waitForCodexDeviceStartAfterDrain(deviceCodeCh, loginDone, timeout, requestDone)
}

func waitForCodexDeviceStartAfterDrain(deviceCodeCh <-chan sdkAuth.DeviceCodeInfo, loginDone <-chan struct{}, timeout <-chan time.Time, requestDone <-chan struct{}) (sdkAuth.DeviceCodeInfo, bool, bool) {
	select {
	case <-requestDone:
		return sdkAuth.DeviceCodeInfo{}, false, false
	case info := <-deviceCodeCh:
		return info, true, false
	case <-loginDone:
		if info, ok := takeReadyCodexDeviceCode(deviceCodeCh); ok {
			return info, true, false
		}
		return sdkAuth.DeviceCodeInfo{}, false, false
	case <-timeout:
		if info, ok := takeReadyCodexDeviceCode(deviceCodeCh); ok {
			return info, true, false
		}
		return sdkAuth.DeviceCodeInfo{}, false, true
	}
}

func takeReadyCodexDeviceCode(deviceCodeCh <-chan sdkAuth.DeviceCodeInfo) (sdkAuth.DeviceCodeInfo, bool) {
	select {
	case info := <-deviceCodeCh:
		return info, true
	default:
		return sdkAuth.DeviceCodeInfo{}, false
	}
}
