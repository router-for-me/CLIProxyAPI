package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	// liveModelSyncInterval is how often a credential's model list is read again.
	liveModelSyncInterval = time.Hour
	// liveModelSyncRetryBackoff is the wait before a failed read is retried.
	liveModelSyncRetryBackoff = 10 * time.Minute
	// liveModelSyncTick is how often credentials are checked for a due sync, which
	// is also how long a newly added credential waits for its first one.
	liveModelSyncTick = 5 * time.Minute
	// liveModelSyncStartDelay lets the first auth scan and token refresh finish.
	liveModelSyncStartDelay = 15 * time.Second
	// codexLiveClientVersion is the client version sent with the Codex model
	// catalog request; the provider omits models that need a newer client.
	codexLiveClientVersion = "0.159.0"
	// metaModelsPath is the Muse Code model catalog. It sits at the root of the
	// origin each Meta credential talks to, so its URL follows the credential's
	// base URL rather than a fixed address.
	metaModelsPath = "/muse-code/models"
)

// Provider model endpoints. Variables so tests can point them at a local server.
var (
	claudeModelsURL = "https://api.anthropic.com/v1/models?limit=1000"
	codexModelsURL  = "https://chatgpt.com/backend-api/codex/models"
)

// liveModel is one model a provider reports for a credential.
type liveModel struct {
	id                  string
	displayName         string
	description         string
	created             int64
	contextLength       int
	maxCompletionTokens int
	thinkingLevels      []string
	// noThinking marks a model the provider reports as not reasoning, so it must
	// not inherit reasoning levels from a catalog model.
	noThinking      bool
	inputModalities []string
	plans           []string
}

// metaEffortOrder is the thinking package's standard level order. Meta's
// reasoning variants are kept in it, lowest first, because the lowest level
// stands in when a client turns reasoning off; other names are dropped.
var metaEffortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// liveModelCache holds, per credential, the models the provider reports that
// the model catalog does not list.
type liveModelCache struct {
	mu        sync.RWMutex
	additions map[string][]*ModelInfo
}

var liveModelAdditions = &liveModelCache{additions: make(map[string][]*ModelInfo)}

func (c *liveModelCache) get(authID string) []*ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.additions[authID]
}

// set stores the additions for a credential and reports whether the set of
// model IDs changed.
func (c *liveModelCache) set(authID string, additions []*ModelInfo) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := !sameModelIDs(c.additions[authID], additions)
	if len(additions) == 0 {
		delete(c.additions, authID)
	} else {
		c.additions[authID] = additions
	}
	return changed
}

func (c *liveModelCache) retain(known map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for authID := range c.additions {
		if _, ok := known[authID]; !ok {
			delete(c.additions, authID)
		}
	}
}

func sameModelIDs(a, b []*ModelInfo) bool {
	if len(a) != len(b) {
		return false
	}
	ids := make(map[string]struct{}, len(a))
	for _, model := range a {
		ids[model.ID] = struct{}{}
	}
	for _, model := range b {
		if _, ok := ids[model.ID]; !ok {
			return false
		}
	}
	return true
}

// liveModelsEnabled reports whether live model discovery is on for the provider.
func liveModelsEnabled(cfg *config.Config, provider string) bool {
	if cfg == nil || cfg.Home.Enabled {
		return false
	}
	switch provider {
	case "claude":
		return cfg.Claude.LiveModels
	case "codex":
		return cfg.Codex.LiveModels
	case "meta":
		return cfg.Meta.LiveModels
	default:
		return false
	}
}

// liveModelProvider returns the provider key when the provider can list the
// credential's models: a Claude or Codex OAuth credential the control-plane
// probe accepts, or any enabled Meta credential other than a meta-api-key
// config entry. Signing in stores a Meta login as a file credential, and the
// Meta executor supplies its request credential, minting an API key from a DCA
// token when needed.
func liveModelProvider(auth *coreauth.Auth) (string, bool) {
	if provider, ok := oauthProbeProvider(auth); ok {
		return provider, true
	}
	if auth == nil || auth.Disabled || strings.TrimSpace(auth.ID) == "" {
		return "", false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "meta") || coreauth.IsConfigAPIKeyAuth(auth) {
		return "", false
	}
	return "meta", true
}

// withLiveModels appends the models the provider reported for the credential
// that the catalog list does not carry yet. The catalog entry always wins for a
// model both know, so curated metadata is never replaced.
func (s *Service) withLiveModels(a *coreauth.Auth, provider string, models []*ModelInfo) []*ModelInfo {
	if s == nil || a == nil {
		return models
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if !liveModelsEnabled(cfg, provider) {
		return models
	}
	if _, ok := liveModelProvider(a); !ok {
		return models
	}
	additions := liveModelAdditions.get(a.ID)
	if len(additions) == 0 {
		return models
	}
	known := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model != nil {
			known[model.ID] = struct{}{}
		}
	}
	for _, addition := range additions {
		if _, ok := known[addition.ID]; ok {
			continue
		}
		models = append(models, registry.CloneModelInfo(addition))
	}
	return models
}

// startLiveModelSync keeps the model list of Claude, Codex and Meta credentials
// in step with what the provider reports for each account.
func (s *Service) startLiveModelSync(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		nextSync := make(map[string]time.Time)
		timer := time.NewTimer(liveModelSyncStartDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			s.syncLiveModels(ctx, nextSync, time.Now())
			timer.Reset(liveModelSyncTick)
		}
	}()
}

// syncLiveModels reads the provider model list of every credential that is due
// and re-registers the credentials whose list gained or lost a model. nextSync
// carries the earliest time each credential may be read again.
func (s *Service) syncLiveModels(ctx context.Context, nextSync map[string]time.Time, now time.Time) {
	if s == nil || s.coreManager == nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()

	auths := s.coreManager.List()
	known := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		known[auth.ID] = struct{}{}
	}
	for id := range nextSync {
		if _, ok := known[id]; !ok {
			delete(nextSync, id)
		}
	}
	liveModelAdditions.retain(known)

	for _, auth := range auths {
		if ctx.Err() != nil {
			return
		}
		provider, ok := liveModelProvider(auth)
		if !ok {
			continue
		}
		if !liveModelsEnabled(cfg, provider) {
			if liveModelAdditions.set(auth.ID, nil) {
				s.refreshModelRegistrationForAuth(auth)
			}
			delete(nextSync, auth.ID)
			continue
		}
		if next, pending := nextSync[auth.ID]; pending && now.Before(next) {
			continue
		}
		discovered, errFetch := s.fetchLiveModels(ctx, cfg, provider, auth)
		if errFetch != nil {
			nextSync[auth.ID] = now.Add(liveModelSyncRetryBackoff)
			log.Debugf("live models: %s credential %s: %v", provider, auth.ID, errFetch)
			continue
		}
		nextSync[auth.ID] = now.Add(liveModelSyncInterval)
		additions := liveModelAdditionsFor(provider, auth, discovered)
		if !liveModelAdditions.set(auth.ID, additions) {
			continue
		}
		ids := make([]string, 0, len(additions))
		for _, addition := range additions {
			ids = append(ids, addition.ID)
		}
		log.Infof("live models: %s credential %s now adds %d model(s) the catalog does not list: %s", provider, auth.ID, len(ids), strings.Join(ids, ", "))
		s.refreshModelRegistrationForAuth(auth)
	}
}

// fetchLiveModels reads the provider model list for one credential.
func (s *Service) fetchLiveModels(ctx context.Context, cfg *config.Config, provider string, auth *coreauth.Auth) ([]liveModel, error) {
	switch provider {
	case "claude":
		body, errGet := s.oauthProbeGET(ctx, cfg, provider, auth, claudeModelsURL)
		if errGet != nil {
			return nil, errGet
		}
		return parseClaudeLiveModels(body), nil
	case "codex":
		endpoint := codexModelsURL
		if parsed, errParse := url.Parse(endpoint); errParse == nil {
			query := parsed.Query()
			query.Set("client_version", codexLiveClientVersion)
			parsed.RawQuery = query.Encode()
			endpoint = parsed.String()
		}
		body, errGet := s.oauthProbeGET(ctx, cfg, provider, auth, endpoint)
		if errGet != nil {
			return nil, errGet
		}
		if changed, errMerge := registry.MergeLiveCodexClientModels(body); errMerge != nil {
			log.Debugf("live models: codex credential %s: client catalog not updated: %v", auth.ID, errMerge)
		} else if changed {
			log.Infof("live models: Codex client model catalog updated from credential %s", auth.ID)
		}
		return parseCodexLiveModels(body), nil
	case "meta":
		body, errGet := s.metaModelsGET(ctx, auth)
		if errGet != nil {
			return nil, errGet
		}
		// An answer without the list is not an empty catalog; failing keeps the
		// models found last time and retries after the backoff.
		if !gjson.GetBytes(body, "data").IsArray() {
			return nil, fmt.Errorf("model catalog response has no data list")
		}
		return parseMetaLiveModels(body), nil
	default:
		return nil, nil
	}
}

// metaModelsGET reads the Muse Code model catalog with one Meta credential. The
// request goes through the Meta executor, which supplies the credential as it
// does for model requests. A login that holds only a DCA token gets its API key
// minted through the manager first, which saves it, so the sync does not mint a
// new key on every read.
func (s *Service) metaModelsGET(ctx context.Context, auth *coreauth.Auth) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, oauthProbeTimeout)
	defer cancel()

	if registered, ok := s.coreManager.Executor("meta"); ok {
		if preparer, ok := registered.(coreauth.RequestAuthPreparer); ok {
			prepared, errPrepare := s.coreManager.PrepareRequestAuth(probeCtx, preparer, auth)
			if errPrepare != nil {
				return nil, fmt.Errorf("prepare credential: %w", errPrepare)
			}
			if prepared != nil {
				auth = prepared
			}
		}
	}
	endpoint, errURL := metaModelsURL(auth)
	if errURL != nil {
		return nil, errURL
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	req, errRequest := s.coreManager.NewHttpRequest(probeCtx, auth, http.MethodGet, endpoint, nil, headers)
	if errRequest != nil {
		return nil, fmt.Errorf("create request: %w", errRequest)
	}
	resp, errDo := s.coreManager.HttpRequest(probeCtx, auth, req)
	if errDo != nil {
		return nil, fmt.Errorf("request failed: %w", errDo)
	}
	return readProbeBody(resp)
}

// metaModelsURL returns the address of the Muse Code model catalog on the origin
// the Meta credential talks to. The catalog sits at the root of that origin,
// beside the versioned API its base URL names: the base_url attribute, else the
// base_url or api_base_url metadata, else Meta's default.
func metaModelsURL(auth *coreauth.Auth) (string, error) {
	baseURL := ""
	if auth != nil {
		if auth.Attributes != nil {
			baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		}
		for _, key := range []string{"base_url", "api_base_url"} {
			if baseURL != "" || auth.Metadata == nil {
				break
			}
			if value, ok := auth.Metadata[key].(string); ok {
				baseURL = strings.TrimSpace(value)
			}
		}
	}
	if baseURL == "" {
		baseURL = metaauth.DefaultAPIBaseURL
	}
	parsed, errParse := url.Parse(baseURL)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid base URL")
	}
	catalog := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: metaModelsPath}
	return catalog.String(), nil
}

// parseClaudeLiveModels reads the Anthropic model list response.
func parseClaudeLiveModels(body []byte) []liveModel {
	var models []liveModel
	gjson.GetBytes(body, "data").ForEach(func(_, entry gjson.Result) bool {
		id := strings.TrimSpace(entry.Get("id").String())
		if id == "" || !strings.HasPrefix(strings.ToLower(id), "claude") {
			return true
		}
		model := liveModel{
			id:                  id,
			displayName:         strings.TrimSpace(entry.Get("display_name").String()),
			contextLength:       int(entry.Get("max_input_tokens").Int()),
			maxCompletionTokens: int(entry.Get("max_tokens").Int()),
		}
		if createdAt := strings.TrimSpace(entry.Get("created_at").String()); createdAt != "" {
			if parsed, errParse := time.Parse(time.RFC3339Nano, createdAt); errParse == nil {
				model.created = parsed.Unix()
			}
		}
		models = append(models, model)
		return true
	})
	return models
}

// parseCodexLiveModels reads the Codex client model catalog response.
func parseCodexLiveModels(body []byte) []liveModel {
	var models []liveModel
	gjson.GetBytes(body, "models").ForEach(func(_, entry gjson.Result) bool {
		slug := strings.TrimSpace(entry.Get("slug").String())
		if slug == "" {
			return true
		}
		model := liveModel{
			id:            slug,
			displayName:   strings.TrimSpace(entry.Get("display_name").String()),
			description:   strings.TrimSpace(entry.Get("description").String()),
			contextLength: int(entry.Get("context_window").Int()),
		}
		entry.Get("supported_reasoning_levels").ForEach(func(_, level gjson.Result) bool {
			if effort := strings.TrimSpace(level.Get("effort").String()); effort != "" {
				model.thinkingLevels = append(model.thinkingLevels, effort)
			}
			return true
		})
		entry.Get("input_modalities").ForEach(func(_, modality gjson.Result) bool {
			if value := strings.TrimSpace(modality.String()); value != "" {
				model.inputModalities = append(model.inputModalities, value)
			}
			return true
		})
		entry.Get("available_in_plans").ForEach(func(_, plan gjson.Result) bool {
			if value := strings.ToLower(strings.TrimSpace(plan.String())); value != "" {
				model.plans = append(model.plans, value)
			}
			return true
		})
		models = append(models, model)
		return true
	})
	return models
}

// parseMetaLiveModels reads the Muse Code model catalog response. Only Muse
// models with Muse Code metadata are taken, so a model ID another provider
// serves is never routed to Meta. Models Meta hides from clients, and models
// that do not answer in text, are skipped.
func parseMetaLiveModels(body []byte) []liveModel {
	var models []liveModel
	gjson.GetBytes(body, "data").ForEach(func(_, entry gjson.Result) bool {
		id := strings.TrimSpace(entry.Get("id").String())
		museCode := entry.Get("metadata.muse-code")
		if !strings.HasPrefix(strings.ToLower(id), "muse-") || !museCode.Exists() || museCode.Get("is_hidden").Bool() {
			return true
		}
		if outputs := museCode.Get("modalities.output"); outputs.IsArray() && !gjsonArrayContains(outputs, "text") {
			return true
		}
		model := liveModel{
			id:                  id,
			displayName:         strings.TrimSpace(museCode.Get("name").String()),
			description:         strings.TrimSpace(museCode.Get("description").String()),
			created:             entry.Get("created").Int(),
			contextLength:       int(museCode.Get("limit.context").Int()),
			maxCompletionTokens: int(museCode.Get("limit.output").Int()),
		}
		efforts := make(map[string]bool)
		museCode.Get("variants").ForEach(func(name, variant gjson.Result) bool {
			effort := strings.TrimSpace(variant.Get("reasoningEffort").String())
			if effort == "" {
				effort = strings.TrimSpace(name.String())
			}
			efforts[strings.ToLower(effort)] = true
			return true
		})
		for _, level := range metaEffortOrder {
			if efforts[level] {
				model.thinkingLevels = append(model.thinkingLevels, level)
			}
		}
		if reasoning := museCode.Get("reasoning"); reasoning.Exists() && !reasoning.Bool() {
			model.noThinking, model.thinkingLevels = true, nil
		}
		museCode.Get("modalities.input").ForEach(func(_, modality gjson.Result) bool {
			if value := strings.TrimSpace(modality.String()); value != "" {
				model.inputModalities = append(model.inputModalities, value)
			}
			return true
		})
		models = append(models, model)
		return true
	})
	return models
}

func gjsonArrayContains(values gjson.Result, target string) bool {
	found := false
	values.ForEach(func(_, value gjson.Result) bool {
		found = strings.EqualFold(strings.TrimSpace(value.String()), target)
		return !found
	})
	return found
}

// liveModelAdditionsFor builds definitions for the discovered models that the
// catalog list for this credential does not carry.
func liveModelAdditionsFor(provider string, auth *coreauth.Auth, discovered []liveModel) []*ModelInfo {
	if len(discovered) == 0 {
		return nil
	}
	plan := ""
	if auth != nil && auth.Attributes != nil {
		plan = strings.ToLower(strings.TrimSpace(auth.Attributes["plan_type"]))
	}
	catalog := catalogModelsForLiveSync(provider, plan)
	known := make(map[string]struct{}, len(catalog))
	for _, model := range catalog {
		known[model.ID] = struct{}{}
	}
	var additions []*ModelInfo
	for _, model := range discovered {
		if _, ok := known[model.id]; ok {
			continue
		}
		if plan != "" && len(model.plans) > 0 && !containsString(model.plans, plan) {
			continue
		}
		known[model.id] = struct{}{}
		additions = append(additions, liveModelInfo(provider, model, catalog))
	}
	sort.Slice(additions, func(i, j int) bool { return additions[i].ID < additions[j].ID })
	return additions
}

// catalogModelsForLiveSync returns the catalog list a credential is registered
// with before live additions.
func catalogModelsForLiveSync(provider, codexPlan string) []*ModelInfo {
	switch provider {
	case "claude":
		return registry.GetClaudeModels()
	case "codex":
		switch codexPlan {
		case "plus":
			return registry.GetCodexPlusModels()
		case "team", "business", "go":
			return registry.GetCodexTeamModels()
		case "free":
			return registry.GetCodexFreeModels()
		default:
			return registry.GetCodexProModels()
		}
	case "meta":
		return registry.GetMetaModels()
	default:
		return nil
	}
}

// liveModelInfo builds the definition of a model the catalog does not list. It
// starts from the catalog model with the closest name, which carries the
// capability metadata the provider list does not report, and overrides what the
// provider does report.
func liveModelInfo(provider string, model liveModel, catalog []*ModelInfo) *ModelInfo {
	info := registry.CloneModelInfo(closestCatalogModel(model.id, catalog))
	if info == nil {
		info = &ModelInfo{Object: "model"}
		switch provider {
		case "claude":
			info.OwnedBy, info.Type = "anthropic", "claude"
		case "meta":
			info.OwnedBy, info.Type = "meta", "meta"
		default:
			info.OwnedBy, info.Type = "openai", "openai"
		}
	}
	info.ID = model.id
	info.MetadataModelID = ""
	info.Name = ""
	info.Version = ""
	info.DisplayName = model.displayName
	if info.DisplayName == "" {
		info.DisplayName = model.id
	}
	info.Description = model.description
	if model.created > 0 {
		info.Created = model.created
	}
	if model.contextLength > 0 {
		info.ContextLength = model.contextLength
	}
	if model.maxCompletionTokens > 0 {
		info.MaxCompletionTokens = model.maxCompletionTokens
	}
	if model.noThinking {
		info.Thinking = nil
	} else if len(model.thinkingLevels) > 0 {
		info.Thinking = &registry.ThinkingSupport{Levels: append([]string(nil), model.thinkingLevels...)}
	}
	if len(model.inputModalities) > 0 {
		info.SupportedInputModalities = append([]string(nil), model.inputModalities...)
	}
	return info
}

// closestCatalogModel returns the catalog model sharing the longest name prefix
// with id, preferring the newest among equals.
func closestCatalogModel(id string, catalog []*ModelInfo) *ModelInfo {
	var closest *ModelInfo
	longest := 0
	for _, candidate := range catalog {
		if candidate == nil {
			continue
		}
		shared := sharedPrefixLength(id, candidate.ID)
		if shared > longest || (shared == longest && closest != nil && candidate.Created > closest.Created) {
			closest, longest = candidate, shared
		}
	}
	return closest
}

func sharedPrefixLength(a, b string) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return limit
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
