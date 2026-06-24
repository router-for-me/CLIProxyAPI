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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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
		if rel, errRel := filepath.Rel(authDir, path); errRel == nil && rel != "" && !strings.HasPrefix(rel, "..") {
			id = rel
		}
	}
	id = filepath.ToSlash(filepath.Clean(id))
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func (h *Host) AuthProviderIdentifiers() []string {
	if h == nil {
		return nil
	}
	out := make([]string, 0)
	for _, record := range h.activeRecords() {
		provider := record.plugin.Capabilities.AuthProvider
		if provider == nil || h.isPluginFused(record.id) {
			continue
		}
		identifier, okIdentifier := h.callAuthProviderIdentifier(record.id, provider)
		if okIdentifier && identifier != "" {
			out = append(out, identifier)
		}
	}
	return out
}

func (h *Host) HasAuthProvider(provider string) bool {
	return h.authProviderRecord(provider) != nil
}

func (h *Host) authProviderRecord(provider string) *capabilityRecord {
	provider = normalizeProviderID(provider)
	if h == nil || provider == "" {
		return nil
	}
	for _, record := range h.activeRecords() {
		authProvider := record.plugin.Capabilities.AuthProvider
		if authProvider == nil || h.isPluginFused(record.id) {
			continue
		}
		identifier, okIdentifier := h.callAuthProviderIdentifier(record.id, authProvider)
		if okIdentifier && identifier == provider {
			copyRecord := record
			return &copyRecord
		}
	}
	return nil
}

func (h *Host) callAuthProviderIdentifier(pluginID string, provider pluginapi.AuthProvider) (identifier string, ok bool) {
	if h == nil || provider == nil || h.isPluginFused(pluginID) {
		return "", false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, "AuthProvider.Identifier", recovered)
			identifier = ""
			ok = false
		}
	}()
	return normalizeProviderID(provider.Identifier()), true
}

func (h *Host) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	auths, handled, errParseAuths := h.ParseAuths(ctx, req)
	if errParseAuths != nil || !handled || len(auths) == 0 {
		return nil, handled, errParseAuths
	}
	return auths[0], true, nil
}

func (h *Host) ParseAuths(ctx context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	if h == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(req.Provider) != "" {
		record := h.authProviderRecord(req.Provider)
		if record == nil {
			return nil, false, nil
		}
		return h.callParseAuths(ctx, *record, req)
	}
	for _, record := range h.activeRecords() {
		if record.plugin.Capabilities.AuthProvider == nil || h.isPluginFused(record.id) {
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
	provider := record.plugin.Capabilities.AuthProvider
	if h == nil || provider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return nil, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "AuthProvider.ParseAuth", recovered)
			auths = nil
			handled = false
			err = fmt.Errorf("auth provider panic: %v", recovered)
		}
	}()
	if req.Host.AuthDir == "" {
		req.Host = h.hostConfigSummary()
	}
	req.Provider = normalizeProviderID(req.Provider)
	if req.Provider == "" {
		req.Provider = normalizeProviderID(provider.Identifier())
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
			data.Provider = normalizeProviderID(provider.Identifier())
		}
		if normalizeProviderID(data.Provider) == "" {
			return nil, true, fmt.Errorf("auth provider %s returned auth without provider", record.id)
		}
		parsed := h.AuthDataToCoreAuth(data, req.Path, req.FileName)
		if parsed == nil {
			return nil, true, fmt.Errorf("auth provider %s returned invalid auth data", record.id)
		}
		auths = append(auths, parsed)
	}
	return auths, true, nil
}

func pluginAuthParseResponseAuths(resp pluginapi.AuthParseResponse) []pluginapi.AuthData {
	if len(resp.Auths) > 0 {
		return append([]pluginapi.AuthData(nil), resp.Auths...)
	}
	return []pluginapi.AuthData{resp.Auth}
}

func (h *Host) StartLogin(ctx context.Context, provider string, baseURL string) (pluginapi.AuthLoginStartResponse, bool, error) {
	record := h.authProviderRecord(provider)
	if record == nil {
		return pluginapi.AuthLoginStartResponse{}, false, nil
	}
	return h.callStartLogin(ctx, *record, provider, baseURL)
}

func (h *Host) callStartLogin(ctx context.Context, record capabilityRecord, provider string, baseURL string) (resp pluginapi.AuthLoginStartResponse, handled bool, err error) {
	authProvider := record.plugin.Capabilities.AuthProvider
	if h == nil || authProvider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
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
	req := pluginapi.AuthLoginStartRequest{
		Provider:   normalizeProviderID(provider),
		BaseURL:    strings.TrimSpace(baseURL),
		Host:       h.hostConfigSummary(),
		HTTPClient: h.newHTTPClient(nil),
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
	authProvider := record.plugin.Capabilities.AuthProvider
	if h == nil || authProvider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.AuthLoginPollResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "AuthProvider.PollLogin", recovered)
			resp = pluginapi.AuthLoginPollResponse{}
			handled = false
			err = fmt.Errorf("auth provider poll login panic: %v", recovered)
		}
	}()
	req := pluginapi.AuthLoginPollRequest{
		Provider:   normalizeProviderID(provider),
		State:      strings.TrimSpace(state),
		Host:       h.hostConfigSummary(),
		HTTPClient: h.newHTTPClient(nil),
		Metadata:   cloneAnyMap(metadata),
	}
	resp, errPoll := authProvider.PollLogin(ctx, req)
	if errPoll != nil {
		return pluginapi.AuthLoginPollResponse{}, true, errPoll
	}
	return resp, true, nil
}

func (h *Host) RefreshAuth(ctx context.Context, auth *coreauth.Auth) (refreshed *coreauth.Auth, handled bool, err error) {
	if h == nil || auth == nil {
		return nil, false, nil
	}
	record := h.authProviderRecord(authProvider(auth))
	if record == nil || record.plugin.Capabilities.AuthProvider == nil {
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

	pluginResp, errRefresh := record.plugin.Capabilities.AuthProvider.RefreshAuth(ctx, pluginapi.AuthRefreshRequest{
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
		data.Attributes = cloneStringMap(auth.Attributes)
	}
	if len(data.StorageJSON) == 0 {
		data.StorageJSON = storageJSONFromAuth(auth)
	}
	if pluginResp.NextRefreshAfter.IsZero() {
		data.NextRefreshAfter = auth.NextRefreshAfter
	} else {
		data.NextRefreshAfter = pluginResp.NextRefreshAfter
	}
	next := h.AuthDataToCoreAuth(data, "", data.FileName)
	if next == nil {
		return nil, true, fmt.Errorf("auth provider refresh returned invalid auth data")
	}
	next.Index = auth.Index
	next.CreatedAt = auth.CreatedAt
	next.UpdatedAt = auth.UpdatedAt
	return next, true, nil
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
		attributes[coreauth.AttributePath] = path
		attributes[coreauth.AttributeSource] = path
		attributes[coreauth.AttributeSourceBackend] = coreauth.AuthSourceFile
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
