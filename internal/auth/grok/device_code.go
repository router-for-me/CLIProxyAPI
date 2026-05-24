package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceCodeResponse is the JSON body returned by xAI's device authorization endpoint.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in,omitempty"`
	Interval                int    `json:"interval,omitempty"`
}

// ErrDeviceAuthDenied / ErrDeviceCodeExpired are terminal errors from the
// device authorization poll loop. Callers should not retry.
var (
	ErrDeviceAuthDenied  = errors.New("xAI device authorization was denied")
	ErrDeviceCodeExpired = errors.New("xAI device code expired - please re-run login")
	ErrDeviceCodeTimeout = errors.New("xAI device authorization timed out")
)

// RequestDeviceCode initiates the RFC 8628 device authorization flow against
// xAI. The caller displays the returned VerificationURI + UserCode to the
// human, then calls PollDeviceCodeToken to wait for approval.
func (g *GrokAuth) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	return requestDeviceCodeAt(ctx, g, DeviceAuthURL)
}

// requestDeviceCodeAt is the internal implementation that accepts an explicit
// endpoint URL, enabling tests to point at a local httptest server.
func requestDeviceCodeAt(ctx context.Context, g *GrokAuth, endpoint string) (*DeviceCodeResponse, error) {
	body := url.Values{
		"client_id": {ClientID},
		"scope":     {Scope},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device code request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed with status %d: %s", resp.StatusCode, string(raw))
	}

	var dc DeviceCodeResponse
	if err := json.Unmarshal(raw, &dc); err != nil {
		return nil, fmt.Errorf("parse device code response: %w", err)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" || dc.VerificationURI == "" {
		return nil, fmt.Errorf("device code response missing required fields")
	}
	return &dc, nil
}

// DeviceCodePollOptions controls the polling loop and is intended for tests
// to inject sleep/now without real waits.
type DeviceCodePollOptions struct {
	Sleep func(context.Context, time.Duration) error
	Now   func() time.Time
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// positiveSeconds normalizes a server-supplied seconds value, defending
// against NaN / negative / missing inputs that would otherwise busy-loop
// setTimeout-style. Returns the supplied default when the input is not a
// finite positive number.
func positiveSeconds(value int, defaultVal time.Duration) time.Duration {
	if value <= 0 {
		return defaultVal
	}
	return time.Duration(value) * time.Second
}

// PollDeviceCodeToken polls the token endpoint until the user approves the
// device authorization or a terminal error/timeout occurs.
//
// Implements the RFC 8628 §3.5 state machine: authorization_pending (keep
// polling at the same interval), slow_down (bump interval by +5s and keep
// polling), access_denied / authorization_denied (terminal), expired_token
// (terminal). Anything else is treated as terminal.
func (g *GrokAuth) PollDeviceCodeToken(ctx context.Context, device *DeviceCodeResponse, opts DeviceCodePollOptions) (*TokenResponse, error) {
	return pollDeviceCodeTokenAt(ctx, g, device, opts, TokenURL)
}

// pollDeviceCodeTokenAt is the internal implementation that accepts an explicit
// token endpoint URL, enabling tests to point at a local httptest server.
func pollDeviceCodeTokenAt(ctx context.Context, g *GrokAuth, device *DeviceCodeResponse, opts DeviceCodePollOptions, tokenEndpoint string) (*TokenResponse, error) {
	sleep := opts.Sleep
	if sleep == nil {
		sleep = defaultSleep
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	expiresIn := positiveSeconds(device.ExpiresIn, DeviceCodeDefaultExpires)
	deadline := now().Add(expiresIn)
	interval := positiveSeconds(device.Interval, DeviceCodeDefaultInterval)
	if interval < DeviceCodeMinInterval {
		interval = DeviceCodeMinInterval
	}

	for now().Before(deadline) {
		body := url.Values{
			"grant_type":  {DeviceCodeGrant},
			"client_id":   {ClientID},
			"device_code": {device.DeviceCode},
		}.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build device token request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")

		resp, err := g.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("device token request failed: %w", err)
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read device token response: %w", readErr)
		}

		if resp.StatusCode == http.StatusOK {
			var tok TokenResponse
			if err := json.Unmarshal(raw, &tok); err != nil {
				return nil, fmt.Errorf("parse device token response: %w", err)
			}
			return &tok, nil
		}

		var errBody struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description,omitempty"`
		}
		_ = json.Unmarshal(raw, &errBody)

		remaining := deadline.Sub(now())
		if remaining <= 0 {
			break
		}
		waitFor := interval + DeviceCodePollSafetyMargin
		if waitFor > remaining {
			waitFor = remaining
		}

		switch errBody.Error {
		case "authorization_pending":
			if err := sleep(ctx, waitFor); err != nil {
				return nil, err
			}
			continue
		case "slow_down":
			interval += DeviceCodeSlowDownIncrement
			waitFor = interval + DeviceCodePollSafetyMargin
			if waitFor > remaining {
				waitFor = remaining
			}
			if err := sleep(ctx, waitFor); err != nil {
				return nil, err
			}
			continue
		case "access_denied", "authorization_denied":
			return nil, ErrDeviceAuthDenied
		case "expired_token":
			return nil, ErrDeviceCodeExpired
		default:
			detail := errBody.ErrorDescription
			if detail == "" {
				detail = errBody.Error
			}
			return nil, fmt.Errorf("device token exchange failed (status %d): %s", resp.StatusCode, detail)
		}
	}

	return nil, ErrDeviceCodeTimeout
}
