package executor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type boundModelKey struct{}

var ErrBoundModelChanged = errors.New("delegated_model_changed")

// WithBoundModel forbids payload rules from changing an admitted model.
// It is internal server context, never a caller-supplied header or boolean.
func WithBoundModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, boundModelKey{}, model)
}

// ValidateBoundModel is read-only and runs after the final payload barrier.
func ValidateBoundModel(ctx context.Context, body []byte) error {
	if ctx == nil {
		return nil
	}
	model, _ := ctx.Value(boundModelKey{}).(string)
	if model == "" {
		return nil
	}
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Model != model {
		return ErrBoundModelChanged
	}
	return nil
}

type boundAuthKey struct{}
type boundAuth struct{ token, account string }

var ErrBoundAuthChanged = errors.New("delegated_auth_changed")

// WithBoundAuth carries the immutable authenticated account pairing internally.
// It is never serialized or accepted from request headers.
func WithBoundAuth(ctx context.Context, token, account string) context.Context {
	return context.WithValue(ctx, boundAuthKey{}, boundAuth{token: strings.TrimSpace(token), account: account})
}

// ValidateBoundCodexHTTP runs after all final header rules. It does not repair
// changed authority, and disables redirects for this exact-account boundary.
func ValidateBoundCodexHTTP(ctx context.Context, client *http.Client, req *http.Request) error {
	if model, _ := ctx.Value(boundModelKey{}).(string); model == "" {
		return nil
	}
	expected, ok := ctx.Value(boundAuthKey{}).(boundAuth)
	if !ok || expected.token == "" || expected.account == "" || req == nil || req.URL == nil || req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" || (req.URL.Path != "/backend-api/codex/responses" && req.URL.Path != "/backend-api/codex/responses/compact") || req.Header.Get("Authorization") != "Bearer "+expected.token || req.Header.Get("Chatgpt-Account-Id") != expected.account {
		return ErrBoundAuthChanged
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return nil
}
