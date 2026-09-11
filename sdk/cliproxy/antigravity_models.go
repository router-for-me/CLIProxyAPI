package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	antigravityModelBaseURLDaily      = "https://daily-cloudcode-pa.googleapis.com"
	antigravityModelBaseURLProd       = "https://cloudcode-pa.googleapis.com"
	antigravityModelsPath             = "/v1internal:fetchAvailableModels"
	antigravityCapabilityProbeTimeout = 5 * time.Second
)

type antigravityFetchAvailableModelsResponse struct {
	WebSearchModelIDs []string `json:"webSearchModelIds"`
}

type antigravityModelCapabilityHints struct {
	WebSearchModelIDs map[string]struct{}
}

func (s *Service) fetchAntigravityModelCapabilityHintsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityModelCapabilityHints {
	if auth == nil || auth.Metadata == nil {
		return antigravityModelCapabilityHints{}
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return antigravityModelCapabilityHints{}
	}

	probeCtx := ctx
	if probeCtx == nil {
		probeCtx = context.Background()
	}
	var cancel context.CancelFunc
	probeCtx, cancel = context.WithTimeout(probeCtx, antigravityCapabilityProbeTimeout)
	defer cancel()

	client := &http.Client{
		Timeout: antigravityCapabilityProbeTimeout,
	}
	if transport, _, errProxy := proxyutil.BuildHTTPTransport(s.antigravityModelFetchProxyURL(auth)); errProxy == nil && transport != nil {
		client.Transport = transport
	}

	baseURLs := antigravityModelBaseURLs(auth)
	if len(baseURLs) == 1 {
		return s.fetchAntigravityModelHintsFromURL(probeCtx, client, baseURLs[0], accessToken)
	}

	type probeResult struct {
		hints antigravityModelCapabilityHints
	}
	ch := make(chan probeResult, len(baseURLs))
	for _, baseURL := range baseURLs {
		go func(url string) {
			h := s.fetchAntigravityModelHintsFromURL(probeCtx, client, url, accessToken)
			ch <- probeResult{hints: h}
		}(baseURL)
	}

	for i := 0; i < len(baseURLs); i++ {
		select {
		case <-probeCtx.Done():
			return antigravityModelCapabilityHints{}
		case res := <-ch:
			if len(res.hints.WebSearchModelIDs) > 0 {
				return res.hints
			}
		}
	}
	return antigravityModelCapabilityHints{}
}

func (s *Service) fetchAntigravityModelHintsFromURL(ctx context.Context, client *http.Client, baseURL string, accessToken string) antigravityModelCapabilityHints {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+antigravityModelsPath, strings.NewReader(`{}`))
	if errReq != nil {
		return antigravityModelCapabilityHints{}
	}
	req.Close = true
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", misc.AntigravityUserAgent())

	resp, errDo := client.Do(req)
	if errDo != nil {
		return antigravityModelCapabilityHints{}
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("antigravity model fetch: close response body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return antigravityModelCapabilityHints{}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return antigravityModelCapabilityHints{}
	}
	return parseAntigravityModelCapabilityHints(body)
}

func (s *Service) antigravityModelFetchProxyURL(auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s != nil {
		s.cfgMu.RLock()
		defer s.cfgMu.RUnlock()
		if s.cfg != nil {
			return strings.TrimSpace(s.cfg.ProxyURL)
		}
	}
	return ""
}

func antigravityModelBaseURLs(auth *coreauth.Auth) []string {
	if auth != nil && auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["base_urls"]); raw != "" {
			parts := strings.Split(raw, ",")
			urls := make([]string, 0, len(parts))
			for _, p := range parts {
				if trimmed := strings.TrimRight(strings.TrimSpace(p), "/"); trimmed != "" {
					urls = append(urls, trimmed)
				}
			}
			if len(urls) > 0 {
				return urls
			}
		}
	}
	if baseURL := resolveAntigravityModelBaseURL(auth); baseURL != "" {
		return []string{baseURL}
	}
	return []string{antigravityModelBaseURLDaily, antigravityModelBaseURLProd}
}

func resolveAntigravityModelBaseURL(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata["base_url"].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return strings.TrimRight(value, "/")
			}
		}
	}
	return ""
}

func parseAntigravityModelCapabilityHints(body []byte) antigravityModelCapabilityHints {
	var parsed antigravityFetchAvailableModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return antigravityModelCapabilityHints{}
	}
	webSearchModels := make(map[string]struct{}, len(parsed.WebSearchModelIDs))
	for _, modelID := range parsed.WebSearchModelIDs {
		modelID = normalizeAntigravityFetchedModelID(modelID)
		if modelID != "" {
			webSearchModels[modelID] = struct{}{}
		}
	}
	return antigravityModelCapabilityHints{WebSearchModelIDs: webSearchModels}
}

func applyAntigravityFetchedModelCapabilities(models []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	if len(models) == 0 || len(hints.WebSearchModelIDs) == 0 {
		return models
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := normalizeAntigravityFetchedModelID(model.ID)
		if _, ok := hints.WebSearchModelIDs[modelID]; ok {
			model.SupportsWebSearch = true
		}
	}
	return models
}

func normalizeAntigravityFetchedModelID(modelID string) string {
	return strings.ToLower(strings.TrimSpace(modelID))
}

// WaitAntigravityProbes waits for any in-flight asynchronous Antigravity capability probes to complete.
func (s *Service) WaitAntigravityProbes() {
	if s == nil {
		return
	}
	s.antigravityProbeWg.Wait()
}

func (s *Service) waitAntigravityProbesContext(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		s.antigravityProbeWg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
	case <-done:
	}
}

func (s *Service) asyncProbeAntigravityCapabilities(ctx context.Context, auth *coreauth.Auth, providerKey string) {
	if auth == nil || auth.ID == "" || auth.Disabled {
		return
	}
	authClone := auth.Clone()
	expectedEpoch := auth.RegistrationEpoch
	expectedPrefix := auth.Prefix
	expectedRegEpoch := GlobalModelRegistry().ClientRegistrationEpoch(auth.ID)

	probeCtx := ctx
	if probeCtx == nil {
		probeCtx = context.Background()
	}

	if s != nil {
		s.antigravityProbeWg.Add(1)
	}

	go func() {
		if s != nil {
			defer s.antigravityProbeWg.Done()
		}
		hints := s.fetchAntigravityModelCapabilityHintsForAuth(probeCtx, authClone)
		if len(hints.WebSearchModelIDs) == 0 {
			return
		}
		if s == nil {
			return
		}
		if s.coreManager != nil {
			current, exists := s.coreManager.GetByID(authClone.ID)
			if !exists || current == nil || current.Disabled {
				return
			}
			// Version protection: verify auth registration epoch and prefix did not change.
			// Auth.Generation is intentionally NOT checked here because regular request completions
			// increment Generation on every request (via MarkResult), which must not invalidate valid probe results.
			if current.RegistrationEpoch != expectedEpoch || current.Prefix != expectedPrefix {
				return
			}
		}
		aliasMap := s.buildAntigravityReverseAliasMap(authClone)

		// Atomically update capabilities on existing registered models if epoch matches
		updated := GlobalModelRegistry().ApplyClientModelCapabilities(authClone.ID, expectedRegEpoch, func(modelID string, info *ModelInfo) {
			upstreamID := resolveAntigravityUpstreamModelID(modelID, authClone.Prefix, aliasMap)
			if _, ok := hints.WebSearchModelIDs[upstreamID]; ok {
				info.SupportsWebSearch = true
			}
		})
		if !updated {
			return
		}

		if s.coreManager != nil {
			s.coreManager.ReconcileRegistryModelStates(context.Background(), authClone.ID)
			s.coreManager.RefreshSchedulerEntry(authClone.ID)
		}
	}()
}

func (s *Service) buildAntigravityReverseAliasMap(auth *coreauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	var cfg *config.Config
	if s != nil {
		s.cfgMu.RLock()
		cfg = s.cfg
		s.cfgMu.RUnlock()
	}
	channel := coreauth.OAuthModelAliasChannel(auth.Provider, auth.AuthKind())
	aliases := oauthModelAliasesForAuth(cfg, channel, auth.Attributes)
	if len(aliases) == 0 {
		return nil
	}
	aliasMap := make(map[string]string, len(aliases))
	for _, entry := range aliases {
		aliasName := strings.ToLower(strings.TrimSpace(entry.Alias))
		upstreamName := strings.ToLower(strings.TrimSpace(entry.Name))
		if aliasName != "" && upstreamName != "" {
			aliasMap[aliasName] = upstreamName
		}
	}
	return aliasMap
}

func resolveAntigravityUpstreamModelID(modelID string, prefix string, aliasMap map[string]string) string {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	prefix = strings.ToLower(strings.Trim(strings.TrimSpace(prefix), "/"))
	unprefixed := modelID
	if prefix != "" && strings.HasPrefix(modelID, prefix+"/") {
		unprefixed = modelID[len(prefix)+1:]
	}
	if len(aliasMap) > 0 {
		if upstream, ok := aliasMap[unprefixed]; ok && upstream != "" {
			return upstream
		}
	}
	return unprefixed
}
