package helps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type homeStatusErr struct {
	code       int
	msg        string
	diagnostic string
	errorType  string
	upstream   bool
}

func (e homeStatusErr) Error() string {
	if e.upstream {
		return e.msg
	}
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}

func (e homeStatusErr) StatusCode() int { return e.code }

func (e homeStatusErr) LogDiagnostic() string {
	if strings.TrimSpace(e.diagnostic) != "" {
		return logging.SafeDiagnosticForLog(e.diagnostic)
	}
	if e.upstream {
		return fmt.Sprintf("Home refresh upstream response: status=%d", e.code)
	}
	if errorType := strings.ToLower(strings.TrimSpace(e.errorType)); errorType != "" {
		return logging.SafeDiagnosticForLog("Home refresh failed: type=" + errorType)
	}
	return logging.SafeDiagnosticForLog(e.Error())
}

func (e homeStatusErr) DirectResponse() bool { return e.upstream }

func (e homeStatusErr) ResponseBody() []byte {
	if !e.upstream {
		return nil
	}
	return []byte(e.msg)
}

type homeErrorEnvelope struct {
	Error *homeErrorDetail `json:"error"`
}

type homeRefreshAuthEnvelope struct {
	Auth      cliproxyauth.Auth `json:"auth"`
	AuthIndex string            `json:"auth_index"`
}

type homeErrorDetail struct {
	Type       string                `json:"type"`
	Message    string                `json:"message"`
	Code       string                `json:"code,omitempty"`
	Diagnostic string                `json:"diagnostic,omitempty"`
	Upstream   *homeUpstreamResponse `json:"upstream,omitempty"`
}

type homeUpstreamResponse struct {
	Status int    `json:"status"`
	Body   []byte `json:"body"`
}

type homeRefreshClient interface {
	HeartbeatOK() bool
	GetRefreshAuth(ctx context.Context, authIndex string, accessTokenSHA256 string) ([]byte, error)
}

var currentHomeRefreshClient = func() homeRefreshClient {
	return home.Current()
}

// RefreshAuthViaHome replaces local refresh logic when home control plane integration is enabled.
// It returns (updatedAuth, true, nil) when home refresh succeeds; (nil, true, err) when home is
// enabled but refresh fails; and (nil, false, nil) when home is disabled.
func RefreshAuthViaHome(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, bool, error) {
	if cfg == nil || !cfg.Home.Enabled {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if auth == nil {
		return nil, true, homeStatusErr{code: http.StatusInternalServerError, msg: "home refresh: auth is nil"}
	}

	client := currentHomeRefreshClient()
	if client == nil || !client.HeartbeatOK() {
		return nil, true, homeStatusErr{code: http.StatusServiceUnavailable, msg: "home control center unavailable"}
	}

	authIndex := strings.TrimSpace(auth.Index)
	if authIndex == "" {
		authIndex = strings.TrimSpace(auth.EnsureIndex())
	}
	if authIndex == "" {
		return nil, true, homeStatusErr{code: http.StatusBadGateway, msg: "home refresh: auth_index is empty"}
	}

	raw, err := client.GetRefreshAuth(ctx, authIndex, authAccessTokenSHA256(auth))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, true, err
		}
		return nil, true, homeStatusErr{
			code:       http.StatusServiceUnavailable,
			msg:        "home refresh temporarily unavailable",
			diagnostic: "Home refresh transport failed: " + logging.SafeErrorDiagnostic(err),
		}
	}

	var env homeErrorEnvelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal == nil && env.Error != nil {
		code := strings.TrimSpace(env.Error.Type)
		if code == "" {
			code = strings.TrimSpace(env.Error.Code)
		}
		if env.Error.Upstream != nil {
			return nil, true, homeStatusErr{
				code:       env.Error.Upstream.Status,
				msg:        string(env.Error.Upstream.Body),
				diagnostic: env.Error.Diagnostic,
				errorType:  code,
				upstream:   true,
			}
		}
		statusCode := statusFromHomeErrorCode(code)
		message := "credential refresh temporarily unavailable"
		switch statusCode {
		case http.StatusUnauthorized:
			message = "credential unauthorized"
		case http.StatusNotFound:
			message = "credential refresh target not found"
		}
		return nil, true, homeStatusErr{code: statusCode, msg: message, diagnostic: env.Error.Diagnostic, errorType: code}
	}

	updated, returnedIndex, errParse := parseHomeRefreshAuth(raw)
	if errParse != nil {
		return nil, true, homeStatusErr{
			code:       http.StatusBadGateway,
			msg:        "home returned invalid auth payload",
			diagnostic: "Home refresh response decode failed: " + logging.SafeErrorDiagnostic(errParse),
		}
	}
	if updated.Disabled || updated.Status == cliproxyauth.StatusDisabled {
		return nil, true, homeStatusErr{code: http.StatusUnauthorized, msg: "credential unauthorized"}
	}
	if returnedIndex != "" {
		authIndex = returnedIndex
	}
	updated.Index = authIndex
	updated.EnsureIndex()
	return updated, true, nil
}

func authAccessTokenSHA256(auth *cliproxyauth.Auth) string {
	return cliproxyauth.AccessTokenSHA256(auth)
}

func parseHomeRefreshAuth(raw []byte) (*cliproxyauth.Auth, string, error) {
	var rawObject map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &rawObject); errUnmarshal != nil {
		return nil, "", errUnmarshal
	}
	if _, ok := rawObject["auth"]; ok {
		var envelope homeRefreshAuthEnvelope
		if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
			return nil, "", errUnmarshal
		}
		return &envelope.Auth, strings.TrimSpace(envelope.AuthIndex), nil
	}
	var updated cliproxyauth.Auth
	if errUnmarshal := json.Unmarshal(raw, &updated); errUnmarshal != nil {
		return nil, "", errUnmarshal
	}
	return &updated, "", nil
}

func statusFromHomeErrorCode(code string) int {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "authentication_error", "unauthorized", "invalid_grant", "refresh_token_expired", "refresh_token_revoked", "refresh_token_reused":
		return http.StatusUnauthorized
	case "model_not_found":
		return http.StatusNotFound
	case "auth_not_found", "auth_unavailable", "refresh_temporarily_unavailable", "refresh_unsupported", "home_unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusServiceUnavailable
	}
}
