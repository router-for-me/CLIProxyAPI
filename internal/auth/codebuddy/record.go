package codebuddy

import (
	"encoding/json"
	"time"
)

// BuildTokenStorage assembles the token storage from an upstream token
// response, account info, and the parsed /v3/config models. It is shared by
// the CLI login flow and the management API login flow.
func BuildTokenStorage(token *TokenResponse, account *AccountInfo, models []ModelInfo) *TokenStorage {
	storage := &TokenStorage{
		TokenType: "Bearer",
	}
	if token != nil {
		storage.AccessToken = token.AccessToken
		storage.RefreshToken = token.RefreshToken
		if token.TokenType != "" {
			storage.TokenType = token.TokenType
		}
		storage.Scope = token.Scope
		storage.Domain = token.Domain
		if expiresAt := token.ExpiresAt(); expiresAt > 0 {
			storage.Expired = timeAt(expiresAt)
		}
	}
	if account != nil {
		storage.UID = account.UID
		storage.Nickname = account.Nickname
	}
	if len(models) > 0 {
		routable := make([]ModelInfo, 0, len(models))
		ids := make([]string, 0, len(models))
		for _, m := range models {
			if !IsRoutableModel(m.ID) {
				continue
			}
			routable = append(routable, m)
			ids = append(ids, m.ID)
		}
		storage.EnabledModels = ids
		if raw, err := json.Marshal(routable); err == nil {
			storage.ModelsMeta = string(raw)
		}
	}
	return storage
}

// timeAt formats a Unix timestamp as an RFC3339 UTC timestamp.
func timeAt(unix int64) string {
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}
