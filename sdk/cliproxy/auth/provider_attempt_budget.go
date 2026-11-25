package auth

import (
	"context"
	"net"
	"net/url"
	"strings"
)

// providerAttemptBudget tracks how many API-key credentials of one scheduling
// provider group have been attempted for a single request. The budget survives
// request-retry rounds so a group cannot be re-entered indefinitely.
type providerAttemptBudget struct {
	limit           int
	groups          map[string]string
	counts          map[string]int
	counted         map[string]struct{}
	exhausted       map[string]struct{}
	exhaustedGroups map[string]struct{}
}

type providerAttemptBudgetContextKey struct{}

func newProviderAttemptBudget(limit int) *providerAttemptBudget {
	return &providerAttemptBudget{
		limit:           limit,
		groups:          make(map[string]string),
		counts:          make(map[string]int),
		counted:         make(map[string]struct{}),
		exhausted:       make(map[string]struct{}),
		exhaustedGroups: make(map[string]struct{}),
	}
}

func withProviderAttemptBudget(ctx context.Context, budget *providerAttemptBudget) context.Context {
	if budget == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, providerAttemptBudgetContextKey{}, budget)
}

func providerAttemptBudgetFromContext(ctx context.Context) *providerAttemptBudget {
	if ctx == nil {
		return nil
	}
	budget, _ := ctx.Value(providerAttemptBudgetContextKey{}).(*providerAttemptBudget)
	return budget
}

// providerAttemptBudgetForRequest returns the request-scoped budget when the
// caller created one, or a fresh local budget for direct execution calls.
func (m *Manager) providerAttemptBudgetForRequest(ctx context.Context) *providerAttemptBudget {
	if budget := providerAttemptBudgetFromContext(ctx); budget != nil {
		return budget
	}
	if providerAttemptBudgetDisabledInContext(ctx) {
		// The request started while the cap was disabled; snapshot that decision
		// so a reload that enables it mid-request cannot apply it halfway.
		return nil
	}
	if m == nil {
		return nil
	}
	limit := m.maxRetryCredentialsPerProvider()
	if limit <= 0 {
		return nil
	}
	return newProviderAttemptBudget(limit)
}

// WithProviderAttemptBudget binds one provider-attempt budget to the request
// context. It is idempotent so callers that may execute the same HTTP request
// more than once (for example streaming bootstrap retries) reuse the budget
// instead of starting a fresh per-provider allowance.
func (m *Manager) WithProviderAttemptBudget(ctx context.Context) context.Context {
	if m == nil || providerAttemptBudgetFromContext(ctx) != nil || providerAttemptBudgetDisabledInContext(ctx) {
		return ctx
	}
	budget := m.providerAttemptBudgetForRequest(ctx)
	if budget == nil {
		return withProviderAttemptBudgetDisabled(ctx)
	}
	return withProviderAttemptBudget(ctx, budget)
}

type providerAttemptBudgetDisabledKey struct{}

// withProviderAttemptBudgetDisabled records that this request was admitted while
// the cap was disabled.
func withProviderAttemptBudgetDisabled(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, providerAttemptBudgetDisabledKey{}, true)
}

func providerAttemptBudgetDisabledInContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	disabled, _ := ctx.Value(providerAttemptBudgetDisabledKey{}).(bool)
	return disabled
}

// providerAttemptBudgetRemember records the scheduling group that served an auth
// so a delayed failure (for example a failed post-return stream after a config
// reload) still charges the group that actually made the request.
func providerAttemptBudgetRemember(budget *providerAttemptBudget, auth *Auth) {
	if budget == nil || auth == nil || auth.AuthKind() == AuthKindOAuth {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}
	if group := authSchedulingGroup(auth); group != "" {
		budget.groups[authID] = group
	}
}

// seedTriedFromProviderBudget excludes provider groups already exhausted by an
// earlier retry round of the same request.
func seedTriedFromProviderBudget(budget *providerAttemptBudget, tried map[string]struct{}) {
	if budget == nil || tried == nil {
		return
	}
	for authID := range budget.exhausted {
		if authID = strings.TrimSpace(authID); authID != "" {
			tried[authID] = struct{}{}
		}
	}
}

// RememberProviderAttemptGroup records the scheduling group of a credential
// that is about to serve the request. Local scheduling loops that forward
// themselves call this after selection so a failure that surfaces after a
// reload (credential removed or regrouped) still charges the group that
// actually made the request.
func (m *Manager) RememberProviderAttemptGroup(ctx context.Context, candidate *Auth) {
	budget := providerAttemptBudgetFromContext(ctx)
	if budget == nil || candidate == nil {
		return
	}
	providerAttemptBudgetRemember(budget, candidate)
}

// SeedProviderAttemptBudgetExclusions adds every credential that the
// request-scoped provider cap already excluded to tried. Local scheduling loops
// that iterate credentials themselves (for example the Muse Code relay) call
// this before each selection so the cap is not bypassed.
func (m *Manager) SeedProviderAttemptBudgetExclusions(ctx context.Context, tried map[string]struct{}) {
	budget := providerAttemptBudgetFromContext(ctx)
	if m == nil || budget == nil || tried == nil {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, candidate := range m.auths {
		if providerAttemptBudgetExcludes(budget, candidate) {
			tried[id] = struct{}{}
		}
	}
}

// providerAttemptBudgetExcludes reports whether a selector returned a credential
// from a group that the request-local budget already exhausted. The built-in
// selectors honor the tried map, but a plugin/custom selector may not.
func providerAttemptBudgetExcludes(budget *providerAttemptBudget, auth *Auth) bool {
	if budget == nil || auth == nil || auth.AuthKind() == AuthKindOAuth {
		return false
	}
	if _, excluded := budget.exhausted[strings.TrimSpace(auth.ID)]; excluded {
		return true
	}
	_, excluded := budget.exhaustedGroups[authSchedulingGroup(auth)]
	return excluded
}

// authSchedulingGroup returns the provider-group identity used by the
// per-provider retry cap. OpenAI-compatible providers expose a unique
// compat_name; Gemini API-key groups preserve their config group name as
// provider_name. A provider_key that differs from the auth provider is used
// next, and base URL is the fallback for provider types (for example Vertex
// compatibility keys) that share one provider key across distinct endpoints.
func authSchedulingGroup(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if compat := normalizeProviderGroupName(authAttribute(auth, "compat_name")); compat != "" {
		return auth.Provider + "|" + compat
	}
	if name := authAttribute(auth, AttributeProviderName); name != "" {
		return auth.Provider + "|" + name
	}
	if key := authAttribute(auth, AttributeProviderKey); key != "" && key != auth.Provider {
		return auth.Provider + "|" + key
	}
	baseURL := normalizeProviderGroupBaseURL(authAttribute(auth, "base_url"))
	if baseURL == "" {
		// An omitted base URL resolves to the provider endpoint at execution
		// time, so an explicit spelling of that same endpoint must share one
		// scheduling group with it.
		baseURL = providerDefaultBaseURL(auth.Provider)
	}
	if baseURL != "" {
		return auth.Provider + "|" + baseURL
	}
	return auth.Provider
}

// normalizeProviderGroupName lowercases a compatibility group name so it
// matches the runtime provider key derived by util.OpenAICompatibleProviderKey.
func normalizeProviderGroupName(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// providerDefaultBaseURL reports the endpoint an executor substitutes when the
// credential omits base-url. Providers without a single well-known endpoint
// return an empty string and are grouped by their configured value.
func providerDefaultBaseURL(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return "https://api.anthropic.com"
	case "gemini", "interactions", "gemini-interactions":
		return "https://generativelanguage.googleapis.com"
	case "meta":
		return "https://api.meta.ai/v1"
	case "xai":
		return "https://api.x.ai/v1"
	default:
		return ""
	}
}

func normalizeProviderGroupBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	// Collapse scheme-default ports so spellings of the same origin share one
	// scheduling group.
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	switch {
	case port != "":
		parsed.Host = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		parsed.Host = "[" + host + "]"
	default:
		parsed.Host = host
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/")
}

func (m *Manager) maxRetryCredentialsPerProvider() int {
	if m == nil {
		return 0
	}
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil || cfg.MaxRetryCredentialsPerProvider <= 0 {
		return 0
	}
	return cfg.MaxRetryCredentialsPerProvider
}

// noteProviderAttemptFailure records one failed credential attempt and, when
// the per-provider cap is reached, excludes every credential of that group for
// the rest of the request. OAuth credentials keep their existing failover
// behavior; API-key and keyless compatibility credentials participate.
func (m *Manager) noteProviderAttemptFailure(budget *providerAttemptBudget, auth *Auth, tried map[string]struct{}) {
	if m == nil || budget == nil || auth == nil || tried == nil {
		return
	}
	if budget.limit <= 0 || auth.AuthKind() == AuthKindOAuth {
		return
	}
	group := authSchedulingGroup(auth)
	if group == "" {
		return
	}
	providerAttemptBudgetRemember(budget, auth)
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}
	if _, seen := budget.counted[authID]; seen {
		return
	}
	budget.counted[authID] = struct{}{}
	budget.counts[group]++
	if budget.counts[group] < budget.limit {
		return
	}
	m.exhaustProviderGroup(budget, group, tried)
}

// ProviderAttemptBudgetExhausted reports whether the request-scoped provider
// budget has already excluded at least one provider group. Callers use it to
// tell "no credential left because of the per-provider cap" apart from a
// genuine credential shortage, so the real upstream error can be surfaced.
func (m *Manager) ProviderAttemptBudgetExhausted(ctx context.Context) bool {
	budget := providerAttemptBudgetFromContext(ctx)
	if budget == nil {
		return false
	}
	return len(budget.exhaustedGroups) > 0
}

// NoteProviderAttemptFailure charges one failed streaming attempt to the
// request budget. It is used when a stream was already returned to the caller
// and fails before delivering a chunk, so the normal execution loop cannot
// observe the failure. Credential-scoped plugin refusals stay out of the
// upstream-attempt cap.
func (m *Manager) NoteProviderAttemptFailure(ctx context.Context, authID string, err error) {
	if m == nil || isCredentialTerminatedError(err) {
		return
	}
	budget := providerAttemptBudgetFromContext(ctx)
	if budget == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if group := budget.groups[authID]; group != "" {
		// The group recorded when the attempt was published is authoritative:
		// a reload may have removed the credential or moved it to another group
		// while the stream was in flight.
		m.noteProviderGroupFailure(budget, authID, group, make(map[string]struct{}))
		return
	}
	m.mu.RLock()
	auth := m.auths[authID]
	m.mu.RUnlock()
	if auth == nil {
		return
	}
	m.noteProviderAttemptFailure(budget, auth, make(map[string]struct{}))
}

// noteProviderGroupFailure charges a credential to an already-known group.
func (m *Manager) noteProviderGroupFailure(budget *providerAttemptBudget, authID, group string, tried map[string]struct{}) {
	if m == nil || budget == nil || tried == nil || group == "" {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if _, seen := budget.counted[authID]; seen {
		return
	}
	budget.counted[authID] = struct{}{}
	budget.counts[group]++
	if budget.counts[group] < budget.limit {
		return
	}
	m.exhaustProviderGroup(budget, group, tried)
}

func (m *Manager) exhaustProviderGroup(budget *providerAttemptBudget, group string, tried map[string]struct{}) {
	if m == nil || budget == nil || group == "" || tried == nil {
		return
	}
	budget.exhaustedGroups[group] = struct{}{}
	m.mu.RLock()
	authIDs := make([]string, 0, 4)
	for authID, auth := range m.auths {
		if auth == nil || auth.AuthKind() == AuthKindOAuth || authSchedulingGroup(auth) != group {
			continue
		}
		authIDs = append(authIDs, authID)
	}
	m.mu.RUnlock()
	for _, authID := range authIDs {
		if authID = strings.TrimSpace(authID); authID == "" {
			continue
		}
		budget.exhausted[authID] = struct{}{}
		tried[authID] = struct{}{}
	}
}
