package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (h *Host) hostConfigSummaryLocked() pluginapi.HostConfigSummary {
	if h == nil || h.runtimeConfig == nil {
		return pluginapi.HostConfigSummary{}
	}
	cfg := h.runtimeConfig
	return pluginapi.HostConfigSummary{
		AuthDir:          strings.TrimSpace(cfg.AuthDir),
		ProxyURL:         strings.TrimSpace(cfg.ProxyURL),
		ForceModelPrefix: cfg.ForceModelPrefix,
		OAuthModelAlias:  pluginOAuthModelAliases(cfg.OAuthModelAlias),
		ExcludedModels:   cloneStringSliceMap(cfg.OAuthExcludedModels),
	}
}

func (h *Host) hostConfigSummary() pluginapi.HostConfigSummary {
	if h == nil {
		return pluginapi.HostConfigSummary{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hostConfigSummaryLocked()
}

func pluginOAuthModelAliases(in map[string][]config.OAuthModelAlias) map[string][]pluginapi.ModelAlias {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]pluginapi.ModelAlias, len(in))
	for provider, aliases := range in {
		key := normalizeProviderID(provider)
		if key == "" {
			continue
		}
		for _, alias := range aliases {
			name := strings.TrimSpace(alias.Name)
			value := strings.TrimSpace(alias.Alias)
			if name == "" || value == "" {
				continue
			}
			out[key] = append(out[key], pluginapi.ModelAlias{Name: name, Alias: value})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		cleanKey := normalizeProviderID(key)
		if cleanKey == "" {
			continue
		}
		out[cleanKey] = cloneStringSlice(values)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeProviderID(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func authIDForPath(path, authDir string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	id := path
	if authDir = strings.TrimSpace(authDir); authDir != "" {
		cleanDir := filepath.Clean(authDir)
		cleanPath := filepath.Clean(path)
		if rel, errRel := filepath.Rel(cleanDir, cleanPath); isRelativeWithinAuthDir(rel, errRel) {
			id = rel
		} else {
			if !filepath.IsAbs(cleanDir) {
				if absDir, errAbs := filepath.Abs(cleanDir); errAbs == nil {
					cleanDir = absDir
				}
			}
			if !filepath.IsAbs(cleanPath) {
				if absPath, errAbs := filepath.Abs(cleanPath); errAbs == nil {
					cleanPath = absPath
				}
			}
			if rel, errRel := filepath.Rel(cleanDir, cleanPath); isRelativeWithinAuthDir(rel, errRel) {
				id = rel
			}
		}
	}
	id = filepath.ToSlash(filepath.Clean(id))
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func isRelativeWithinAuthDir(rel string, errRel error) bool {
	if errRel != nil || rel == "" || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func hasAuthProviderCap(caps pluginapi.Capabilities) bool {
	if caps.AuthProvider != nil {
		return true
	}
	for _, p := range caps.AuthProviders {
		if p != nil {
			return true
		}
	}
	return false
}

func hasRecordAuthProvider(record capabilityRecord) bool {
	return hasAuthProviderCap(record.plugin.Capabilities)
}

func recordAuthProviders(record capabilityRecord) []pluginapi.AuthProvider {
	caps := record.plugin.Capabilities
	out := make([]pluginapi.AuthProvider, 0, 1+len(caps.AuthProviders))
	if caps.AuthProvider != nil {
		out = append(out, caps.AuthProvider)
	}
	for _, p := range caps.AuthProviders {
		if p != nil {
			out = append(out, p)
		}
	}
	return out
}

func normalizeAuthProviderIdentifiers(values ...string) []string {
	var out []string
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			id := normalizeProviderID(part)
			if id == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func (h *Host) AuthProviderIdentifiers() []string {
	if h == nil {
		return nil
	}
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, record := range h.activeRecords() {
		if !hasRecordAuthProvider(record) || h.isPluginFused(record.id) {
			continue
		}
		identifiers, okIdentifiers := h.callRecordAuthProviderIdentifiers(record)
		if !okIdentifiers {
			continue
		}
		for _, identifier := range identifiers {
			if identifier == "" {
				continue
			}
			if _, exists := seen[identifier]; exists {
				continue
			}
			seen[identifier] = struct{}{}
			out = append(out, identifier)
		}
	}
	return out
}

func (h *Host) HasAuthProvider(provider string) bool {
	return h.authProviderRecord(provider) != nil
}

// SameAuthProvider reports whether providerA and providerB belong to the same active plugin auth provider.
func (h *Host) SameAuthProvider(providerA, providerB string) bool {
	providerA = normalizeProviderID(providerA)
	providerB = normalizeProviderID(providerB)
	if h == nil || providerA == "" || providerB == "" {
		return false
	}
	if providerA == providerB {
		return true
	}
	for _, record := range h.activeRecords() {
		if !hasRecordAuthProvider(record) || h.isPluginFused(record.id) {
			continue
		}
		_, okA := h.recordOwnsCredentialProvider(record, providerA)
		_, okB := h.recordOwnsCredentialProvider(record, providerB)
		if okA && okB {
			return true
		}
	}
	return false
}

func (h *Host) authProviderRecord(provider string) *capabilityRecord {
	record, _ := h.authProviderEntry(provider)
	return record
}

func (h *Host) authProviderEntry(provider string) (*capabilityRecord, pluginapi.AuthProvider) {
	provider = normalizeProviderID(provider)
	if h == nil || provider == "" {
		return nil, nil
	}
	records := h.activeRecords()
	for _, record := range records {
		authProv, _, okMatch := h.recordAuthProviderForTarget(record, provider)
		if okMatch && authProv != nil {
			copyRecord := record
			return &copyRecord, authProv
		}
	}
	for _, record := range records {
		authProv, okOwn := h.recordOwnsCredentialProvider(record, provider)
		if okOwn && authProv != nil {
			copyRecord := record
			return &copyRecord, authProv
		}
	}
	return nil, nil
}

func (h *Host) authProviderCredentialEntry(provider string) (*capabilityRecord, pluginapi.AuthProvider) {
	return h.authProviderEntry(provider)
}

func (h *Host) recordOwnsCredentialProvider(record capabilityRecord, provider string) (pluginapi.AuthProvider, bool) {
	provider = normalizeProviderID(provider)
	if h == nil || provider == "" || !hasRecordAuthProvider(record) || h.isPluginFused(record.id) {
		return nil, false
	}
	if authProv, _, okMatch := h.recordAuthProviderForTarget(record, provider); okMatch && authProv != nil {
		return authProv, true
	}
	if portalID, okRecorded := h.recordedCredentialProviderPortal(record.id, provider); okRecorded {
		if portalID != "" {
			if authProv, _, okPortal := h.recordAuthProviderForTarget(record, portalID); okPortal && authProv != nil {
				return authProv, true
			}
		}
		authProv, _, okPrimary := h.recordAuthProviderForTarget(record, "")
		return authProv, okPrimary && authProv != nil
	}
	matched := false
	for _, candidate := range h.modelProvidersForPlugin(record.id) {
		if normalizeProviderID(candidate) == provider {
			matched = true
			break
		}
	}
	if !matched && record.plugin.Capabilities.Executor != nil {
		if candidate, okExec := h.executorProvider(record, record.plugin.Capabilities.Executor); okExec && candidate == provider {
			matched = true
		}
	}
	if matched {
		authProv, _, okPrimary := h.recordAuthProviderForTarget(record, "")
		return authProv, okPrimary && authProv != nil
	}
	for _, prov := range recordAuthProviders(record) {
		credIDs, okCredIDs := h.callAuthProviderCredentialProviders(record.id, prov)
		if !okCredIDs {
			return nil, false
		}
		for _, credID := range credIDs {
			if credID == provider {
				return prov, true
			}
		}
	}
	return nil, false
}

func (h *Host) recordAuthProviderForTarget(record capabilityRecord, target string) (pluginapi.AuthProvider, string, bool) {
	if h == nil || !hasRecordAuthProvider(record) || h.isPluginFused(record.id) {
		return nil, "", false
	}
	providers := recordAuthProviders(record)
	if len(providers) == 0 {
		return nil, "", false
	}
	target = normalizeProviderID(target)
	if target == "" {
		identifiers, okIDs := h.callAuthProviderIdentifiers(record.id, providers[0])
		if !okIDs {
			return nil, "", false
		}
		primary := ""
		if len(identifiers) > 0 {
			primary = identifiers[0]
		}
		return providers[0], primary, true
	}
	for _, prov := range providers {
		identifiers, okIDs := h.callAuthProviderIdentifiers(record.id, prov)
		if !okIDs {
			return nil, "", false
		}
		for _, identifier := range identifiers {
			if identifier == target {
				return prov, identifier, true
			}
		}
	}
	return nil, "", false
}

func (h *Host) callRecordAuthProviderIdentifiers(record capabilityRecord) ([]string, bool) {
	if h == nil || !hasRecordAuthProvider(record) || h.isPluginFused(record.id) {
		return nil, false
	}
	var out []string
	seen := make(map[string]struct{})
	for _, prov := range recordAuthProviders(record) {
		identifiers, okIDs := h.callAuthProviderIdentifiers(record.id, prov)
		if !okIDs {
			return nil, false
		}
		for _, identifier := range identifiers {
			if identifier == "" {
				continue
			}
			if _, exists := seen[identifier]; exists {
				continue
			}
			seen[identifier] = struct{}{}
			out = append(out, identifier)
		}
	}
	return out, true
}

func (h *Host) callAuthProviderIdentifiers(pluginID string, provider pluginapi.AuthProvider) (identifiers []string, ok bool) {
	if h == nil || provider == nil || h.isPluginFused(pluginID) {
		return nil, false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, "AuthProvider.Identifier", recovered)
			identifiers = nil
			ok = false
		}
	}()
	raw := []string{provider.Identifier()}
	if multi, okMulti := provider.(interface{ Identifiers() []string }); okMulti && multi != nil {
		raw = append(raw, multi.Identifiers()...)
	}
	return normalizeAuthProviderIdentifiers(raw...), true
}

func (h *Host) callAuthProviderCredentialProviders(pluginID string, provider pluginapi.AuthProvider) (providers []string, ok bool) {
	if h == nil || provider == nil || h.isPluginFused(pluginID) {
		return nil, false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, "AuthProvider.Identifier", recovered)
			providers = nil
			ok = false
		}
	}()
	cov, okCov := provider.(interface{ CredentialProviders() []string })
	if !okCov || cov == nil {
		return nil, true
	}
	return normalizeAuthProviderIdentifiers(cov.CredentialProviders()...), true
}

func (h *Host) callAuthProviderIdentifier(pluginID string, provider pluginapi.AuthProvider) (identifier string, ok bool) {
	identifiers, ok := h.callAuthProviderIdentifiers(pluginID, provider)
	if !ok {
		return "", false
	}
	if len(identifiers) == 0 {
		return "", true
	}
	return identifiers[0], true
}

func (h *Host) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	auths, handled, errParseAuths := h.ParseAuths(ctx, req)
	if errParseAuths != nil || !handled || len(auths) == 0 {
		return nil, handled, errParseAuths
	}
	return auths[0], true, nil
}

func extractRawJSONOAuthProviderHint(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil || payload == nil {
		return ""
	}
	for _, key := range []string{"oauth_provider", "portal"} {
		if value, ok := payload[key].(string); ok {
			if normalized := normalizeProviderID(value); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

func (h *Host) ParseAuths(ctx context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	if h == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(req.Provider) != "" {
		if hint := extractRawJSONOAuthProviderHint(req.RawJSON); hint != "" {
			if hintRecord, hintProv := h.authProviderEntry(hint); hintRecord != nil && hintProv != nil {
				if owner, _ := h.authProviderEntry(req.Provider); owner == nil || owner.id == hintRecord.id {
					return h.callSingleProviderParseAuths(ctx, *hintRecord, hintProv, req)
				}
			}
		}
		record, _ := h.authProviderCredentialEntry(req.Provider)
		if record == nil {
			return nil, false, nil
		}
		return h.callParseAuths(ctx, *record, req)
	}
	for _, record := range h.activeRecords() {
		if !hasRecordAuthProvider(record) || h.isPluginFused(record.id) {
			continue
		}
		auths, handled, errParse := h.callParseAuths(ctx, record, req)
		if errParse != nil || handled {
			return auths, handled, errParse
		}
	}
	return nil, false, nil
}

func (h *Host) callParseAuth(ctx context.Context, record capabilityRecord, req pluginapi.AuthParseRequest) (auth *coreauth.Auth, handled bool, err error) {
	auths, handled, errParseAuths := h.callParseAuths(ctx, record, req)
	if errParseAuths != nil || !handled || len(auths) == 0 {
		return nil, handled, errParseAuths
	}
	return auths[0], true, nil
}

func (h *Host) callParseAuths(ctx context.Context, record capabilityRecord, req pluginapi.AuthParseRequest) (auths []*coreauth.Auth, handled bool, err error) {
	if h == nil || !hasRecordAuthProvider(record) || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return nil, false, nil
	}
	var candidates []pluginapi.AuthProvider
	if hint := extractRawJSONOAuthProviderHint(req.RawJSON); hint != "" {
		if matched, _, okMatch := h.recordAuthProviderForTarget(record, hint); okMatch && matched != nil {
			candidates = []pluginapi.AuthProvider{matched}
		}
	}
	if len(candidates) == 0 {
		if target := normalizeProviderID(req.Provider); target != "" {
			if matched, _, okMatch := h.recordAuthProviderForTarget(record, target); okMatch && matched != nil {
				candidates = []pluginapi.AuthProvider{matched}
			} else if owned, okOwn := h.recordOwnsCredentialProvider(record, target); okOwn && owned != nil {
				candidates = []pluginapi.AuthProvider{owned}
			}
		} else {
			candidates = recordAuthProviders(record)
		}
	}
	for _, provider := range candidates {
		auths, handled, err = h.callSingleProviderParseAuths(ctx, record, provider, req)
		if err != nil || handled {
			return auths, handled, err
		}
	}
	return nil, false, nil
}

func (h *Host) callSingleProviderParseAuths(ctx context.Context, record capabilityRecord, provider pluginapi.AuthProvider, req pluginapi.AuthParseRequest) (auths []*coreauth.Auth, handled bool, err error) {
	if h == nil || provider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return nil, false, nil
	}
	primaryIdentifier, okID := h.callAuthProviderIdentifier(record.id, provider)
	if !okID {
		return nil, false, fmt.Errorf("auth provider identifier panic")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "AuthProvider.ParseAuth", recovered)
			auths = nil
			handled = false
			err = fmt.Errorf("auth provider panic: %v", recovered)
		}
	}()
	portalIdentifier := h.resolveParsePortalIdentifier(record.id, provider, req.Provider, req.RawJSON, primaryIdentifier)
	if req.Host.AuthDir == "" {
		req.Host = h.hostConfigSummary()
	}
	req.Provider = normalizeProviderID(req.Provider)
	if req.Provider == "" {
		req.Provider = portalIdentifier
	}
	req.RawJSON = bytes.Clone(req.RawJSON)
	resp, errParse := provider.ParseAuth(ctx, req)
	if errParse != nil {
		return nil, false, errParse
	}
	if !resp.Handled {
		return nil, false, nil
	}
	datas := pluginAuthParseResponseAuths(resp)
	auths = make([]*coreauth.Auth, 0, len(datas))
	for _, data := range datas {
		if strings.TrimSpace(data.Provider) == "" {
			data.Provider = req.Provider
		}
		if strings.TrimSpace(data.Provider) == "" {
			data.Provider = primaryIdentifier
		}
		if normalizeProviderID(data.Provider) == "" {
			return nil, true, fmt.Errorf("auth provider %s returned auth without provider", record.id)
		}
		effectivePortal := firstNonEmpty(authDataPortalHint(&data), portalIdentifier)
		stampAuthDataOAuthProvider(&data, effectivePortal)
		parsed := h.AuthDataToCoreAuth(data, req.Path, req.FileName)
		if parsed == nil {
			return nil, true, fmt.Errorf("auth provider %s returned invalid auth data", record.id)
		}
		h.recordCredentialProvider(record.id, req.Provider, effectivePortal)
		h.recordCredentialProvider(record.id, data.Provider, effectivePortal)
		auths = append(auths, parsed)
	}
	return auths, true, nil
}

func (h *Host) resolveParsePortalIdentifier(pluginID string, provider pluginapi.AuthProvider, reqProvider string, rawJSON []byte, fallback string) string {
	if identifiers, ok := h.callAuthProviderIdentifiers(pluginID, provider); ok {
		matches := func(candidate string) bool {
			candidate = normalizeProviderID(candidate)
			if candidate == "" {
				return false
			}
			for _, id := range identifiers {
				if id == candidate {
					return true
				}
			}
			return false
		}
		if hint := extractRawJSONOAuthProviderHint(rawJSON); matches(hint) {
			return normalizeProviderID(hint)
		}
		if matches(reqProvider) {
			return normalizeProviderID(reqProvider)
		}
	}
	return normalizeProviderID(fallback)
}

func (h *Host) recordCredentialProvider(pluginID, provider, portalIdentifier string) {
	pluginID = strings.TrimSpace(pluginID)
	provider = normalizeProviderID(provider)
	portalIdentifier = normalizeProviderID(portalIdentifier)
	if h == nil || pluginID == "" || provider == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.authCredentialProviders == nil {
		h.authCredentialProviders = make(map[string]map[string]string)
	}
	set := h.authCredentialProviders[pluginID]
	if set == nil {
		set = make(map[string]string)
		h.authCredentialProviders[pluginID] = set
	}
	if _, exists := set[provider]; !exists || set[provider] == "" {
		set[provider] = portalIdentifier
	}
}

func (h *Host) recordedCredentialProviderPortal(pluginID, provider string) (string, bool) {
	pluginID = strings.TrimSpace(pluginID)
	provider = normalizeProviderID(provider)
	if h == nil || pluginID == "" || provider == "" {
		return "", false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.authCredentialProviders[pluginID]
	portal, ok := set[provider]
	return portal, ok
}

func pluginAuthParseResponseAuths(resp pluginapi.AuthParseResponse) []pluginapi.AuthData {
	if len(resp.Auths) > 0 {
		return append([]pluginapi.AuthData(nil), resp.Auths...)
	}
	return []pluginapi.AuthData{resp.Auth}
}

type oauthProxyURLContextKey struct{}

// WithOAuthProxyURL attaches a session-scoped proxy URL to ctx for plugin OAuth login calls.
func WithOAuthProxyURL(ctx context.Context, proxyURL string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return ctx
	}
	return context.WithValue(ctx, oauthProxyURLContextKey{}, proxyURL)
}

func oauthProxyURLFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(oauthProxyURLContextKey{}).(string)
	return strings.TrimSpace(value)
}

func (h *Host) StartLogin(ctx context.Context, provider string, baseURL string, metadata ...map[string]any) (pluginapi.AuthLoginStartResponse, bool, error) {
	record := h.authProviderRecord(provider)
	if record == nil {
		return pluginapi.AuthLoginStartResponse{}, false, nil
	}
	var startMetadata map[string]any
	if len(metadata) > 0 {
		startMetadata = metadata[0]
	}
	return h.callStartLogin(ctx, *record, provider, baseURL, startMetadata)
}

func (h *Host) callStartLogin(ctx context.Context, record capabilityRecord, provider string, baseURL string, metadata map[string]any) (resp pluginapi.AuthLoginStartResponse, handled bool, err error) {
	authProvider, _, okProvider := h.recordAuthProviderForTarget(record, provider)
	if !okProvider || authProvider == nil {
		authProvider, okProvider = h.recordOwnsCredentialProvider(record, provider)
	}
	if h == nil || !okProvider || authProvider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.AuthLoginStartResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "AuthProvider.StartLogin", recovered)
			resp = pluginapi.AuthLoginStartResponse{}
			handled = false
			err = fmt.Errorf("auth provider start login panic: %v", recovered)
		}
	}()
	hostSummary := h.hostConfigSummary()
	sessionProxyURL := oauthProxyURLFromContext(ctx)
	if sessionProxyURL != "" {
		hostSummary.ProxyURL = sessionProxyURL
	}
	req := pluginapi.AuthLoginStartRequest{
		Provider:   normalizeProviderID(provider),
		BaseURL:    strings.TrimSpace(baseURL),
		Host:       hostSummary,
		HTTPClient: h.newHTTPClientWithProxy(nil, sessionProxyURL, provider),
		Metadata:   cloneAnyMap(metadata),
	}
	resp, errStart := authProvider.StartLogin(ctx, req)
	if errStart != nil {
		return pluginapi.AuthLoginStartResponse{}, true, errStart
	}
	return resp, true, nil
}

func (h *Host) PollLogin(ctx context.Context, provider, state string, metadata ...map[string]any) (pluginapi.AuthLoginPollResponse, bool, error) {
	record := h.authProviderRecord(provider)
	if record == nil {
		return pluginapi.AuthLoginPollResponse{}, false, nil
	}
	var pollMetadata map[string]any
	if len(metadata) > 0 {
		pollMetadata = metadata[0]
	}
	return h.callPollLogin(ctx, *record, provider, state, pollMetadata)
}

func (h *Host) callPollLogin(ctx context.Context, record capabilityRecord, provider, state string, metadata map[string]any) (resp pluginapi.AuthLoginPollResponse, handled bool, err error) {
	authProvider, matchedPortal, okProvider := h.recordAuthProviderForTarget(record, provider)
	if !okProvider || authProvider == nil {
		authProvider, okProvider = h.recordOwnsCredentialProvider(record, provider)
	}
	if h == nil || !okProvider || authProvider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.AuthLoginPollResponse{}, false, nil
	}
	if matchedPortal == "" {
		matchedPortal, _ = h.callAuthProviderIdentifier(record.id, authProvider)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "AuthProvider.PollLogin", recovered)
			resp = pluginapi.AuthLoginPollResponse{}
			handled = false
			err = fmt.Errorf("auth provider poll login panic: %v", recovered)
		}
	}()
	hostSummary := h.hostConfigSummary()
	sessionProxyURL := oauthProxyURLFromContext(ctx)
	if sessionProxyURL != "" {
		hostSummary.ProxyURL = sessionProxyURL
	}
	req := pluginapi.AuthLoginPollRequest{
		Provider:   normalizeProviderID(provider),
		State:      strings.TrimSpace(state),
		Host:       hostSummary,
		HTTPClient: h.newHTTPClientWithProxy(nil, sessionProxyURL, provider),
		Metadata:   cloneAnyMap(metadata),
	}
	resp, errPoll := authProvider.PollLogin(ctx, req)
	if errPoll != nil {
		return pluginapi.AuthLoginPollResponse{}, true, errPoll
	}
	if resp.Status == pluginapi.AuthLoginStatusSuccess {
		for index := range resp.Auths {
			stampAuthDataOAuthProvider(&resp.Auths[index], matchedPortal)
			h.recordCredentialProvider(record.id, resp.Auths[index].Provider, matchedPortal)
		}
		stampAuthDataOAuthProvider(&resp.Auth, matchedPortal)
		h.recordCredentialProvider(record.id, resp.Auth.Provider, matchedPortal)
	}
	return resp, true, nil
}

func authDataPortalHint(data *pluginapi.AuthData) string {
	if data == nil {
		return ""
	}
	for _, key := range []string{"oauth_provider", "portal"} {
		if data.Attributes != nil {
			if v := normalizeProviderID(data.Attributes[key]); v != "" {
				return v
			}
		}
		if data.Metadata != nil {
			if raw, ok := data.Metadata[key].(string); ok {
				if v := normalizeProviderID(raw); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func stampAuthDataOAuthProvider(data *pluginapi.AuthData, portalIdentifier string) {
	if data == nil {
		return
	}
	portalIdentifier = firstNonEmpty(authDataPortalHint(data), normalizeProviderID(portalIdentifier))
	if portalIdentifier == "" {
		return
	}
	credProvider := normalizeProviderID(data.Provider)
	if credProvider == "" || credProvider == portalIdentifier {
		return
	}
	if data.Metadata == nil {
		data.Metadata = make(map[string]any)
	}
	if _, exists := data.Metadata["oauth_provider"]; !exists {
		data.Metadata["oauth_provider"] = portalIdentifier
	}
	if data.Attributes == nil {
		data.Attributes = make(map[string]string)
	}
	if strings.TrimSpace(data.Attributes["oauth_provider"]) == "" {
		data.Attributes["oauth_provider"] = portalIdentifier
	}
}

func authPortalHints(auth *coreauth.Auth) []string {
	if auth == nil {
		return nil
	}
	out := make([]string, 0, 2)
	add := func(v string) {
		v = normalizeProviderID(v)
		if v == "" {
			return
		}
		for _, existing := range out {
			if existing == v {
				return
			}
		}
		out = append(out, v)
	}
	for _, key := range []string{"oauth_provider", "portal"} {
		if auth.Attributes != nil {
			add(auth.Attributes[key])
		}
		if auth.Metadata != nil {
			if raw, ok := auth.Metadata[key].(string); ok {
				add(raw)
			}
		}
	}
	return out
}

func (h *Host) authProviderCredentialEntryForAuth(auth *coreauth.Auth) (*capabilityRecord, pluginapi.AuthProvider) {
	if h == nil || auth == nil {
		return nil, nil
	}
	credProv := normalizeProviderID(authProvider(auth))
	for _, hint := range authPortalHints(auth) {
		for _, record := range h.activeRecords() {
			portalAuthProv, _, okPortal := h.recordAuthProviderForTarget(record, hint)
			if !okPortal || portalAuthProv == nil {
				continue
			}
			if credProv != "" && credProv != hint {
				if explicitOwner, _ := h.authProviderEntry(credProv); explicitOwner != nil && explicitOwner.id != record.id {
					continue
				}
				h.recordCredentialProvider(record.id, credProv, hint)
			}
			copyRecord := record
			return &copyRecord, portalAuthProv
		}
	}
	if credProv != "" {
		return h.authProviderCredentialEntry(credProv)
	}
	return nil, nil
}

func (h *Host) RefreshAuth(ctx context.Context, auth *coreauth.Auth) (refreshed *coreauth.Auth, handled bool, err error) {
	if h == nil || auth == nil {
		return nil, false, nil
	}
	record, authProv := h.authProviderCredentialEntryForAuth(auth)
	if record == nil || authProv == nil {
		return nil, false, nil
	}
	if !h.recordCurrent(*record) {
		return nil, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "AuthProvider.RefreshAuth", recovered)
			refreshed = nil
			handled = true
			err = fmt.Errorf("auth provider refresh panic: %v", recovered)
		}
	}()

	pluginResp, errRefresh := authProv.RefreshAuth(ctx, pluginapi.AuthRefreshRequest{
		AuthID:       authID(auth),
		AuthProvider: authProvider(auth),
		StorageJSON:  storageJSONFromAuth(auth),
		Metadata:     cloneAnyMap(authMetadata(auth)),
		Attributes:   authAttributes(auth),
		Host:         h.hostConfigSummary(),
		HTTPClient:   h.newHTTPClient(auth),
	})
	if errRefresh != nil {
		return nil, true, errRefresh
	}
	data := pluginResp.Auth
	if strings.TrimSpace(data.Provider) == "" {
		data.Provider = authProvider(auth)
	}
	if strings.TrimSpace(data.ID) == "" {
		data.ID = authID(auth)
	}
	if strings.TrimSpace(data.FileName) == "" {
		data.FileName = auth.FileName
	}
	if strings.TrimSpace(data.Label) == "" {
		data.Label = auth.Label
	}
	if strings.TrimSpace(data.Prefix) == "" {
		data.Prefix = auth.Prefix
	}
	if strings.TrimSpace(data.ProxyURL) == "" {
		data.ProxyURL = auth.ProxyURL
	}
	if len(data.Metadata) == 0 {
		data.Metadata = cloneAnyMap(auth.Metadata)
	}
	if len(data.Attributes) == 0 {
		if auth != nil {
			data.Attributes = cloneStringMap(auth.Attributes)
		}
	} else if auth != nil {
		attributes := cloneStringMap(data.Attributes)
		for key, value := range auth.Attributes {
			if _, exists := attributes[key]; !exists {
				attributes[key] = value
			}
		}
		data.Attributes = attributes
	}
	preserveAuthOAuthProviderMetadata(&data, auth)
	preserveFileAuthPriority(&data, auth)
	if len(data.StorageJSON) == 0 {
		data.StorageJSON = storageJSONFromAuth(auth)
	}
	if pluginResp.NextRefreshAfter.IsZero() {
		data.NextRefreshAfter = auth.NextRefreshAfter
	} else {
		data.NextRefreshAfter = pluginResp.NextRefreshAfter
	}
	path := ""
	if auth != nil && auth.Attributes != nil {
		path = auth.Attributes[coreauth.AttributePath]
	}
	next := h.AuthDataToCoreAuth(data, path, data.FileName)
	if next == nil {
		return nil, true, fmt.Errorf("auth provider refresh returned invalid auth data")
	}
	next.Index = auth.Index
	next.CreatedAt = auth.CreatedAt
	next.UpdatedAt = auth.UpdatedAt
	return next, true, nil
}

func preserveAuthOAuthProviderMetadata(data *pluginapi.AuthData, auth *coreauth.Auth) {
	if data == nil || auth == nil || len(auth.Metadata) == 0 {
		return
	}
	for _, key := range []string{"oauth_provider", "portal"} {
		raw, ok := auth.Metadata[key].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		if data.Metadata == nil {
			data.Metadata = make(map[string]any)
		}
		if _, exists := data.Metadata[key]; !exists {
			data.Metadata[key] = raw
		}
	}
}

func preserveFileAuthPriority(data *pluginapi.AuthData, auth *coreauth.Auth) {
	if data == nil || auth == nil || auth.Attributes[coreauth.AttributeSourceBackend] != coreauth.AuthSourceFile {
		return
	}
	if data.Attributes == nil {
		data.Attributes = make(map[string]string)
	}
	for _, key := range []string{coreauth.AttributePath, coreauth.AttributeSource, coreauth.AttributeSourceBackend, coreauth.AttributeFilePriority} {
		if value, ok := auth.Attributes[key]; ok {
			data.Attributes[key] = value
		}
	}
	if auth.Attributes[coreauth.AttributeFilePriority] != "true" {
		delete(data.Attributes, coreauth.AttributeFilePriority)
		return
	}
	if priority, ok := auth.Attributes["priority"]; ok {
		data.Attributes["priority"] = priority
	} else {
		delete(data.Attributes, "priority")
	}
	if data.Metadata == nil {
		data.Metadata = make(map[string]any)
	}
	if priority, ok := auth.Metadata["priority"]; ok {
		data.Metadata["priority"] = priority
	} else {
		delete(data.Metadata, "priority")
	}
}

func (h *Host) AuthDataToCoreAuth(data pluginapi.AuthData, path, fileName string) *coreauth.Auth {
	authDir := ""
	if h != nil {
		authDir = h.hostConfigSummary().AuthDir
	}
	return pluginAuthDataToCoreAuth(data, path, fileName, authDir)
}

type pluginTokenStorage struct {
	provider string
	rawJSON  []byte
	meta     map[string]any
}

func (s *pluginTokenStorage) SetMetadata(meta map[string]any) {
	if s == nil {
		return
	}
	if _, hadPriority := s.meta["priority"]; hadPriority {
		if _, hasPriority := meta["priority"]; !hasPriority {
			var raw map[string]any
			if errUnmarshal := json.Unmarshal(s.rawJSON, &raw); errUnmarshal == nil {
				delete(raw, "priority")
				if cleaned, errMarshal := json.Marshal(raw); errMarshal == nil {
					s.rawJSON = cleaned
				}
			}
		}
	}
	s.meta = cloneAnyMap(meta)
}

func (s *pluginTokenStorage) RawJSON() []byte {
	if s == nil {
		return nil
	}
	payload, errPayload := mergedStorageJSON(s.rawJSON, s.meta, s.provider)
	if errPayload != nil {
		return nil
	}
	return payload
}

func (s *pluginTokenStorage) SaveTokenToFile(path string) error {
	if s == nil {
		return fmt.Errorf("plugin token storage is nil")
	}
	payload, errPayload := mergedStorageJSON(s.rawJSON, s.meta, s.provider)
	if errPayload != nil {
		return errPayload
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return fmt.Errorf("plugin token storage payload is empty")
	}
	if pluginTokenStorageFileCurrent(path, payload) {
		return nil
	}
	return atomicWriteFile(path, payload)
}

func pluginTokenStorageFileCurrent(path string, payload []byte) bool {
	if strings.TrimSpace(path) == "" || len(bytes.TrimSpace(payload)) == 0 {
		return false
	}
	current, errRead := os.ReadFile(path)
	if errRead != nil {
		return false
	}
	return jsonPayloadEqual(current, payload)
}

func jsonPayloadEqual(left, right []byte) bool {
	var leftValue any
	if errUnmarshalLeft := json.Unmarshal(left, &leftValue); errUnmarshalLeft != nil {
		return false
	}
	var rightValue any
	if errUnmarshalRight := json.Unmarshal(right, &rightValue); errUnmarshalRight != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func mergedStorageJSON(raw []byte, metadata map[string]any, provider string) ([]byte, error) {
	out := make(map[string]any)
	if len(bytes.TrimSpace(raw)) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &out); errUnmarshal != nil {
			return nil, fmt.Errorf("decode plugin token storage: %w", errUnmarshal)
		}
		if out == nil {
			out = make(map[string]any)
		}
	}
	for key, value := range metadata {
		out[key] = value
	}
	provider = normalizeProviderID(provider)
	if provider != "" {
		out["type"] = provider
	}
	coreauth.NormalizeCredentialMetadata(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("plugin token storage payload is empty")
	}
	payload, errMarshal := json.Marshal(out)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode plugin token storage: %w", errMarshal)
	}
	return payload, nil
}

func atomicWriteFile(path string, data []byte) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("path is empty")
	}
	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create auth directory: %w", errMkdir)
	}
	tmp, errCreate := os.CreateTemp(dir, ".plugin-auth-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create temp auth file: %w", errCreate)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, errWrite := tmp.Write(data); errWrite != nil {
		if errClose := tmp.Close(); errClose != nil {
			errWrite = fmt.Errorf("%w; close temp auth file: %v", errWrite, errClose)
		}
		return fmt.Errorf("write temp auth file: %w", errWrite)
	}
	if errClose := tmp.Close(); errClose != nil {
		return fmt.Errorf("close temp auth file: %w", errClose)
	}
	if errRename := os.Rename(tmpPath, path); errRename != nil {
		return fmt.Errorf("rename temp auth file: %w", errRename)
	}
	return nil
}

func pluginAuthDataToCoreAuth(data pluginapi.AuthData, path, fileName string, authDir string) *coreauth.Auth {
	provider := normalizeProviderID(data.Provider)
	if provider == "" {
		return nil
	}
	metadata := cloneAnyMap(data.Metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	if provider != "" {
		metadata["type"] = provider
	}
	attributes := cloneStringMap(data.Attributes)
	if attributes == nil {
		attributes = make(map[string]string)
	}
	path = strings.TrimSpace(path)
	if path != "" {
		if attributes[coreauth.AttributePath] == "" {
			attributes[coreauth.AttributePath] = path
		}
		if attributes[coreauth.AttributeSource] == "" {
			attributes[coreauth.AttributeSource] = path
		}
		if attributes[coreauth.AttributeSourceBackend] == "" {
			attributes[coreauth.AttributeSourceBackend] = coreauth.AuthSourceFile
		}
	}
	fileName = strings.TrimSpace(firstNonEmpty(data.FileName, fileName))
	if fileName != "" && attributes[coreauth.AttributeSource] == "" {
		attributes[coreauth.AttributeSource] = fileName
	}
	id := strings.TrimSpace(data.ID)
	if id == "" {
		id = authIDForPath(firstNonEmpty(path, fileName), authDir)
	}
	status := coreauth.StatusActive
	if data.Disabled {
		status = coreauth.StatusDisabled
	}
	now := time.Now().UTC()
	auth := &coreauth.Auth{
		Provider:         provider,
		ID:               id,
		FileName:         fileName,
		Label:            strings.TrimSpace(data.Label),
		Prefix:           strings.TrimSpace(data.Prefix),
		ProxyURL:         strings.TrimSpace(data.ProxyURL),
		Disabled:         data.Disabled,
		Status:           status,
		Storage:          &pluginTokenStorage{provider: provider, rawJSON: bytes.Clone(data.StorageJSON), meta: metadata},
		Metadata:         metadata,
		Attributes:       attributes,
		CreatedAt:        now,
		UpdatedAt:        now,
		NextRefreshAfter: data.NextRefreshAfter,
	}
	return auth
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
