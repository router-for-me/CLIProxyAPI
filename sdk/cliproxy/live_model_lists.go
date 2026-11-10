package cliproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// Live model lists (models.live-lists). Claude, Codex and Meta publish the models each account can
// use, but the built-in catalog is edited by hand and lags them: it can keep retired models and
// miss new ones for days. With the option on, CPA fetches every Claude, Codex and Meta login's list
// in the background (at startup, every ModelsRefreshInterval, and on POST /models/refresh) and
// registers the catalog filtered by it, plus the models the catalog doesn't have yet when the
// provider describes them (Claude and Codex; Meta lists ids only). Catalog values win for models
// it already has. Until a list arrives, or while fetches fail, the catalog is used unchanged, so a
// provider outage never empties a login. API keys keep the catalog.

const (
	liveModelListScanInterval = 10 * time.Minute
	liveModelListRetry        = 10 * time.Minute
	liveModelListMaxBody      = 8 << 20
	liveModelListMaxPages     = 10
	codexLiveModelListOrigin  = "codex-tui"
	// Codex filters /models by client_version: each model has a minimal_client_version, and an
	// older version silently gets a shorter list. CPA isn't the client, so it asks with a version
	// past every minimum to see everything the account has.
	codexLiveModelListClientVersion = "99.0.0"
)

// liveModelList is one login's list as its provider returned it.
type liveModelList struct {
	identity  string              // the account the list belongs to (liveModelListIdentity)
	ids       map[string]struct{} // lower-case ids
	extra     []*ModelInfo        // provider descriptions, used for ids the catalog lacks
	fetchedAt time.Time
}

type liveModelListStore struct {
	mu        sync.Mutex
	lists     map[string]liveModelList // by auth ID
	nextRetry map[string]time.Time     // by auth ID, after a failed fetch
	inFlight  map[string]struct{}      // by auth ID
	wake      chan struct{}
	force     atomic.Bool
	fetches   sync.WaitGroup
}

var liveModelLists = &liveModelListStore{
	lists:     make(map[string]liveModelList),
	nextRetry: make(map[string]time.Time),
	inFlight:  make(map[string]struct{}),
	wake:      make(chan struct{}, 1),
}

// liveModelListIdentity names the account behind a login. An auth file can be overwritten with
// another account under the same ID, so lists are kept per identity, not per ID alone. Logins
// without a stable account field fall back to a hash of the credential, which only costs an
// extra fetch after a token refresh.
func liveModelListIdentity(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	parts := []string{strings.ToLower(strings.TrimSpace(auth.Provider)), liveModelListBaseURL(auth, "")}
	stable := false
	for _, key := range []string{"account_id", "email", "user_id"} {
		value, _ := auth.Metadata[key].(string)
		if value = strings.TrimSpace(value); value == "" {
			value = strings.TrimSpace(auth.Attributes[key])
		}
		if value != "" {
			parts = append(parts, key+"="+value)
			stable = true
		}
	}
	if !stable {
		token, _ := auth.Metadata["access_token"].(string)
		if token = strings.TrimSpace(token); token == "" {
			token = strings.TrimSpace(auth.Attributes["api_key"])
		}
		sum := sha256.Sum256([]byte(token))
		parts = append(parts, "credential="+hex.EncodeToString(sum[:8]))
	}
	return strings.Join(parts, "|")
}

var liveModelListNow = time.Now

// get returns the login's list if it belongs to the same account.
func (st *liveModelListStore) get(authID, identity string) (liveModelList, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	list, ok := st.lists[authID]
	if !ok || list.identity != identity {
		return liveModelList{}, false
	}
	return list, true
}

// put stores a successful fetch and reports whether the ids or the provider descriptions changed.
func (st *liveModelListStore) put(authID string, list liveModelList) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	previous, existed := st.lists[authID]
	st.lists[authID] = list
	delete(st.nextRetry, authID)
	return !existed || !sameLiveModelList(previous, list)
}

func (st *liveModelListStore) fail(authID string, now time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextRetry[authID] = now.Add(liveModelListRetry)
}

// due reports whether a login's list should be fetched now: never fetched, fetched for another
// account, or older than ModelsRefreshInterval, unless a fetch is running or a failure is waiting
// for its retry.
func (st *liveModelListStore) due(authID, identity string, now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, running := st.inFlight[authID]; running {
		return false
	}
	list, ok := st.lists[authID]
	if ok && list.identity != identity {
		return true
	}
	if retry, failed := st.nextRetry[authID]; failed && now.Before(retry) {
		return false
	}
	return !ok || now.Sub(list.fetchedAt) >= registry.ModelsRefreshInterval
}

// start marks a fetch as running; it reports false if one already is.
func (st *liveModelListStore) start(authID string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, running := st.inFlight[authID]; running {
		return false
	}
	st.inFlight[authID] = struct{}{}
	st.fetches.Add(1)
	return true
}

func (st *liveModelListStore) finish(authID string) {
	st.mu.Lock()
	delete(st.inFlight, authID)
	st.mu.Unlock()
	st.fetches.Done()
}

// forget drops lists of logins that no longer exist.
func (st *liveModelListStore) forget(keep map[string]struct{}) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for id := range st.lists {
		if _, ok := keep[id]; !ok {
			delete(st.lists, id)
		}
	}
	for id := range st.nextRetry {
		if _, ok := keep[id]; !ok {
			delete(st.nextRetry, id)
		}
	}
}

// request wakes the refresh loop; force fetches every login instead of only the due ones.
func (st *liveModelListStore) request(force bool) {
	if force {
		st.force.Store(true)
	}
	select {
	case st.wake <- struct{}{}:
	default:
	}
}

// sameLiveModelList compares everything a list publishes: its account, ids and every field of the
// models it may add.
func sameLiveModelList(a, b liveModelList) bool {
	return a.identity == b.identity && reflect.DeepEqual(a.ids, b.ids) && reflect.DeepEqual(a.extra, b.extra)
}

// liveModelListProvider returns the provider whose live list applies to auth, or "".
func liveModelListProvider(auth *coreauth.Auth) string {
	if auth == nil || auth.AuthKind() == coreauth.AuthKindAPIKey {
		return ""
	}
	switch provider := strings.ToLower(strings.TrimSpace(auth.Provider)); provider {
	case "claude", "codex", "meta":
		return provider
	}
	return ""
}

// applyLiveModelList filters a login's catalog models by its live list and appends the listed
// models the catalog lacks. Without the option, for other logins, or before the first successful
// fetch it returns models unchanged (and asks for a fetch).
func applyLiveModelList(cfg *config.Config, auth *coreauth.Auth, models []*ModelInfo) []*ModelInfo {
	provider := liveModelListProvider(auth)
	if cfg == nil || !cfg.Models.LiveLists || cfg.Home.Enabled || provider == "" {
		return models
	}
	list, ok := liveModelLists.get(auth.ID, liveModelListIdentity(auth))
	if !ok {
		liveModelLists.request(false)
		return models
	}
	return filterByLiveModelList(provider, models, list)
}

func filterByLiveModelList(provider string, models []*ModelInfo, list liveModelList) []*ModelInfo {
	keep := liveModelListBuiltins(provider)
	out := make([]*ModelInfo, 0, len(models)+len(list.extra))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(model.ID))
		_, listed := list.ids[id]
		_, builtin := keep[id]
		if listed || builtin {
			out = append(out, model)
			seen[id] = struct{}{}
		}
	}
	for _, model := range list.extra {
		id := strings.ToLower(model.ID)
		if _, ok := seen[id]; ok {
			continue
		}
		clone := *model
		if model.Thinking != nil {
			thinking := *model.Thinking
			thinking.Levels = append([]string(nil), model.Thinking.Levels...)
			clone.Thinking = &thinking
		}
		clone.SupportedInputModalities = append([]string(nil), model.SupportedInputModalities...)
		clone.SupportedOutputModalities = append([]string(nil), model.SupportedOutputModalities...)
		out = append(out, &clone)
		seen[id] = struct{}{}
	}
	return out
}

// liveModelListBuiltins are models CPA adds itself that the provider's list never includes.
func liveModelListBuiltins(provider string) map[string]struct{} {
	keep := map[string]struct{}{}
	if provider == "codex" {
		for _, model := range registry.WithCodexBuiltins(nil) {
			if model != nil {
				keep[strings.ToLower(model.ID)] = struct{}{}
			}
		}
	}
	return keep
}

func (s *Service) liveModelListsEnabled() bool {
	if s == nil {
		return false
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg != nil && s.cfg.Models.LiveLists && !s.cfg.Home.Enabled
}

// runLiveModelListRefresh fetches due lists every scan interval and when woken, and registers
// the models of logins whose list changed.
func (s *Service) runLiveModelListRefresh(ctx context.Context) {
	registry.SetLiveModelListRefreshHook(func() { liveModelLists.request(true) })
	defer registry.SetLiveModelListRefreshHook(nil)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-liveModelLists.wake:
		case <-timer.C:
			timer.Reset(liveModelListScanInterval)
		}
		s.refreshLiveModelLists(ctx, liveModelLists.force.Swap(false))
	}
}

// refreshLiveModelLists starts a fetch for every due login (every login when forced). Fetches
// run in parallel and carry no deadline once connected, as for other upstream traffic, so a slow
// provider only delays its own logins; a login whose fetch is still running is skipped.
func (s *Service) refreshLiveModelLists(ctx context.Context, force bool) {
	if !s.liveModelListsEnabled() || s.coreManager == nil {
		return
	}
	present := make(map[string]struct{})
	for _, auth := range s.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || liveModelListProvider(auth) == "" {
			continue
		}
		present[auth.ID] = struct{}{}
		identity := liveModelListIdentity(auth)
		if auth.Disabled || (!force && !liveModelLists.due(auth.ID, identity, liveModelListNow())) {
			continue
		}
		if !liveModelLists.start(auth.ID) {
			continue
		}
		snapshot := auth.Clone()
		go func() {
			defer liveModelLists.finish(snapshot.ID)
			s.fetchAndRegisterLiveModelList(ctx, snapshot, identity)
		}()
	}
	liveModelLists.forget(present)
}

func (s *Service) fetchAndRegisterLiveModelList(ctx context.Context, snapshot *coreauth.Auth, identity string) {
	list, err := s.fetchLiveModelList(ctx, snapshot)
	if ctx.Err() != nil {
		return
	}
	// The login may have been replaced by another account while the request ran.
	current, ok := s.coreManager.GetByID(snapshot.ID)
	if !ok || current == nil || liveModelListIdentity(current) != identity {
		return
	}
	if err != nil {
		liveModelLists.fail(snapshot.ID, liveModelListNow())
		log.Warnf("live model list for %s login %s: %v (keeping the previous list)", snapshot.Provider, snapshot.ID, err)
		return
	}
	list.identity = identity
	if liveModelLists.put(snapshot.ID, list) && !current.Disabled {
		s.refreshModelRegistrationForAuth(current)
		log.Infof("live model list for %s login %s: %d models", snapshot.Provider, snapshot.ID, len(list.ids))
	}
}

func (s *Service) fetchLiveModelList(ctx context.Context, auth *coreauth.Auth) (liveModelList, error) {
	provider := liveModelListProvider(auth)
	executor, ok := s.coreManager.Executor(provider)
	if !ok || executor == nil {
		return liveModelList{}, fmt.Errorf("no %s executor", provider)
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	get := func(rawURL string, header http.Header) ([]byte, error) {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if errReq != nil {
			return nil, errReq
		}
		for key, values := range header {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		resp, errDo := executor.HttpRequest(ctx, auth, req)
		if errDo != nil {
			return nil, errDo
		}
		defer func() { _ = resp.Body.Close() }()
		body, errRead := io.ReadAll(io.LimitReader(resp.Body, liveModelListMaxBody))
		if errRead != nil {
			return nil, errRead
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, req.URL.Path)
		}
		return body, nil
	}

	switch provider {
	case "claude":
		base := liveModelListBaseURL(auth, "https://api.anthropic.com")
		header := http.Header{}
		if probe, errProbe := http.NewRequest(http.MethodGet, base, nil); errProbe == nil {
			helps.ApplyClaudeDefaultDeviceProfileHeaders(probe, cfg)
			header = probe.Header.Clone()
		}
		header.Set("Anthropic-Version", "2023-06-01")
		header.Set("Anthropic-Beta", "oauth-2025-04-20")
		pages := make([][]byte, 0, 1)
		after := ""
		for page := 0; page < liveModelListMaxPages; page++ {
			query := url.Values{"limit": {"1000"}}
			if after != "" {
				query.Set("after_id", after)
			}
			body, err := get(base+"/v1/models?"+query.Encode(), header)
			if err != nil {
				return liveModelList{}, err
			}
			pages = append(pages, body)
			root := ojson.ParseBytes(body)
			after = strings.TrimSpace(root.Get("last_id").String())
			if !root.Get("has_more").Bool() || after == "" {
				break
			}
		}
		return parseClaudeLiveModelList(pages, liveModelListNow())
	case "codex":
		// The account list belongs to the outer ChatGPT login: mark the request as a Codex client
		// one so a use_siwc login still uses its outer token, and name the workspace.
		ctx = coreauth.WithCodexClientRequest(ctx, true)
		base := liveModelListBaseURL(auth, "https://chatgpt.com/backend-api/codex")
		header := http.Header{}
		header.Set("Originator", codexLiveModelListOrigin)
		if cfg != nil && strings.TrimSpace(cfg.CodexHeaderDefaults.UserAgent) != "" {
			header.Set("User-Agent", strings.TrimSpace(cfg.CodexHeaderDefaults.UserAgent))
		}
		if accountID, _ := auth.Metadata["account_id"].(string); strings.TrimSpace(accountID) != "" {
			header.Set("Chatgpt-Account-Id", strings.TrimSpace(accountID))
		}
		body, err := get(base+"/models?client_version="+codexLiveModelListClientVersion, header)
		if err != nil {
			return liveModelList{}, err
		}
		return parseCodexLiveModelList(body, liveModelListNow())
	case "meta":
		body, err := get(liveModelListBaseURL(auth, metaauth.DefaultAPIBaseURL)+"/models", nil)
		if err != nil {
			return liveModelList{}, err
		}
		return parseMetaLiveModelList(body, liveModelListNow())
	}
	return liveModelList{}, fmt.Errorf("unsupported provider %q", provider)
}

func liveModelListBaseURL(auth *coreauth.Auth, fallback string) string {
	if auth != nil {
		if base := strings.TrimSpace(auth.Attributes["base_url"]); base != "" {
			return strings.TrimRight(base, "/")
		}
		if base, _ := auth.Metadata["base_url"].(string); strings.TrimSpace(base) != "" {
			return strings.TrimRight(strings.TrimSpace(base), "/")
		}
	}
	return fallback
}

// liveModelListLevels keeps the efforts CPA knows, in its order.
func liveModelListLevels(supported func(level string) bool) []string {
	var levels []string
	for _, level := range []string{"minimal", "low", "medium", "high", "xhigh", "max"} {
		if supported(level) {
			levels = append(levels, level)
		}
	}
	return levels
}

func errEmptyLiveModelList(provider string) error {
	return fmt.Errorf("%s returned no models", provider)
}

// parseClaudeLiveModelList reads GET /v1/models pages: ids plus each model's window, output
// limit, efforts and input modalities.
func parseClaudeLiveModelList(pages [][]byte, now time.Time) (liveModelList, error) {
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for _, body := range pages {
		if !ojson.ValidBytes(body) {
			return liveModelList{}, fmt.Errorf("claude model list: invalid JSON")
		}
		for _, item := range ojson.ParseBytes(body).Get("data").Array() {
			id := strings.TrimSpace(item.Get("id").String())
			if id == "" {
				continue
			}
			list.ids[strings.ToLower(id)] = struct{}{}
			caps := item.Get("capabilities")
			model := &ModelInfo{ID: id, Object: "model", OwnedBy: "anthropic", Type: "claude",
				DisplayName: item.Get("display_name").String(), ContextLength: int(item.Get("max_input_tokens").Int()),
				MaxCompletionTokens: int(item.Get("max_tokens").Int()), SupportedInputModalities: []string{"text"},
				SupportedOutputModalities: []string{"text"}}
			if created, err := time.Parse(time.RFC3339, item.Get("created_at").String()); err == nil {
				model.Created = created.Unix()
			}
			if caps.Get("image_input.supported").Bool() {
				model.SupportedInputModalities = append(model.SupportedInputModalities, "image")
			}
			levels := liveModelListLevels(func(level string) bool {
				return caps.Get("effort." + level + ".supported").Bool()
			})
			if len(levels) > 0 {
				model.Thinking = &registry.ThinkingSupport{Levels: levels,
					ZeroAllowed:    caps.Get("thinking.types.disabled.supported").Bool(),
					DynamicAllowed: caps.Get("thinking.types.adaptive.supported").Bool()}
			}
			list.extra = append(list.extra, model)
		}
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("claude")
	}
	return list, nil
}

// parseCodexLiveModelList reads GET /backend-api/codex/models: every slug counts as available,
// and the visible ones (Codex hides fallbacks such as gpt-reserve) may be added.
func parseCodexLiveModelList(body []byte, now time.Time) (liveModelList, error) {
	if !ojson.ValidBytes(body) {
		return liveModelList{}, fmt.Errorf("codex model list: invalid JSON")
	}
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for _, item := range ojson.ParseBytes(body).Get("models").Array() {
		id := strings.TrimSpace(item.Get("slug").String())
		if id == "" {
			continue
		}
		list.ids[strings.ToLower(id)] = struct{}{}
		if strings.EqualFold(item.Get("visibility").String(), "hide") {
			continue
		}
		window := int(item.Get("max_context_window").Int())
		if window <= 0 {
			window = int(item.Get("context_window").Int())
		}
		model := &ModelInfo{ID: id, Object: "model", OwnedBy: "openai", Type: "openai",
			DisplayName: item.Get("display_name").String(), Description: item.Get("description").String(),
			ContextLength: window, SupportedOutputModalities: []string{"text"}}
		for _, modality := range item.Get("input_modalities").Array() {
			if value := strings.TrimSpace(modality.String()); value != "" {
				model.SupportedInputModalities = append(model.SupportedInputModalities, value)
			}
		}
		if len(model.SupportedInputModalities) == 0 {
			model.SupportedInputModalities = []string{"text"}
		}
		efforts := item.Get("supported_reasoning_levels").Array()
		levels := liveModelListLevels(func(level string) bool {
			for _, effort := range efforts {
				if strings.EqualFold(effort.Get("effort").String(), level) {
					return true
				}
			}
			return false
		})
		if len(levels) > 0 {
			model.Thinking = &registry.ThinkingSupport{Levels: levels}
		}
		list.extra = append(list.extra, model)
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("codex")
	}
	return list, nil
}

// parseMetaLiveModelList reads GET /v1/models, which has ids only: Meta logins are filtered, and
// nothing is added.
func parseMetaLiveModelList(body []byte, now time.Time) (liveModelList, error) {
	if !ojson.ValidBytes(body) {
		return liveModelList{}, fmt.Errorf("meta model list: invalid JSON")
	}
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for _, item := range ojson.ParseBytes(body).Get("data").Array() {
		if id := strings.TrimSpace(item.Get("id").String()); id != "" {
			list.ids[strings.ToLower(id)] = struct{}{}
		}
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("meta")
	}
	return list, nil
}
