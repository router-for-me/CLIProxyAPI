package cliproxy

import (
	"context"
	"strings"

	clineauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cline"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// maybeDetectClineModels lazily detects the per-account model catalog for a
// cline credential that has no stored models yet (typical case: a manually
// imported auth file). It runs detached from the auth-update pipeline: the
// fetch is serialized per credential by clineauth.DetectAvailableModelsOnce,
// and a successful detection is persisted to the auth file; the watcher WRITE
// event then re-registers the auth, so /v1/models picks up the detected
// catalog. Detection failure is intentionally not persisted, so the next
// auth lifecycle event may retry.
//
// The metadata "models" key is the durable "detected" marker: once present
// (even with an empty list for a model-less account), no further fetch occurs.
func (s *Service) maybeDetectClineModels(auth *coreauth.Auth) {
	if s == nil || s.coreManager == nil || auth == nil {
		return
	}
	if s.cfg != nil && s.cfg.Home.Enabled {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "cline") {
		return
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return
	}
	if _, detected := auth.Metadata[clineauth.ModelsMetadataKey]; detected {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}
	go func() {
		ctx := context.Background()
		err := clineauth.DetectAvailableModelsOnce(authID, func() error {
			current, ok := s.coreManager.GetByID(authID)
			if !ok || current == nil {
				return nil
			}
			if _, detected := current.Metadata[clineauth.ModelsMetadataKey]; detected {
				return nil
			}
			token, baseURL := clineCredentialDetails(current)
			svc := clineauth.NewClineAuthWithProxyURLAndBaseURL(s.cfg, current.ProxyURL, baseURL)
			models, errFetch := svc.FetchAvailableModels(ctx, token)
			if errFetch != nil {
				log.Warnf("cline: per-account model detection failed for %s: %v", authID, errFetch)
				return nil // Not persisted: the next auth lifecycle event may retry.
			}
			latest, okLatest := s.coreManager.GetByID(authID)
			if !okLatest || latest == nil {
				return nil
			}
			updated := latest.Clone()
			if updated.Metadata == nil {
				updated.Metadata = make(map[string]any)
			}
			updated.Metadata[clineauth.ModelsMetadataKey] = models
			if _, errSave := sdkAuth.GetTokenStore().Save(ctx, updated); errSave != nil {
				return errSave
			}
			label := strings.TrimSpace(current.Label)
			if label == "" {
				label = "cline"
			}
			log.Infof("cline: detected %d model(s) for account %s", len(models), label)
			return nil
		})
		if err != nil {
			log.Warnf("cline: persisting detected models for %s failed: %v", authID, err)
		}
	}()
}

// clineCredentialDetails extracts the access token and API base URL from a
// cline auth record (attributes take precedence, then metadata).
func clineCredentialDetails(auth *coreauth.Auth) (token, baseURL string) {
	if auth == nil {
		return "", clineauth.DefaultAPIBaseURL
	}
	if auth.Attributes != nil {
		token = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if baseURL == "" {
		baseURL = metadataValue(auth, "base_url")
	}
	if token == "" {
		token = metadataValue(auth, "access_token")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = clineauth.DefaultAPIBaseURL
	}
	return token, strings.TrimSpace(baseURL)
}

func metadataValue(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
