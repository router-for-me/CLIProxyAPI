package client

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/protocol"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/transport"
)

// CreateSession creates a new chat session. Single attempt — the conductor
// handles retries and token refresh.
func (c *Client) CreateSession(ctx context.Context, token, locale, proxyURL string) (string, error) {
	clients := c.requestClientsForProxy(proxyURL)
	headers := c.authHeaders(token, locale)
	resp, status, err := c.postJSONWithStatus(ctx, clients, protocol.DeepSeekCreateSessionURL, headers, map[string]any{})
	if err != nil {
		return "", err
	}
	code, bizCode, msg, bizMsg := extractResponseStatus(resp)
	if status == http.StatusOK && code == 0 && bizCode == 0 {
		sessionID := extractCreateSessionID(resp)
		if sessionID != "" {
			return sessionID, nil
		}
	}
	if ch := DetectCaptchaChallenge(resp); ch != nil {
		return "", &RequestFailure{Op: "create session", Kind: FailureCaptchaRequired, Message: failureMessage(msg, bizMsg, "captcha challenge required")}
	}
	if isTokenInvalid(status, code, bizCode, msg, bizMsg) {
		return "", &RequestFailure{Op: "create session", Kind: FailureManagedUnauthorized, Message: failureMessage(msg, bizMsg, "create session failed")}
	}
	return "", &RequestFailure{Op: "create session", Kind: FailureUnknown, Message: failureMessage(msg, bizMsg, "create session failed")}
}

// GetPow fetches and solves a PoW challenge for the completion target path.
func (c *Client) GetPow(ctx context.Context, token, locale, proxyURL string) (string, error) {
	return c.GetPowForTarget(ctx, token, locale, proxyURL, protocol.DeepSeekCompletionTargetPath)
}

// GetPowForTarget fetches and solves a PoW challenge for the given target path.
// Checks the prefetch cache first (keyed by accountID from context). Single
// attempt — the conductor handles retries.
func (c *Client) GetPowForTarget(ctx context.Context, token, locale, proxyURL, targetPath string) (string, error) {
	targetPath = strings.TrimSpace(targetPath)
	if targetPath == "" {
		targetPath = protocol.DeepSeekCompletionTargetPath
	}

	// Check prefetch cache (keyed by accountID stamped on the context).
	accountID := transport.AccountIDFromContext(ctx)
	if cached, ok := c.powCache.get(accountID, targetPath); ok {
		answer, err := ComputePow(ctx, cached)
		if err != nil {
			return "", err
		}
		return BuildPowHeader(cached, answer)
	}

	clients := c.requestClientsForProxy(proxyURL)
	headers := c.authHeaders(token, locale)
	resp, status, err := c.postJSONWithStatus(ctx, clients, protocol.DeepSeekCreatePowURL, headers, map[string]any{"target_path": targetPath})
	if err != nil {
		return "", err
	}
	code, bizCode, msg, bizMsg := extractResponseStatus(resp)
	if status == http.StatusOK && code == 0 && bizCode == 0 {
		data, _ := resp["data"].(map[string]any)
		bizData, _ := data["biz_data"].(map[string]any)
		challenge, _ := bizData["challenge"].(map[string]any)
		answer, err := ComputePow(ctx, challenge)
		if err != nil {
			return "", err
		}
		return BuildPowHeader(challenge, answer)
	}
	if ch := DetectCaptchaChallenge(resp); ch != nil {
		return "", &RequestFailure{Op: "get pow", Kind: FailureCaptchaRequired, Message: failureMessage(msg, bizMsg, "captcha challenge required")}
	}
	if isTokenInvalid(status, code, bizCode, msg, bizMsg) {
		return "", &RequestFailure{Op: "get pow", Kind: FailureManagedUnauthorized, Message: failureMessage(msg, bizMsg, "get pow failed")}
	}
	return "", &RequestFailure{Op: "get pow", Kind: FailureUnknown, Message: failureMessage(msg, bizMsg, "get pow failed")}
}

// PrefetchPow fetches a PoW challenge and caches it for later use by GetPow.
// The challenge is cached per (accountID, targetPath) and expires at expire_at.
// This is optional — GetPow will fetch fresh if no prefetch has occurred.
func (c *Client) PrefetchPow(ctx context.Context, token, locale, proxyURL, targetPath string) error {
	targetPath = strings.TrimSpace(targetPath)
	if targetPath == "" {
		targetPath = protocol.DeepSeekCompletionTargetPath
	}
	clients := c.requestClientsForProxy(proxyURL)
	headers := c.authHeaders(token, locale)
	resp, status, err := c.postJSONWithStatus(ctx, clients, protocol.DeepSeekCreatePowURL, headers, map[string]any{"target_path": targetPath})
	if err != nil {
		return err
	}
	code, bizCode, msg, bizMsg := extractResponseStatus(resp)
	if status != http.StatusOK || code != 0 || bizCode != 0 {
		return errors.New(failureMessage(msg, bizMsg, "prefetch pow failed"))
	}
	data, _ := resp["data"].(map[string]any)
	bizData, _ := data["biz_data"].(map[string]any)
	challenge, _ := bizData["challenge"].(map[string]any)
	accountID := transport.AccountIDFromContext(ctx)
	c.powCache.set(accountID, targetPath, challenge)
	return nil
}
