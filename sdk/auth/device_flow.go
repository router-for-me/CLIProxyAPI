package auth

import (
	"context"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// DeviceFlow is a device authorization that has been started and not yet exchanged.
// StartDeviceFlow returns it without waiting for the user to approve.
type DeviceFlow struct {
	Provider                string
	UserCode                string
	DeviceCode              string
	DeviceAuthID            string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               time.Duration
	Interval                time.Duration
	TokenEndpoint           string
}

// DeviceFlowPoll is one device-code exchange attempt.
// Pending is set when the user has not approved yet. Auth is set when the
// exchange finished and produced a credential.
type DeviceFlowPoll struct {
	Pending  bool
	Interval time.Duration
	Auth     *coreauth.Auth
}

// pollUntilDeviceAuthorized calls poll until it returns an auth or an error.
// The first attempt runs immediately. Later attempts wait the poll interval
// and stop once deadline has passed.
func pollUntilDeviceAuthorized(ctx context.Context, interval time.Duration, deadline time.Time, timeoutErr error, poll func() (*DeviceFlowPoll, error)) (*coreauth.Auth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	first := true
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !first && !deadline.IsZero() && time.Now().After(deadline) {
			if timeoutErr != nil {
				return nil, timeoutErr
			}
			return nil, context.DeadlineExceeded
		}
		first = false

		result, err := poll()
		if err != nil {
			return nil, err
		}
		if result != nil && result.Auth != nil {
			return result.Auth, nil
		}
		next := interval
		if result != nil && result.Interval > 0 {
			next = result.Interval
		}
		interval = next

		timer := time.NewTimer(next)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
