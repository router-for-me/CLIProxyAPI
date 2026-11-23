// Package copilot exposes Copilot authentication and model discovery to SDK consumers.
package copilot

import (
	"context"
	"net/http"

	"github.com/MoeForge/moego/json"
	internalcopilot "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
)

// Provider identifies GitHub Copilot credentials.
const Provider = internalcopilot.Provider

// DeviceCode contains the data required to complete a device authorization.
type DeviceCode = internalcopilot.DeviceCode

// Token contains a short-lived Copilot credential and its API endpoint.
type Token = internalcopilot.Token

// HTTPError reports the status returned by an upstream request.
type HTTPError = internalcopilot.HTTPError

// TrustedAPI reports whether the endpoint can receive a Copilot credential.
func TrustedAPI(base string) bool {
	return internalcopilot.TrustedAPI(base)
}

// StartDeviceFlow starts GitHub device authorization.
func StartDeviceFlow(ctx context.Context, client *http.Client) (DeviceCode, error) {
	return internalcopilot.StartDeviceFlow(ctx, client)
}

// Exchange obtains a short-lived Copilot token from a GitHub credential.
func Exchange(ctx context.Context, client *http.Client, githubToken string) (Token, error) {
	return internalcopilot.Exchange(ctx, client, githubToken)
}

// FetchModels reads the model catalog for the selected Copilot account.
func FetchModels(ctx context.Context, client *http.Client, base, token string) (json.RawMessage, error) {
	return internalcopilot.FetchModels(ctx, client, base, token)
}
