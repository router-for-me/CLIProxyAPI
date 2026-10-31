package pluginhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const refreshPolicyStorageKey = coreauth.PluginRefreshPolicyStorageKey

func restoreParsedRefreshPolicy(data *pluginapi.AuthData, raw []byte) error {
	if data.RefreshPolicy != nil {
		if !data.RefreshPolicy.Valid() {
			return pluginapi.ErrInvalidRefreshPolicy
		}
		return nil
	}
	var stored map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &stored) != nil {
		return nil
	}
	if value, ok := stored[refreshPolicyStorageKey]; ok {
		var policy pluginapi.RefreshPolicy
		if err := json.Unmarshal(value, &policy); err != nil {
			return fmt.Errorf("restore plugin refresh policy: %w", err)
		}
		data.RefreshPolicy = &policy
	}
	return nil
}

func (s *pluginTokenStorage) CloneTokenStorage() baseauth.TokenStorage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &pluginTokenStorage{provider: s.provider, rawJSON: bytes.Clone(s.rawJSON), meta: cloneAnyMap(s.meta)}
}

func normalizeRefreshFields(input map[string]any) (map[string]any, error) {
	out := cloneAnyMap(input)
	if out == nil {
		out = make(map[string]any)
	}
	for _, keys := range [][]string{{"access_token", "accessToken"}, {"refresh_token", "refreshToken"}} {
		var value string
		present := false
		for _, key := range keys {
			if raw, ok := input[key]; ok {
				token, valid := raw.(string)
				if !valid || strings.TrimSpace(token) == "" {
					return nil, fmt.Errorf("invalid plugin refresh %s", keys[0])
				}
				if present && token != value {
					return nil, fmt.Errorf("contradictory plugin refresh %s", keys[0])
				}
				value, present = token, true
			}
			delete(out, key)
		}
		if present {
			out[keys[0]] = value
		}
	}
	var expiry any
	var expiryAuth *coreauth.Auth
	for _, key := range []string{"expired", "expire", "expires_at", "expiresAt", "expiry", "expires"} {
		if value, ok := input[key]; ok {
			candidate := &coreauth.Auth{Metadata: map[string]any{key: value}}
			exp, valid := candidate.ExpirationTime()
			if !valid || exp.IsZero() {
				return nil, fmt.Errorf("invalid plugin refresh expiry")
			}
			if expiryAuth != nil {
				prior, _ := expiryAuth.ExpirationTime()
				if !prior.Equal(exp) {
					return nil, fmt.Errorf("contradictory plugin refresh expiry")
				}
			}
			expiry, expiryAuth = exp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), candidate
		}
		delete(out, key)
	}
	if expiryAuth != nil {
		out["expired"] = expiry
	}
	coreauth.NormalizeCredentialMetadata(out)
	if value, present := out["api_key"]; present {
		if _, valid := value.(string); !valid {
			return nil, fmt.Errorf("invalid plugin compat bearer")
		}
	}
	return out, nil
}

func (h *Host) refreshedAuth(base *coreauth.Auth, resp pluginapi.AuthRefreshResponse) (next *coreauth.Auth, err error) {
	if base.RefreshPolicy == pluginapi.RefreshOnDemand {
		defer func() {
			if err != nil {
				err = &coreauth.InvalidRefreshResultError{Err: err}
			}
		}()
	}
	data := resp.Auth
	if data.RefreshPolicy != nil && !data.RefreshPolicy.Valid() {
		return nil, fmt.Errorf("unsupported plugin refresh policy")
	}
	managed := base.RefreshPolicy == pluginapi.RefreshOnDemand
	if managed {
		if data.ID != "" && data.ID != base.ID || data.Provider != "" && normalizeProviderID(data.Provider) != base.Provider {
			return nil, fmt.Errorf("plugin refresh changed credential identity")
		}
		var raw map[string]any
		if len(bytes.TrimSpace(data.StorageJSON)) > 0 {
			if err := json.Unmarshal(data.StorageJSON, &raw); err != nil || raw == nil {
				return nil, fmt.Errorf("invalid plugin refresh storage")
			}
		}
		if value, present := raw["id"]; present && value != base.ID {
			return nil, fmt.Errorf("plugin refresh storage changed credential identity")
		}
		if value, present := raw["type"]; present && value != base.Provider {
			return nil, fmt.Errorf("plugin refresh storage changed credential provider")
		}
		rawFields, err := normalizeRefreshFields(raw)
		if err != nil {
			return nil, err
		}
		metaFields, err := normalizeRefreshFields(data.Metadata)
		if err != nil {
			return nil, err
		}
		for _, key := range []string{"access_token", "refresh_token", "expired", "api_key"} {
			if a, ok := rawFields[key]; ok {
				if b, supplied := metaFields[key]; supplied && a != b {
					return nil, fmt.Errorf("plugin refresh storage and metadata disagree on %s", key)
				}
			}
		}
		fields := make(map[string]any, len(rawFields)+len(metaFields))
		for key, value := range rawFields {
			fields[key] = value
		}
		for key, value := range metaFields {
			fields[key] = value
		}
		prior, err := normalizeRefreshFields(base.Metadata)
		if err != nil {
			return nil, err
		}
		if token, supplied := fields["access_token"]; supplied && token != prior["access_token"] {
			if _, hasExpiry := fields["expired"]; !hasExpiry {
				jwt := &coreauth.Auth{Metadata: map[string]any{"access_token": token}}
				exp, valid := jwt.AccessTokenExpirationTime()
				if !valid || exp.IsZero() {
					return nil, fmt.Errorf("plugin refresh changed bearer without renewed expiry")
				}
				fields["expired"] = exp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			}
		}
		for key, value := range prior {
			if _, supplied := fields[key]; !supplied {
				fields[key] = value
			}
		}
		data.Metadata = fields
		var stored map[string]any
		_ = json.Unmarshal(storageJSONFromAuth(base), &stored)
		stored, err = normalizeRefreshFields(stored)
		if err != nil {
			return nil, err
		}
		for key, value := range fields {
			stored[key] = value
		}
		bearer, _ := fields["access_token"].(string)
		if explicit, ok := resp.Auth.Attributes["api_key"]; ok && explicit != bearer {
			return nil, fmt.Errorf("plugin refresh returned contradictory compat bearer")
		}
		if explicit, ok := metaFields["api_key"]; ok && explicit != bearer {
			return nil, fmt.Errorf("plugin refresh returned contradictory metadata bearer")
		}
		if explicit, ok := rawFields["api_key"]; ok && explicit != bearer {
			return nil, fmt.Errorf("plugin refresh returned contradictory storage bearer")
		}
		data.Attributes = cloneStringMap(data.Attributes)
		if data.Attributes == nil {
			data.Attributes = make(map[string]string)
		}
		if _, compat := base.Attributes["api_key"]; compat {
			data.Attributes["api_key"] = bearer
		}
		if _, compat := stored["api_key"]; compat {
			data.Metadata["api_key"] = bearer
			stored["api_key"] = bearer
		}
		data.StorageJSON, err = json.Marshal(stored)
		if err != nil {
			return nil, fmt.Errorf("encode plugin refresh storage: %w", err)
		}
	} else {
		if len(data.Metadata) == 0 {
			data.Metadata = cloneAnyMap(base.Metadata)
		}
	}
	metadata := data.Metadata
	data = authDataWithDefaults(data, base)
	if !managed {
		data.Metadata = metadata
		data.NextRefreshAfter = base.NextRefreshAfter
	}
	preserveFileAuthPriority(&data, base)
	if !resp.NextRefreshAfter.IsZero() {
		data.NextRefreshAfter = resp.NextRefreshAfter
	}
	next = h.authDataToCoreAuth(data, base.Attributes[coreauth.AttributePath], data.FileName, managed)
	if next == nil {
		return nil, fmt.Errorf("plugin refresh returned invalid auth data")
	}
	next.Index, next.CreatedAt, next.UpdatedAt = base.Index, base.CreatedAt, base.UpdatedAt
	return next, nil
}
