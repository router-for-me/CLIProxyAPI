package oauthflow

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

var (
	// ErrAuthorizationPending indicates the user has not completed authorization yet.
	ErrAuthorizationPending = errors.New("oauthflow: authorization_pending")
	// ErrSlowDown indicates the authorization server asked the client to poll less frequently.
	ErrSlowDown = errors.New("oauthflow: slow_down")
	// ErrDeviceCodeExpired indicates the device code expired before the user completed authorization.
	ErrDeviceCodeExpired = errors.New("oauthflow: device_code_expired")
	// ErrAccessDenied indicates the user denied the authorization request.
	ErrAccessDenied = errors.New("oauthflow: access_denied")
	// ErrPollingTimeout indicates polling exceeded the device code lifetime or an internal timeout.
	ErrPollingTimeout = errors.New("oauthflow: polling_timeout")
	// ErrTransient indicates a retryable/transient polling failure.
	ErrTransient = errors.New("oauthflow: transient")
)

const (
	defaultDevicePollInterval = 5 * time.Second
	minDevicePollInterval     = 1 * time.Second
	maxDevicePollInterval     = 10 * time.Second
	maxDevicePollDuration     = 15 * time.Minute
)

// PollDeviceToken runs an RFC 8628-style polling loop.
//
// pollOnce must return:
//   - (*TokenResult, nil) on success
//   - ErrAuthorizationPending / ErrSlowDown to keep polling
//   - ErrDeviceCodeExpired / ErrAccessDenied to abort
//   - ErrTransient or a net.Error to keep polling (best-effort)
func PollDeviceToken(ctx context.Context, device *DeviceCodeResult, pollOnce func(context.Context) (*TokenResult, error)) (*TokenResult, error) {
	if device == nil {
		return nil, fmt.Errorf("oauthflow: device code is nil")
	}
	if pollOnce == nil {
		return nil, fmt.Errorf("oauthflow: pollOnce is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = defaultDevicePollInterval
	}
	if interval < minDevicePollInterval {
		interval = minDevicePollInterval
	}

	deadline := time.Now().Add(maxDevicePollDuration)
	if device.ExpiresIn > 0 {
		expiresAt := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
		if expiresAt.Before(deadline) {
			deadline = expiresAt
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, ErrPollingTimeout
		}

		token, err := pollOnce(ctx)
		if err == nil {
			return token, nil
		}

		switch {
		case errors.Is(err, ErrAuthorizationPending):
			// keep interval unchanged
		case errors.Is(err, ErrSlowDown):
			interval += 5 * time.Second
			if interval > maxDevicePollInterval {
				interval = maxDevicePollInterval
			}
		case errors.Is(err, ErrDeviceCodeExpired), errors.Is(err, ErrAccessDenied):
			return nil, err
		default:
			// Best-effort: keep polling on transient transport failures.
			var ne net.Error
			if errors.Is(err, ErrTransient) || errors.As(err, &ne) {
				// keep polling
			} else {
				return nil, err
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

