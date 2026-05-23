package helps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type homeStatusErr struct {
	code int
	msg  string
}

func (e homeStatusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}

func (e homeStatusErr) StatusCode() int { return e.code }

type homeErrorEnvelope struct {
	Error *homeErrorDetail `json:"error"`
}

type homeErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
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

	client := home.Current()
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

	raw, err := client.GetRefreshAuth(ctx, authIndex)
	if err != nil {
		return nil, true, homeStatusErr{code: http.StatusBadGateway, msg: err.Error()}
	}

	var env homeErrorEnvelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal == nil && env.Error != nil {
		code := strings.TrimSpace(env.Error.Type)
		if code == "" {
			code = strings.TrimSpace(env.Error.Code)
		}
		msg := strings.TrimSpace(env.Error.Message)
		if msg == "" {
			msg = "home returned error"
		}
		return nil, true, homeStatusErr{code: statusFromHomeErrorCode(code), msg: msg}
	}

	var updated cliproxyauth.Auth
	if errUnmarshal := json.Unmarshal(raw, &updated); errUnmarshal != nil {
		return nil, true, homeStatusErr{code: http.StatusBadGateway, msg: "home returned invalid auth payload"}
	}
	updated.Index = authIndex
	updated.EnsureIndex()

	// Preserve fields from the original auth that are not returned by home control plane.
	// Attributes contains priority, source, path, and other runtime config.
	if updated.Attributes == nil && auth.Attributes != nil {
		updated.Attributes = make(map[string]string, len(auth.Attributes))
		for k, v := range auth.Attributes {
			updated.Attributes[k] = v
		}
	} else if auth.Attributes != nil {
		for k, v := range auth.Attributes {
			if _, exists := updated.Attributes[k]; !exists {
				updated.Attributes[k] = v
			}
		}
	}
	// Preserve provider, proxy, label, and disabled state if not set by home.
	if updated.Provider == "" {
		updated.Provider = auth.Provider
	}
	if updated.ProxyURL == "" {
		updated.ProxyURL = auth.ProxyURL
	}
	if updated.Label == "" {
		updated.Label = auth.Label
	}
	if !updated.Disabled && updated.Status != cliproxyauth.StatusDisabled {
		updated.Disabled = auth.Disabled
		updated.Status = auth.Status
	}
	// Preserve ID to ensure Update() matches the existing entry.
	if updated.ID == "" {
		updated.ID = auth.ID
	}

	return &updated, true, nil
}

func statusFromHomeErrorCode(code string) int {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "authentication_error", "unauthorized":
		return http.StatusUnauthorized
	case "model_not_found":
		return http.StatusNotFound
	default:
		return http.StatusBadGateway
	}
}
