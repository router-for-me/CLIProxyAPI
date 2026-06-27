package helps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/home"
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
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

type homeRefreshAuthEnvelope struct {
	Auth      cliproxyauth.Auth `json:"auth"`
	AuthIndex string            `json:"auth_index"`
}

type homeErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type homeRefreshClient interface {
	HeartbeatOK() bool
	GetRefreshAuth(ctx context.Context, authIndex string) ([]byte, error)
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

	updated, returnedIndex, errParse := parseHomeRefreshAuth(raw)
	if errParse != nil {
		return nil, true, homeStatusErr{code: http.StatusBadGateway, msg: "home returned invalid auth payload"}
	}
	if returnedIndex != "" {
		authIndex = returnedIndex
	}
	updated.Index = authIndex
	updated.EnsureIndex()
	return updated, true, nil
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
	case "authentication_error", "unauthorized":
		return http.StatusUnauthorized
	case "model_not_found":
		return http.StatusNotFound
	default:
		return http.StatusBadGateway
	}
}
