package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// ResolveSIWCBindPath resolves a target Codex credential file path (either an absolute path
// or a bare filename looked up under cfg.AuthDir) and validates that it is an existing Codex JSON file.
func ResolveSIWCBindPath(cfg *config.Config, target string) (string, error) {
	raw := strings.TrimSpace(target)
	if raw == "" {
		return "", fmt.Errorf("siwc-bind: missing codex credential file path")
	}

	if cfg == nil || strings.TrimSpace(cfg.AuthDir) == "" {
		return "", fmt.Errorf("siwc-bind: auth-dir is not configured")
	}
	authDir, errResolve := util.ResolveAuthDir(cfg.AuthDir)
	if errResolve != nil {
		return "", fmt.Errorf("siwc-bind: failed to resolve auth-dir: %w", errResolve)
	}
	authDir, errAbs := filepath.Abs(authDir)
	if errAbs != nil {
		return "", fmt.Errorf("siwc-bind: failed to resolve auth-dir path: %w", errAbs)
	}
	authDir, errRealDir := filepath.EvalSymlinks(authDir)
	if errRealDir != nil {
		return "", fmt.Errorf("siwc-bind: failed to resolve real auth-dir: %w", errRealDir)
	}

	candidate := raw
	if !filepath.IsAbs(raw) {
		if strings.ContainsAny(raw, `/\`) || raw == "." || raw == ".." {
			return "", fmt.Errorf("siwc-bind: expected an absolute path or bare filename")
		}
		candidate = filepath.Join(authDir, raw)
	}
	candidate, errRealFile := filepath.EvalSymlinks(candidate)
	if errRealFile != nil {
		return "", fmt.Errorf("siwc-bind: failed to resolve credential file: %w", errRealFile)
	}
	relative, errRel := filepath.Rel(authDir, candidate)
	if errRel != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("siwc-bind: credential file must belong to auth-dir")
	}
	info, errStat := os.Stat(candidate)
	if errStat != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("siwc-bind: credential target must be an existing regular file")
	}
	data, errRead := os.ReadFile(candidate)
	if errRead != nil {
		return "", fmt.Errorf("siwc-bind: failed to read credential file %s: %w", candidate, errRead)
	}
	var parsed map[string]any
	if errUnmarshal := json.Unmarshal(data, &parsed); errUnmarshal != nil {
		return "", fmt.Errorf("siwc-bind: invalid credential JSON in %s", candidate)
	}
	if errValidate := validateSIWCBindCredential(parsed); errValidate != nil {
		return "", errValidate
	}
	return filepath.Clean(candidate), nil
}

func validateSIWCBindCredential(metadata map[string]any) error {
	provider, _ := metadata["type"].(string)
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return fmt.Errorf("siwc-bind: credential type must be codex")
	}
	for _, key := range []string{"access_token", "refresh_token"} {
		value, _ := metadata[key].(string)
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("siwc-bind: codex credential requires a non-empty %s", key)
		}
	}
	return nil
}

func ensureSIWCDeviceIDFromFile(authFilePath string) (string, error) {
	data, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		return "", fmt.Errorf("failed to read credential file: %w", errRead)
	}
	var parsed map[string]any
	if errUnmarshal := json.Unmarshal(data, &parsed); errUnmarshal != nil {
		return "", fmt.Errorf("invalid credential JSON: %w", errUnmarshal)
	}
	if siwcMap, ok := parsed["siwc"].(map[string]any); ok && len(siwcMap) > 0 {
		if existingID, okID := siwcMap["device_id"].(string); okID && codex.IsValidUUID(existingID) {
			return strings.ToLower(strings.TrimSpace(existingID)), nil
		}
	}
	return strings.ToLower(uuid.NewString()), nil
}

func saveSIWCToCodexCredentialFile(authFilePath string, siwcData *codex.CodexSIWCTokenData) error {
	if siwcData == nil {
		return fmt.Errorf("siwc token data is nil")
	}
	misc.LogSavingCredentials(authFilePath)
	return codex.WithFileLock(authFilePath, func() error {
		existingBytes, errRead := os.ReadFile(authFilePath)
		if errRead != nil {
			return fmt.Errorf("failed to read existing credential file: %w", errRead)
		}
		var diskMap map[string]any
		if errUnmarshal := json.Unmarshal(existingBytes, &diskMap); errUnmarshal != nil {
			return fmt.Errorf("failed to parse existing credential file: %w", errUnmarshal)
		}
		if errValidate := validateSIWCBindCredential(diskMap); errValidate != nil {
			return errValidate
		}
		// Binding is an explicit SIWC replacement, not a stale snapshot merge.
		diskMap["use_siwc"] = true
		diskMap["siwc"] = siwcData.ToMap()

		return codex.WriteJSONFileAtomic(authFilePath, diskMap)
	})
}

// DoSIWCBind runs the OpenAI Sign in with ChatGPT (SIWC) OAuth flow and writes the resulting
// SIWC credential section and use_siwc=true into the specified Codex credential file.
func DoSIWCBind(cfg *config.Config, target string, options *LoginOptions) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if options == nil {
		options = &LoginOptions{}
	}
	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	authFilePath, errResolve := ResolveSIWCBindPath(cfg, target)
	if errResolve != nil {
		log.Errorf("%v", errResolve)
		return
	}

	deviceID, errDevice := ensureSIWCDeviceIDFromFile(authFilePath)
	if errDevice != nil {
		log.Errorf("siwc-bind: %v", errDevice)
		return
	}

	callbackPort := codex.SIWCCallbackPort
	if options.CallbackPort > 0 {
		callbackPort = options.CallbackPort
	}
	redirectURI := fmt.Sprintf("http://%s:%d%s", codex.SIWCCallbackHost, callbackPort, codex.SIWCCallbackPath)

	pkceCodes, errPKCE := codex.GeneratePKCECodes()
	if errPKCE != nil {
		log.Errorf("siwc-bind: PKCE generation failed: %v", errPKCE)
		return
	}
	state, errState := codex.GenerateRandomBase64URL()
	if errState != nil {
		log.Errorf("siwc-bind: state generation failed: %v", errState)
		return
	}
	nonce, errNonce := codex.GenerateRandomBase64URL()
	if errNonce != nil {
		log.Errorf("siwc-bind: nonce generation failed: %v", errNonce)
		return
	}

	oauthServer := codex.NewOAuthServer(callbackPort)
	if errStart := oauthServer.Start(); errStart != nil {
		if strings.Contains(errStart.Error(), "already in use") {
			authErr := codex.NewAuthenticationError(codex.ErrPortInUse, errStart)
			log.Error(codex.GetUserFriendlyMessage(authErr))
			os.Exit(codex.ErrPortInUse.Code)
		}
		log.Errorf("siwc-bind: failed to start OAuth callback server: %v", errStart)
		return
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if errStop := oauthServer.Stop(stopCtx); errStop != nil {
			log.Warnf("siwc-bind: oauth server stop error: %v", errStop)
		}
	}()

	authSvc := codex.NewCodexAuth(cfg)
	authURL, errURL := authSvc.GenerateSIWCAuthURL(state, nonce, deviceID, redirectURI, pkceCodes)
	if errURL != nil {
		log.Errorf("siwc-bind: failed to generate authorization URL: %v", errURL)
		return
	}

	if !options.NoBrowser {
		fmt.Println("Opening browser for OpenAI Sign in with ChatGPT authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(callbackPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Sign in with ChatGPT authentication callback...")

	callbackCh := make(chan *codex.OAuthResult, 1)
	callbackErrCh := make(chan error, 1)
	manualDescription := ""

	go func() {
		result, errWait := oauthServer.WaitForCallback(5 * time.Minute)
		if errWait != nil {
			callbackErrCh <- errWait
			return
		}
		callbackCh <- result
	}()

	var result *codex.OAuthResult
	var manualPromptTimer *time.Timer
	var manualPromptC <-chan time.Time
	if promptFn != nil {
		manualPromptTimer = time.NewTimer(15 * time.Second)
		manualPromptC = manualPromptTimer.C
		defer manualPromptTimer.Stop()
	}

	var manualInputCh <-chan string
	var manualInputErrCh <-chan error

waitForCallback:
	for {
		select {
		case result = <-callbackCh:
			break waitForCallback
		case errWait := <-callbackErrCh:
			fmt.Printf("Sign in with ChatGPT authentication failed: %v\n", errWait)
			return
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case result = <-callbackCh:
				break waitForCallback
			case errWait := <-callbackErrCh:
				fmt.Printf("Sign in with ChatGPT authentication failed: %v\n", errWait)
				return
			default:
			}
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(promptFn, "Paste the callback URL (or press Enter to keep waiting): ")
			continue
		case input := <-manualInputCh:
			manualInputCh = nil
			manualInputErrCh = nil
			parsed, errParse := misc.ParseOAuthCallback(input)
			if errParse != nil {
				fmt.Printf("Sign in with ChatGPT authentication failed: %v\n", errParse)
				return
			}
			if parsed == nil {
				continue
			}
			manualDescription = parsed.ErrorDescription
			result = &codex.OAuthResult{
				Code:     parsed.Code,
				State:    parsed.State,
				ClientID: parsed.ClientID,
				Error:    parsed.Error,
			}
			break waitForCallback
		case errManual := <-manualInputErrCh:
			fmt.Printf("Sign in with ChatGPT authentication failed: %v\n", errManual)
			return
		}
	}

	if result.Error != "" {
		errOAuth := codex.NewOAuthError(result.Error, manualDescription, http.StatusBadRequest)
		fmt.Printf("Sign in with ChatGPT authentication failed: %v\n", errOAuth)
		return
	}
	if result.State != state {
		fmt.Println("Sign in with ChatGPT authentication failed: state mismatch")
		return
	}
	if strings.TrimSpace(result.ClientID) == "" {
		fmt.Println("Sign in with ChatGPT authentication failed: callback did not contain an issued client ID")
		return
	}

	siwcData, errExchange := authSvc.ExchangeSIWCCodeForTokens(context.Background(), result.Code, result.ClientID, redirectURI, deviceID, pkceCodes)
	if errExchange != nil {
		fmt.Printf("Sign in with ChatGPT token exchange failed: %v\n", errExchange)
		return
	}

	if errSave := saveSIWCToCodexCredentialFile(authFilePath, siwcData); errSave != nil {
		fmt.Printf("Failed to save SIWC credentials: %v\n", errSave)
		return
	}

	fmt.Printf("SIWC credentials bound and saved to %s\n", authFilePath)
	fmt.Println("Sign in with ChatGPT authentication successful!")
}
