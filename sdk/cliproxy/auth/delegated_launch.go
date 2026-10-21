package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const launchTTL = 30 * time.Second
const launchIdleTTL = 30 * time.Minute
const launchLimit = 256
const usageURL = "https://chatgpt.com/backend-api/wham/usage"

// DelegatedLaunchExecutor is an execution-owned boundary: implementations must
// use this exact immutable auth/model, validate the final payload and never
// reselect credentials or models. Ordinary/plugin executors do not opt in by
// merely registering under the name "codex".
type DelegatedLaunchExecutor interface {
	ExecuteDelegatedLaunch(context.Context, *Auth, execution.Request, execution.Options) (execution.Response, *execution.StreamResult, error)
}

var exactLaunchModel = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

type launchEntry struct {
	request        PrepareLaunchRequest
	principal      string
	result         LaunchResult
	authID         string
	revision       [32]byte
	done           chan struct{}
	expires        time.Time
	established    bool
	revoked        bool
	active         context.CancelFunc
	prepareCancel  context.CancelFunc
	attempts       int
	refusalVersion uint64
	lastRefusal    *LaunchRefusal
}

// LaunchPool owns admission and dispatch. All state is bounded and process-local;
// a restart invalidates bindings. The consumer owns durable waiting/re-preparation.
type LaunchPool struct {
	manager  *Manager
	now      func() time.Time
	mu       sync.Mutex
	entries  map[[32]byte]*launchEntry
	bindings map[string]*launchEntry
	probes   chan struct{}
}

func NewLaunchPool(manager *Manager) *LaunchPool {
	return NewLaunchPoolWithClock(manager, time.Now)
}

// NewLaunchPoolWithClock permits deterministic embedded integration tests. The
// server uses NewLaunchPool; neither HTTP nor configuration can override time.
func NewLaunchPoolWithClock(manager *Manager, now func() time.Time) *LaunchPool {
	if now == nil {
		now = time.Now
	}
	return &LaunchPool{manager: manager, now: now, entries: make(map[[32]byte]*launchEntry), bindings: make(map[string]*launchEntry), probes: make(chan struct{}, 4)}
}

func launchRefusal(code, binding string, domains []string) *LaunchRefusal {
	scope := "unknown"
	if code == "usage_limit" {
		scope = "account"
	}
	return &LaunchRefusal{Code: code, BindingID: binding, QuotaScope: scope, QuotaDomains: append([]string{}, domains...)}
}

func unverifiedLaunch(code string) LaunchResult {
	return LaunchResult{ProtocolVersion: LaunchProtocolVersion, Status: "unverified", Code: code, QuotaScope: "unknown", QuotaDomains: []string{}}
}

func validLaunchRequest(r PrepareLaunchRequest) bool {
	if r.Provider != "codex" || !exactLaunchModel.MatchString(r.Model) || !exactLaunchModel.MatchString(r.SessionID) || r.ClientRequestID == "" || len(r.ClientRequestID) > 256 || len(r.BlockedQuotaDomains) > 64 {
		return false
	}
	for _, d := range r.BlockedQuotaDomains {
		if len(d) > 512 || d == "" {
			return false
		}
	}
	return true
}

// Prepare performs read-only bounded probes, never executor generation.
func (p *LaunchPool) Prepare(ctx context.Context, principal string, r PrepareLaunchRequest) LaunchResult {
	if principal == "" || !validLaunchRequest(r) {
		return unverifiedLaunch("invalid_prepare")
	}
	// Normalize a private copy so idempotency includes the complete blocked set.
	r.BlockedQuotaDomains = append([]string{}, r.BlockedQuotaDomains...)
	sort.Strings(r.BlockedQuotaDomains)
	requestJSON, _ := json.Marshal(r)
	key := sha256.Sum256([]byte(principal + "\x00" + r.ClientRequestID))
	p.mu.Lock()
	p.pruneLocked()
	if old := p.entries[key]; old != nil {
		oldJSON, _ := json.Marshal(old.request)
		if old.principal != principal || string(oldJSON) != string(requestJSON) {
			p.mu.Unlock()
			return unverifiedLaunch("idempotency_conflict")
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return unverifiedLaunch("prepare_cancelled")
		case <-old.done:
			p.mu.Lock()
			result := cloneLaunchResult(old.result)
			if old.revoked {
				result = unverifiedLaunch("binding_released")
			}
			p.mu.Unlock()
			return result
		}
	}
	if len(p.entries) >= launchLimit {
		p.mu.Unlock()
		return unverifiedLaunch("pool_busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	entry := &launchEntry{prepareCancel: cancel, request: r, principal: principal, done: make(chan struct{}), expires: p.now().Add(launchTTL)}
	p.entries[key] = entry
	p.mu.Unlock()
	defer cancel()
	result, authID, revision := p.prepare(ctx, r)
	p.mu.Lock()
	if entry.revoked || ctx.Err() != nil {
		result = unverifiedLaunch("prepare_cancelled")
	}
	entry.prepareCancel = nil
	entry.result, entry.authID, entry.revision = result, authID, revision
	entry.expires = p.now().Add(launchTTL)
	if result.Status == "ready" {
		p.bindings[result.BindingID] = entry
		entry.expires = result.ExpiresAt
	}
	close(entry.done)
	p.mu.Unlock()
	return cloneLaunchResult(result)
}

func cloneLaunchResult(r LaunchResult) LaunchResult {
	r.ProtocolVersion = LaunchProtocolVersion
	r.QuotaDomains = append([]string{}, r.QuotaDomains...)
	r.Windows = append([]LaunchWindow(nil), r.Windows...)
	return r
}

func (p *LaunchPool) pruneLocked() {
	for k, e := range p.entries {
		select {
		case <-e.done:
			if e.active == nil && !p.now().Before(e.expires) {
				delete(p.entries, k)
				delete(p.bindings, e.result.BindingID)
			}
		default:
		}
	}
}

func launchAccount(a *Auth) string {
	if a == nil || a.Provider != "codex" || a.Disabled || a.Status == StatusDisabled {
		return ""
	}
	// Custom endpoints, API keys, SIWC and injected credential headers cannot prove
	// that the wham account is the account used by Responses.
	for k, v := range a.Attributes {
		if v != "" && (k == "api_key" || k == "base_url" || strings.HasPrefix(k, "header:")) {
			return ""
		}
	}
	if a.Metadata["type"] != "codex" {
		return ""
	}
	if _, ok := a.Metadata["use_siwc"]; ok {
		return ""
	}
	if a.Attributes["use_siwc"] != "" {
		return ""
	}
	token, _ := a.Metadata["access_token"].(string)
	id, _ := a.Metadata["account_id"].(string)
	if strings.TrimSpace(token) == "" || strings.TrimSpace(id) == "" || len(id) > 256 {
		return ""
	}
	return id
}

func launchRevision(a *Auth) [32]byte {
	// Private security identity, not scheduler/refresh bookkeeping. CredentialVersion
	// covers manager-owned token replacements; explicit fields also guard snapshots
	// mutated during probes without going through Manager.Update.
	metadata := make(map[string]any)
	for _, key := range []string{"type", "access_token", "accessToken", "refresh_token", "refreshToken", "id_token", "idToken", "api_key", "account_id", "use_siwc", "siwc"} {
		if value, ok := a.Metadata[key]; ok {
			metadata[key] = value
		}
	}
	attributes := make(map[string]string)
	for key, value := range a.Attributes {
		if key == "api_key" || key == "base_url" || key == "use_siwc" || strings.HasPrefix(key, "header:") {
			attributes[key] = value
		}
	}
	b, _ := json.Marshal([]any{a.ID, a.Provider, a.RegistrationEpoch, a.CredentialVersion, a.Disabled, a.Status == StatusDisabled, attributes, metadata, a.ProxyURL})
	return sha256.Sum256(b)
}

func quotaDomain(id string) string { return "codex:credential:" + id }

type launchProbe struct {
	auth     *Auth
	revision [32]byte
	result   LaunchResult
}

func (p *LaunchPool) prepare(ctx context.Context, r PrepareLaunchRequest) (LaunchResult, string, [32]byte) {
	if p.manager == nil {
		return unverifiedLaunch("pool_unavailable"), "", [32]byte{}
	}
	auths := p.manager.List()
	sort.Slice(auths, func(i, j int) bool { return auths[i].ID < auths[j].ID })
	blocked := make(map[string]bool)
	for _, d := range r.BlockedQuotaDomains {
		blocked[d] = true
	}
	var candidates []*Auth
	seen := make(map[string]bool)
	for _, a := range auths {
		account := launchAccount(a)
		if account == "" || blocked[quotaDomain(account)] || seen[account] {
			continue
		}
		if !registry.GetGlobalRegistry().ClientSupportsModel(a.ID, r.Model) {
			continue
		}
		seen[account] = true
		candidates = append(candidates, a)
		if len(candidates) == 32 {
			break
		}
	}
	if len(candidates) == 0 {
		return unverifiedLaunch("no_verified_account"), "", [32]byte{}
	}
	// Independent probes run concurrently in a globally bounded pool. Saturation
	// fails closed rather than spawning unbounded goroutines or queued work.
	results := make([]launchProbe, len(candidates))
	var wg sync.WaitGroup
	for i, a := range candidates {
		select {
		case p.probes <- struct{}{}:
			wg.Add(1)
			go func(i int, a *Auth) {
				defer wg.Done()
				defer func() { <-p.probes }()
				results[i] = launchProbe{auth: a, revision: launchRevision(a), result: p.probe(ctx, a)}
			}(i, a)
		default:
			results[i] = launchProbe{auth: a, result: unverifiedLaunch("pool_busy")}
		}
	}
	wg.Wait()
	denied := LaunchResult{Status: "waiting", Code: "usage_limit", QuotaScope: "account", QuotaDomains: []string{}}
	for _, probe := range results {
		q := probe.result
		if q.Status == "ready" && ctx.Err() == nil {
			current, ok := p.manager.GetByID(probe.auth.ID)
			if !ok || launchRevision(current) != probe.revision {
				denied.Status = "unverified"
				continue
			}
			var random [24]byte
			if _, err := rand.Read(random[:]); err != nil {
				return unverifiedLaunch("binding_unavailable"), "", [32]byte{}
			}
			q.BindingID = hex.EncodeToString(random[:])
			q.Provider, q.Model, q.SessionID = r.Provider, r.Model, r.SessionID
			q.BasePath = "/v1/delegated-launch/bindings/" + q.BindingID + "/sessions/" + url.PathEscape(r.SessionID)
			return q, probe.auth.ID, probe.revision
		}
		if q.Status != "waiting" {
			denied.Status = "unverified"
			denied.Code = "capacity_unverified"
			denied.QuotaScope = "unknown"
		}
		denied.QuotaDomains = append(denied.QuotaDomains, q.QuotaDomains...)
	}
	return denied, "", [32]byte{}
}

// probe uses the existing executor's credential injection, but owns a bounded,
// no-redirect HTTP client. No manager lock is held during the read-only request.
func (p *LaunchPool) probe(ctx context.Context, a *Auth) LaunchResult {
	unknown := unverifiedLaunch("capacity_unverified")
	account := launchAccount(a)
	if account == "" {
		return unknown
	}
	unknown.QuotaDomains = []string{quotaDomain(account)}
	p.manager.mu.RLock()
	ex := p.manager.executors["codex"]
	p.manager.mu.RUnlock()
	preparer, ok := ex.(RequestPreparer)
	if !ok {
		return unknown
	}
	if _, ok := ex.(DelegatedLaunchExecutor); !ok {
		return unknown
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil || preparer.PrepareRequest(req, a) != nil {
		return unknown
	}
	req.Header.Set("Chatgpt-Account-Id", account)
	req.Header.Set("Cache-Control", "no-cache, no-store")
	transport := p.manager.roundTripperFor(a)
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return unknown
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return unknown
	}
	date, err := http.ParseTime(resp.Header.Get("Date"))
	now := p.now().UTC()
	if err != nil || now.Sub(date) > launchTTL || date.Sub(now) > launchTTL || (resp.Header.Get("Age") != "" && resp.Header.Get("Age") != "0") {
		return unknown
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || ctx.Err() != nil {
		return unknown
	}
	return decodeLaunchUsage(body, account, now)
}

func decodeLaunchUsage(body []byte, account string, now time.Time) LaunchResult {
	unknown := unverifiedLaunch("capacity_unverified")
	unknown.QuotaDomains = []string{quotaDomain(account)}
	type window struct {
		Used    *float64 `json:"used_percent"`
		Reset   *int64   `json:"reset_at"`
		Seconds *int64   `json:"limit_window_seconds"`
	}
	var raw struct {
		Account *string `json:"account_id"`
		Spend   *struct {
			Reached *bool `json:"reached"`
		} `json:"spend_control"`
		ReachedType *struct {
			Type string `json:"type"`
		} `json:"rate_limit_reached_type"`
		Additional json.RawMessage `json:"additional_rate_limits"`
		Rate       struct {
			Allowed   *bool   `json:"allowed"`
			Reached   *bool   `json:"limit_reached"`
			Primary   *window `json:"primary_window"`
			Secondary *window `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if json.Unmarshal(body, &raw) != nil || (raw.Account != nil && *raw.Account != account) || raw.Rate.Allowed == nil || raw.Rate.Reached == nil {
		return unknown
	}
	// Extra model-specific limits have no proven normalization in this protocol.
	// Ignoring them could turn a partial snapshot into a false healthy result.
	if extra := strings.TrimSpace(string(raw.Additional)); extra != "" && extra != "null" && extra != "[]" {
		return unknown
	}
	if raw.Spend != nil && raw.Spend.Reached == nil {
		return unknown
	}
	if raw.ReachedType != nil {
		switch raw.ReachedType.Type {
		case "rate_limit_reached", "workspace_owner_credits_depleted", "workspace_member_credits_depleted", "workspace_owner_usage_limit_reached", "workspace_member_usage_limit_reached":
		default:
			return unknown
		}
	}
	r := LaunchResult{ProtocolVersion: LaunchProtocolVersion, Status: "ready", QuotaDomains: []string{quotaDomain(account)}, CheckedAt: now, ExpiresAt: now.Add(launchTTL), OrdinaryUsageAllowed: *raw.Rate.Allowed}
	for i, w := range []*window{raw.Rate.Primary, raw.Rate.Secondary} {
		if w == nil {
			continue
		}
		if w.Used == nil || w.Reset == nil || w.Seconds == nil || *w.Seconds <= 0 || *w.Used < 0 || *w.Used > 100 || !time.Unix(*w.Reset, 0).After(now) {
			return unknown
		}
		id := []string{"primary", "secondary"}[i]
		r.Windows = append(r.Windows, LaunchWindow{ID: id, UsedPercent: *w.Used, ResetsAt: time.Unix(*w.Reset, 0).UTC()})
		if *w.Used >= 100 {
			r.Status = "waiting"
		}
		if reset := time.Unix(*w.Reset, 0); reset.Before(r.ExpiresAt) {
			r.ExpiresAt = reset
		}
	}
	if !r.OrdinaryUsageAllowed || *raw.Rate.Reached || raw.ReachedType != nil || (raw.Spend != nil && *raw.Spend.Reached) {
		r.Status = "waiting"
	}
	if len(r.Windows) == 0 && r.Status == "ready" {
		return unknown
	}
	if r.Status == "waiting" {
		r.Code = "usage_limit"
		r.QuotaScope = "account"
	}
	return r
}

// authorize linearizes admission under manager-read + pool locks. Auth changes
// after this point do not replace the immutable active request's credential.
func (p *LaunchPool) authorize(ctx context.Context, cancel context.CancelFunc, principal, binding, session, model string) (_ *Auth, _ ProviderExecutor, refusal *LaunchRefusal) {
	p.mu.Lock()
	p.pruneLocked()
	e := p.bindings[binding]
	if e == nil || e.revoked || e.principal != principal || principal == "" || e.request.SessionID != session || e.request.Model != model || (e.active == nil && !p.now().Before(e.expires)) {
		p.mu.Unlock()
		return nil, nil, launchRefusal("binding_invalid", binding, nil)
	}
	if e.active != nil {
		p.mu.Unlock()
		return nil, nil, launchRefusal("binding_busy", binding, nil)
	}
	if e.lastRefusal != nil {
		r := cloneLaunchRefusal(e.lastRefusal)
		p.mu.Unlock()
		return nil, nil, r
	}
	// Own the binding throughout admission, not only after its probe. This bounds
	// waiting to one caller per binding and makes release cancel queued work.
	e.active = cancel
	latch := true
	defer func() {
		if refusal != nil {
			p.mu.Lock()
			if latch && ctx.Err() == nil && !e.revoked {
				e.latchRefusal(refusal)
			}
			e.active = nil
			p.mu.Unlock()
		}
	}()
	authID, revision, domains := e.authID, e.revision, append([]string{}, e.result.QuotaDomains...)
	p.mu.Unlock()
	a, ok := p.manager.GetByID(authID)
	if !ok || launchRevision(a) != revision || launchAccount(a) == "" {
		return nil, nil, launchRefusal("binding_changed", binding, domains)
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
	select {
	case p.probes <- struct{}{}:
		waitCancel()
	case <-waitCtx.Done():
		waitCancel()
		latch = false // Local contention is not authoritative quota evidence.
		return nil, nil, launchRefusal("capacity_unverified", binding, domains)
	}
	quota := p.probe(ctx, a)
	<-p.probes
	if ctx.Err() != nil {
		return nil, nil, launchRefusal("binding_changed", binding, domains)
	}
	if quota.Status != "ready" {
		r := launchRefusal(quota.Code, binding, domains)
		r.QuotaScope = quota.QuotaScope
		return nil, nil, r
	}
	p.manager.mu.RLock()
	defer p.manager.mu.RUnlock()
	current, ex := p.manager.auths[authID], p.manager.executors["codex"]
	p.mu.Lock()
	defer p.mu.Unlock()
	if current == nil || launchRevision(current) != revision || launchAccount(current) == "" || !registry.GetGlobalRegistry().ClientSupportsModel(authID, model) || ex == nil || ctx.Err() != nil || e.revoked || p.bindings[binding] != e || !p.now().Before(e.expires) {
		return nil, nil, launchRefusal("binding_changed", binding, domains)
	}
	if e.lastRefusal != nil {
		return nil, nil, cloneLaunchRefusal(e.lastRefusal)
	}
	e.established, e.active = true, cancel
	return current.Clone(), ex, nil
}

// latchRefusal runs under the pool lock. Returning an existing refusal is not a
// new event; its acknowledgement remains stable until consumed by Resume.
func (e *launchEntry) latchRefusal(r *LaunchRefusal) {
	if e.lastRefusal == nil {
		e.refusalVersion++
		e.lastRefusal = cloneLaunchRefusal(r)
	}
}

func cloneLaunchRefusal(r *LaunchRefusal) *LaunchRefusal {
	if r == nil {
		return nil
	}
	copy := *r
	copy.QuotaDomains = append([]string{}, r.QuotaDomains...)
	return &copy
}

// Release is authenticated and idempotent, including unknown/expired bindings.
// Unknown and wrong-principal bindings disclose no state and affect no work.
func (p *LaunchPool) Release(principal, binding, session string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.bindings[binding]; e != nil && principal != "" && e.principal == principal && e.request.SessionID == session {
		p.revokeLocked(e)
	}
	p.pruneLocked()
}
func (p *LaunchPool) CancelPrepare(principal, requestID string) {
	key := sha256.Sum256([]byte(principal + "\x00" + requestID))
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[key]; e != nil && principal != "" && e.principal == principal {
		p.revokeLocked(e)
	}
	p.pruneLocked()
}
func (p *LaunchPool) revokeLocked(e *launchEntry) {
	e.revoked = true
	e.expires = p.now().Add(launchTTL)
	if e.prepareCancel != nil {
		e.prepareCancel()
	}
	if e.active != nil {
		e.active()
	}
}

// Inspect is the supported metadata channel when native transports compress errors.
func (p *LaunchPool) Inspect(principal, binding, session string) LaunchBindingState {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	e := p.bindings[binding]
	if e == nil || principal == "" || e.principal != principal || e.request.SessionID != session {
		return LaunchBindingState{ProtocolVersion: LaunchProtocolVersion, State: "invalid"}
	}
	state := "pending"
	if e.established {
		state = "established"
	}
	if e.revoked {
		state = "released"
	}
	return LaunchBindingState{ProtocolVersion: LaunchProtocolVersion, State: state, Active: e.active != nil, ExecutorAttempts: e.attempts, RefusalVersion: e.refusalVersion, Refusal: cloneLaunchRefusal(e.lastRefusal)}
}

// Touch is an explicit consumer heartbeat during long tool work. It renews
// storage activity only, never quota freshness, auth revision or execution rights.
func (p *LaunchPool) Touch(principal, binding, session string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	e := p.bindings[binding]
	if e == nil || e.revoked || !e.established || principal == "" || e.principal != principal || e.request.SessionID != session {
		return false
	}
	e.expires = p.now().Add(launchIdleTTL)
	return true
}

// Resume acknowledges a terminal refusal without changing the pin. Consumers
// must never use this as startup replay after useful work. The refusal version
// distinguishes even consecutive pre-executor denials at the same attempt count.
func (p *LaunchPool) Resume(principal, binding, session string, expectedRefusalVersion uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	e := p.bindings[binding]
	if e == nil || e.revoked || principal == "" || e.principal != principal || e.request.SessionID != session || e.active != nil || e.lastRefusal == nil || expectedRefusalVersion == 0 || e.refusalVersion != expectedRefusalVersion {
		return false
	}
	e.lastRefusal = nil
	return true
}

func (p *LaunchPool) finish(binding string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.bindings[binding]; e != nil {
		e.active = nil
		if !e.revoked {
			e.expires = p.now().Add(launchIdleTTL)
		}
	}
}

// ExecuteBound has exactly one credential/model attempt. It deliberately bypasses
// model routing, preview-model retries, Home and ordinary affinity/failover.
func (p *LaunchPool) ExecuteBound(ctx context.Context, principal, binding, session string, req execution.Request, opts execution.Options) (execution.Response, *execution.StreamResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	a, ex, refusal := p.authorize(ctx, cancel, principal, binding, session, req.Model)
	if refusal != nil {
		cancel()
		return execution.Response{}, nil, refusal
	}
	async := false
	defer func() {
		if !async {
			cancel()
			p.finish(binding)
		}
	}()
	if execution.ValidateBoundModel(execution.WithBoundModel(ctx, req.Model), req.Payload) != nil || opts.Alt != "" && opts.Alt != "responses/compact" {
		return execution.Response{}, nil, launchRefusal("model_mismatch", binding, nil)
	}
	ctx = execution.WithBoundModel(ctx, req.Model)
	if rt := p.manager.roundTripperFor(a); rt != nil {
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", rt)
	}
	opts.Metadata = map[string]any{execution.PinnedAuthMetadataKey: a.ID, execution.CanonicalSessionIDMetadataKey: session}
	opts.RequestAfterAuthInterceptor = nil
	boundExecutor, ok := ex.(DelegatedLaunchExecutor)
	if !ok {
		return execution.Response{}, nil, launchRefusal("capacity_unverified", binding, nil)
	}
	p.mu.Lock()
	p.bindings[binding].attempts++
	p.mu.Unlock()
	response, stream, err := boundExecutor.ExecuteDelegatedLaunch(ctx, a, req, opts)
	if opts.Stream {
		if err == nil && stream != nil {
			async = true
			original := stream.Chunks
			chunks := make(chan execution.StreamChunk)
			stream = &execution.StreamResult{Headers: stream.Headers, Chunks: chunks}
			go func() {
				defer close(chunks)
				defer cancel()
				defer p.finish(binding)
				for {
					select {
					case <-ctx.Done():
						return
					case chunk, ok := <-original:
						if !ok {
							return
						}
						chunk.Err = p.boundError(chunk.Err, binding, a)
						select {
						case chunks <- chunk:
						case <-ctx.Done():
							return
						}
						if chunk.Err != nil {
							return
						}
					}
				}
			}()
		}
		return execution.Response{}, stream, p.boundError(err, binding, a)
	}
	return response, nil, p.boundError(err, binding, a)
}

// boundError accepts execution-owned structured classification, never status or prose.
func (p *LaunchPool) boundError(err error, binding string, a *Auth) error {
	if errors.Is(err, execution.ErrBoundAuthChanged) {
		return launchRefusal("binding_changed", binding, nil)
	}
	if errors.Is(err, execution.ErrBoundModelChanged) {
		return launchRefusal("model_mismatch", binding, nil)
	}
	var classified interface{ DelegatedFailure() (string, string) }
	if !errors.As(err, &classified) {
		return err
	}
	code, scope := classified.DelegatedFailure()
	if code == "" {
		return err
	}
	r := launchRefusal(code, binding, nil)
	r.QuotaScope = scope
	if account := launchAccount(a); account != "" {
		r.AttemptedAccountDomain = quotaDomain(account)
	}
	if scope == "account" {
		r.QuotaDomains = []string{quotaDomain(launchAccount(a))}
	}
	p.mu.Lock()
	if e := p.bindings[binding]; e != nil {
		e.latchRefusal(r)
	}
	p.mu.Unlock()
	return r
}
