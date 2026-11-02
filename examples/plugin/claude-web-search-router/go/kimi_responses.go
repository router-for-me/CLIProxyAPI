package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const (
	// kimiResponsesCodingModel is the registry name KimiExecutor normalizes to
	// kimi-for-coding on coding-plan endpoints (api.kimi.com/coding, api.kimi.ai/coding).
	kimiResponsesCodingModel = "kimi-k2.8-code"
	// kimiResponsesPlatformModel is the default for Moonshot Open Platform credentials.
	kimiResponsesPlatformModel = "kimi-k3"
)

// kimiResponsesTarget is the resolved host execution target for the kimi_responses
// backend: a registry model plus the credential ID and provider to pin when known.
// provider carries the host-canonical scheduling key ("kimi" or "kimi-ai") of the
// pinned credential, because the host selects auths within the forced provider
// bucket and rejects a pinned ID whose provider does not match.
type kimiResponsesTarget struct {
	model    string
	authID   string
	provider string
}

// canonicalKimiSchedulingProvider maps a kimi-family provider key to the
// canonical scheduling name the host uses in auth selection: kimi.com
// credentials schedule under "kimi", kimi.ai credentials under "kimi-ai".
func canonicalKimiSchedulingProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "kimi.ai", "kimi-ai":
		return "kimi-ai"
	default:
		return "kimi"
	}
}

// isKimiProviderEntry reports whether an auth entry belongs to the kimi provider family.
func isKimiProviderEntry(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		return true
	}
	return false
}

// kimiResponsesModelForBaseURL maps a credential base URL to the registry model whose
// normalized upstream name exists on that endpoint family.
func kimiResponsesModelForBaseURL(baseURL string) string {
	if strings.Contains(strings.ToLower(strings.TrimSpace(baseURL)), "moonshot") {
		return kimiResponsesPlatformModel
	}
	return kimiResponsesCodingModel
}

// kimiResponsesCredentialCounter rotates the pinned credential among the
// compatible entries so one ID does not permanently capture the backend when
// the host has several.
var kimiResponsesCredentialCounter atomic.Uint64

// kimiResponsesModelFamily buckets a configured kimi_responses_model into the
// credential family that can serve it. It accepts the same alias set the host
// upstream normalizer recognizes (internal/runtime/executor/kimi_executor.go),
// including CLIProxyAPI "kimi-" prefixes, "[1m]" context suffixes, and thinking
// suffixes such as "(1024)" — stripped in the host's order (thinking suffix
// first, then "[1m]") so combined forms classify too. The second return value
// is false for models whose family is unknown; those must not pin a
// possibly-incompatible credential.
func kimiResponsesModelFamily(model string) (string, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.Index(m, "("); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	m = strings.TrimSuffix(m, "[1m]")
	switch m {
	case "kimi-k2.8", "k2.8",
		"kimi-k2.8-code", "k2.8-code",
		"kimi-k2.8-preview", "k2.8-preview",
		"kimi-k2.7-code", "k2.7-code",
		"kimi-for-coding", "for-coding",
		"kimi-k2.7-code-highspeed", "k2.7-code-highspeed",
		"kimi-for-coding-highspeed", "for-coding-highspeed":
		return "coding", true
	}
	// K2 and K3 registered platform models (internal/registry/models/models.json);
	// the host normalizer accepts them via generic prefix stripping.
	switch {
	case m == "k2" || m == "kimi-k2" ||
		m == "k2-thinking" || m == "kimi-k2-thinking" ||
		m == "k2.5" || m == "kimi-k2.5" ||
		m == "k2.6" || m == "kimi-k2.6":
		return "platform", true
	case m == "k3" || m == "kimi-k3" || strings.HasPrefix(m, "k3-") || strings.HasPrefix(m, "kimi-k3-"):
		return "platform", true
	}
	return "", false
}

// highestPriorityEntries narrows a credential pool to its highest configured
// priority tier, mirroring the host auth selector (sdk/cliproxy/auth/selector.go)
// so rotation never pins a lower-priority backup while a higher-priority
// credential is available.
func highestPriorityEntries(entries []*pluginapi.HostAuthFileEntry) []*pluginapi.HostAuthFileEntry {
	if len(entries) <= 1 {
		return entries
	}
	best := entries[0].Priority
	for _, entry := range entries[1:] {
		if entry.Priority > best {
			best = entry.Priority
		}
	}
	out := make([]*pluginapi.HostAuthFileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Priority == best {
			out = append(out, entry)
		}
	}
	return out
}

// kimiCredentialPoolEntries splits kimi-family entries into coding-plan and
// Moonshot platform credentials, skipping disabled and unavailable ones.
func kimiCredentialPoolEntries(entries []pluginapi.HostAuthFileEntry) (coding, platform []*pluginapi.HostAuthFileEntry) {
	for i := range entries {
		entry := &entries[i]
		if !isKimiProviderEntry(entry.Provider) || entry.Disabled || entry.Unavailable {
			continue
		}
		if kimiResponsesModelForBaseURL(entry.BaseURL) == kimiResponsesCodingModel {
			coding = append(coding, entry)
		} else {
			platform = append(platform, entry)
		}
	}
	return coding, platform
}

// groupPriorityTiers buckets entries into descending priority tiers (highest
// first), preserving input order within a tier.
func groupPriorityTiers(entries []*pluginapi.HostAuthFileEntry) [][]*pluginapi.HostAuthFileEntry {
	if len(entries) == 0 {
		return nil
	}
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(a, b *pluginapi.HostAuthFileEntry) int {
		return b.Priority - a.Priority
	})
	var tiers [][]*pluginapi.HostAuthFileEntry
	for _, entry := range sorted {
		if n := len(tiers); n > 0 && tiers[n-1][0].Priority == entry.Priority {
			tiers[n-1] = append(tiers[n-1], entry)
		} else {
			tiers = append(tiers, []*pluginapi.HostAuthFileEntry{entry})
		}
	}
	return tiers
}

// kimiCredentialPools narrows each family pool to its highest priority tier,
// mirroring the host auth selector's per-request narrowing.
func kimiCredentialPools(entries []pluginapi.HostAuthFileEntry) (coding, platform []*pluginapi.HostAuthFileEntry) {
	coding, platform = kimiCredentialPoolEntries(entries)
	return highestPriorityEntries(coding), highestPriorityEntries(platform)
}

// resolveKimiResponsesTargetForModel picks the credential and model for the
// host-routed kimi Responses backend, consuming one rotation tick. Coding-plan
// credentials are preferred over Moonshot platform keys unless a model
// override names a platform model. When the override names a model no
// credential family can serve (or no compatible credential exists), the
// second return value is false so the caller can fall back to an unpinned
// override.
func resolveKimiResponsesTargetForModel(entries []pluginapi.HostAuthFileEntry, modelOverride string) (kimiResponsesTarget, bool) {
	return resolveKimiResponsesTargetAt(entries, modelOverride, func() uint64 {
		return kimiResponsesCredentialCounter.Add(1)
	})
}

// peekKimiResponsesTargetForModel resolves the target without consuming a
// rotation tick; availability probes use it so probing does not skew which
// credential the actual execution pins.
func peekKimiResponsesTargetForModel(entries []pluginapi.HostAuthFileEntry, modelOverride string) (kimiResponsesTarget, bool) {
	return resolveKimiResponsesTargetAt(entries, modelOverride, func() uint64 {
		return kimiResponsesCredentialCounter.Load() + 1
	})
}

// resolveKimiResponsesFamily picks the credential family compatible with the
// model override: coding-plan credentials by default (platform keys when no
// coding credential exists), the platform family for a platform model
// override. It returns nil when the override names a model no credential
// family can serve, or when no compatible credential exists.
func resolveKimiResponsesFamily(entries []pluginapi.HostAuthFileEntry, modelOverride string) []*pluginapi.HostAuthFileEntry {
	coding, platform := kimiCredentialPoolEntries(entries)
	family := coding
	if modelOverride != "" {
		fam, known := kimiResponsesModelFamily(modelOverride)
		if !known {
			return nil
		}
		if fam == "platform" {
			family = platform
		}
	} else if len(family) == 0 {
		family = platform
	}
	if len(family) == 0 {
		return nil
	}
	return family
}

// resolveKimiResponsesTargetsAt builds every compatible target in execution
// order: the highest priority tier first, rotated within the tier (one
// rotation tick consumed, so the first element is the target a single-target
// resolve would have picked), then lower priority tiers descending — so a
// retryable top-tier failure descends to the configured backup credentials,
// mirroring the host selector's highest-*currently-available*-tier semantics
// (sdk/cliproxy/auth/selector.go).
func resolveKimiResponsesTargetsAt(entries []pluginapi.HostAuthFileEntry, modelOverride string, nextCounter func() uint64) []kimiResponsesTarget {
	family := resolveKimiResponsesFamily(entries, modelOverride)
	if len(family) == 0 {
		return nil
	}
	tiers := groupPriorityTiers(family)
	n := nextCounter()
	if n == 0 {
		n = 1
	}
	primary := tiers[0]
	start := int(n-1) % len(primary)
	ordered := make([]*pluginapi.HostAuthFileEntry, 0, len(family))
	for i := range primary {
		ordered = append(ordered, primary[(start+i)%len(primary)])
	}
	for _, tier := range tiers[1:] {
		ordered = append(ordered, tier...)
	}
	targets := make([]kimiResponsesTarget, 0, len(ordered))
	for _, entry := range ordered {
		model := kimiResponsesModelForBaseURL(entry.BaseURL)
		if modelOverride != "" {
			model = modelOverride
		}
		targets = append(targets, kimiResponsesTarget{
			model:    model,
			authID:   strings.TrimSpace(entry.ID),
			provider: canonicalKimiSchedulingProvider(entry.Provider),
		})
	}
	return targets
}

func resolveKimiResponsesTargetAt(entries []pluginapi.HostAuthFileEntry, modelOverride string, nextCounter func() uint64) (kimiResponsesTarget, bool) {
	targets := resolveKimiResponsesTargetsAt(entries, modelOverride, nextCounter)
	if len(targets) == 0 {
		return kimiResponsesTarget{}, false
	}
	return targets[0], true
}

// resolveKimiResponsesTargetFromEntries picks the credential and model with no
// override: coding-plan credentials preferred, rotated among compatible ones.
func resolveKimiResponsesTargetFromEntries(entries []pluginapi.HostAuthFileEntry) (kimiResponsesTarget, bool) {
	return resolveKimiResponsesTargetForModel(entries, "")
}

// resolveKimiResponsesTarget resolves the backend target, consuming one
// rotation tick. kimi_responses_model wins for the model; when it names a
// known family a matching credential is pinned, and an unknown model degrades
// to an unpinned override. Otherwise host.auth.list supplies metadata only
// (never secrets). When the list call fails (older host), the coding default
// is tried unpinned so the request still degrades gracefully through the
// fallback chain.
func resolveKimiResponsesTarget(cfg pluginConfig) (kimiResponsesTarget, bool) {
	return resolveKimiResponsesTargetWith(cfg, false)
}

// peekKimiResponsesTarget resolves the backend target without consuming a
// rotation tick; availability probes use it so probing does not skew which
// credential the actual execution pins.
func peekKimiResponsesTarget(cfg pluginConfig) (kimiResponsesTarget, bool) {
	return resolveKimiResponsesTargetWith(cfg, true)
}

// resolveKimiResponsesTargets resolves every compatible execution target in
// rotation order, consuming one rotation tick. kimi_responses_model wins for
// the model; when it names a known family the matching pool is enumerated,
// and an unknown model degrades to a single unpinned override. Otherwise
// host.auth.list supplies metadata only (never secrets). When the list call
// fails (older host), the coding default degrades to a single unpinned target
// so the request still executes gracefully through the fallback chain.
func resolveKimiResponsesTargets(cfg pluginConfig) []kimiResponsesTarget {
	return resolveKimiResponsesTargetsWith(cfg, false)
}

// peekKimiResponsesTargets resolves the targets without consuming a rotation
// tick; availability probes use it so probing does not skew which credential
// the actual execution pins.
func peekKimiResponsesTargets(cfg pluginConfig) []kimiResponsesTarget {
	return resolveKimiResponsesTargetsWith(cfg, true)
}

func resolveKimiResponsesTargetsWith(cfg pluginConfig, peek bool) []kimiResponsesTarget {
	next := func() uint64 {
		return kimiResponsesCredentialCounter.Add(1)
	}
	if peek {
		next = func() uint64 {
			return kimiResponsesCredentialCounter.Load() + 1
		}
	}
	entries, listed := listKimiAuthEntries()
	if m := strings.TrimSpace(cfg.KimiResponsesModel); m != "" {
		if targets := resolveKimiResponsesTargetsAt(entries, m, next); len(targets) > 0 {
			return targets
		}
		return []kimiResponsesTarget{{model: m, provider: "kimi"}}
	}
	if targets := resolveKimiResponsesTargetsAt(entries, "", next); len(targets) > 0 {
		return targets
	}
	if !listed {
		// host.auth.list unavailable: keep the unpinned coding default so an
		// explicit route accepted from AvailableProviders still executes.
		return []kimiResponsesTarget{{model: kimiResponsesCodingModel, provider: "kimi"}}
	}
	return nil
}

func resolveKimiResponsesTargetWith(cfg pluginConfig, peek bool) (kimiResponsesTarget, bool) {
	targets := resolveKimiResponsesTargetsWith(cfg, peek)
	if len(targets) == 0 {
		return kimiResponsesTarget{}, false
	}
	return targets[0], true
}

// listKimiAuthEntries returns the host kimi auth metadata. The second return
// value is false when the host.auth.list call is unavailable or fails, which
// callers distinguish from a successful listing with no usable credential.
func listKimiAuthEntries() ([]pluginapi.HostAuthFileEntry, bool) {
	raw, errCall := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, false
	}
	var resp struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return nil, false
	}
	return resp.Files, true
}

// kimiResponsesBody converts the Claude Messages request into a Kimi Responses
// payload. History is flattened to text-only input items and the only tool offered
// upstream is web_search; Claude-side tool options stay with the kimi_web_search
// backend where they map to the standalone search APIs.
func kimiResponsesBody(claudeBody []byte, model string, stream bool) ([]byte, error) {
	input := make([]map[string]any, 0, 8)
	for _, message := range gjson.GetBytes(claudeBody, "messages").Array() {
		text := strings.TrimSpace(extractClaudeMessageText(message.Get("content")))
		if text == "" {
			continue
		}
		role, contentType := "user", "input_text"
		if strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "assistant") {
			role, contentType = "assistant", "output_text"
		}
		input = append(input, map[string]any{
			"type":    "message",
			"role":    role,
			"content": []map[string]string{{"type": contentType, "text": text}},
		})
	}
	if len(input) == 0 {
		return nil, fmt.Errorf("kimi responses: claude body carries no text input")
	}
	out := map[string]any{
		"model":   model,
		"input":   input,
		"tools":   []map[string]string{{"type": "web_search"}},
		"include": []string{"web_search_call.action.sources"},
		"store":   false,
		"stream":  stream,
	}
	if instructions := strings.TrimSpace(extractClaudeMessageText(gjson.GetBytes(claudeBody, "system"))); instructions != "" {
		out["instructions"] = instructions
	}
	return json.Marshal(out)
}

// kimiBucketCandidates returns the forced-provider buckets an unpinned target
// should try, in order. At execute time the plugin cannot see which kimi
// provider families the host has — ExecutorRequest.Metadata never carries
// ModelRouteRequest.AvailableProviders
// (internal/pluginhost/adapters_executors.go buildExecutorRequest) — so both
// buckets are attempted and the route-time hint only orders them.
func kimiBucketCandidates(routeProvider string) []string {
	if canonicalKimiSchedulingProvider(routeProvider) == "kimi-ai" {
		return []string{"kimi-ai", "kimi"}
	}
	return []string{"kimi", "kimi-ai"}
}

// expandKimiUnpinnedTargets orders unpinned targets across both kimi provider
// buckets so degraded execution falls through to the other family when the
// first bucket has no usable credential (host auth_not_found) — e.g. a
// kimi-ai-only deployment no longer dies on the default "kimi" bucket. Pinned
// targets pass through unchanged: the host rejects a pinned ID whose provider
// does not match, so the credential-derived provider must stay.
func expandKimiUnpinnedTargets(targets []kimiResponsesTarget, routeProvider string) []kimiResponsesTarget {
	out := make([]kimiResponsesTarget, 0, len(targets)+1)
	for _, target := range targets {
		if target.authID != "" {
			out = append(out, target)
			continue
		}
		for _, bucket := range kimiBucketCandidates(routeProvider) {
			expanded := target
			expanded.provider = bucket
			out = append(out, expanded)
		}
	}
	return out
}

// isKimiAuthMiss reports whether the host failed to find a usable auth for
// the target (auth_not_found). Unlike other non-retryable failures this one
// is bucket- or credential-specific, so the retry loop moves on: another
// credential, or the other provider bucket for an unpinned target, may still
// serve the request.
func isKimiAuthMiss(err error) bool {
	return err != nil && strings.Contains(err.Error(), "auth_not_found")
}

// kimiRouteProviderHint returns the canonical kimi scheduling provider the
// route decision can rely on ("kimi-ai" when the host listed a kimi.ai
// credential), or "" when the metadata names no kimi provider. Degraded
// unpinned execution applies it so a kimi-ai-accepted route is not forced
// into the "kimi" bucket.
func kimiRouteProviderHint(providers []string) string {
	for _, p := range providers {
		if canonicalKimiSchedulingProvider(p) == "kimi-ai" {
			return "kimi-ai"
		}
	}
	return ""
}

// hostModelExecuteKimiTarget routes the request to the host's kimi provider
// with a Responses-format body so KimiExecutor posts to {credential base}/v1/responses under
// the existing kimi auth (OAuth coding plan or Moonshot platform key), pinned
// to the given target.
func hostModelExecuteKimiTarget(ctx context.Context, hostCallbackID string, target kimiResponsesTarget, body []byte, stream bool) ([]byte, int, error) {
	reqBody, errBody := kimiResponsesBody(body, target.model, stream)
	if errBody != nil {
		return nil, 0, errBody
	}
	return hostModelExecuteRequest(ctx, hostCallbackID, pluginapi.HostModelExecutionRequest{
		EntryProtocol:  "openai-response",
		ExitProtocol:   "claude",
		Model:          target.model,
		Stream:         stream,
		Body:           reqBody,
		ForcedProvider: target.provider,
		AuthID:         target.authID,
	})
}

// kimiAttemptOutcome carries one credential attempt's result so the retry
// loop can decide whether the next credential should be tried.
type kimiAttemptOutcome struct {
	payload []byte
	status  int
	err     error
}

// runKimiCredentialAttempts executes attempt for each target in order. A
// retryable outcome moves to the next credential; a non-retryable one stops
// immediately because it applies to every credential in the pool — except an
// auth_not_found miss, which is bucket- or credential-specific. It returns
// the last outcome once the pool is exhausted.
func runKimiCredentialAttempts(targets []kimiResponsesTarget, attempt func(kimiResponsesTarget) kimiAttemptOutcome) kimiAttemptOutcome {
	var last kimiAttemptOutcome
	for _, target := range targets {
		last = attempt(target)
		if last.err == nil {
			if !isRetryableHTTPStatus(last.status) {
				return last
			}
			continue
		}
		if !isRetryableHTTPStatus(hostHTTPStatusFromError(last.err)) && !isKimiAuthMiss(last.err) {
			return last
		}
	}
	return last
}

// hostModelExecuteKimiResponses executes through the host kimi provider,
// retrying the remaining compatible credentials on retryable failures before
// giving up, so one throttled or unavailable credential does not push the
// request to the next backend while others in the pool could still serve it.
func hostModelExecuteKimiResponses(ctx context.Context, hostCallbackID string, cfg pluginConfig, body []byte, stream bool, routeProvider string) ([]byte, int, error) {
	targets := expandKimiUnpinnedTargets(resolveKimiResponsesTargets(cfg), routeProvider)
	if len(targets) == 0 {
		return nil, 0, fmt.Errorf("kimi responses: no usable kimi credential")
	}
	last := runKimiCredentialAttempts(targets, func(target kimiResponsesTarget) kimiAttemptOutcome {
		payload, status, errRun := hostModelExecuteKimiTarget(ctx, hostCallbackID, target, body, stream)
		return kimiAttemptOutcome{payload: payload, status: status, err: errRun}
	})
	if last.err == nil && !isRetryableHTTPStatus(last.status) {
		return last.payload, last.status, nil
	}
	if last.err != nil {
		return nil, last.status, last.err
	}
	return nil, last.status, fmt.Errorf("host model status %d", last.status)
}

// kimiStreamAttemptOutcome carries one streaming credential attempt's result.
type kimiStreamAttemptOutcome struct {
	status  int
	partial bool
	err     error
}

// runKimiStreamCredentialAttempts executes attempt for each target in order,
// moving to the next credential only when the failure was retryable (or an
// auth_not_found miss) and no chunk was emitted yet. Once part of a message
// is on the plugin stream, another credential cannot be tried without
// emitting a second message_start and corrupting the stream.
func runKimiStreamCredentialAttempts(targets []kimiResponsesTarget, attempt func(kimiResponsesTarget) kimiStreamAttemptOutcome) kimiStreamAttemptOutcome {
	var last kimiStreamAttemptOutcome
	for _, target := range targets {
		last = attempt(target)
		if last.err == nil {
			if !isRetryableHTTPStatus(last.status) {
				return last
			}
		} else if !isRetryableHTTPStatus(hostHTTPStatusFromError(last.err)) && !isKimiAuthMiss(last.err) {
			return last
		}
		if last.partial {
			return last
		}
	}
	return last
}

// hostModelStreamForwardKimiResponses streams through the host kimi provider,
// retrying the remaining compatible credentials on retryable failures that
// happened before any chunk was emitted.
func hostModelStreamForwardKimiResponses(ctx context.Context, hostCallbackID string, cfg pluginConfig, body []byte, pluginStreamID string, routeProvider string) (int, bool, error) {
	targets := expandKimiUnpinnedTargets(resolveKimiResponsesTargets(cfg), routeProvider)
	if len(targets) == 0 {
		return 0, false, fmt.Errorf("kimi responses: no usable kimi credential")
	}
	last := runKimiStreamCredentialAttempts(targets, func(target kimiResponsesTarget) kimiStreamAttemptOutcome {
		status, partial, errRun := hostModelStreamForwardKimiTarget(ctx, hostCallbackID, target, body, pluginStreamID)
		return kimiStreamAttemptOutcome{status: status, partial: partial, err: errRun}
	})
	if last.err == nil && !isRetryableHTTPStatus(last.status) {
		return last.status, last.partial, nil
	}
	if last.err != nil {
		return last.status, last.partial, last.err
	}
	return last.status, last.partial, fmt.Errorf("host model status %d", last.status)
}

// hostModelStreamForwardKimiTarget streams the request through the host's
// kimi provider pinned to the given target.
func hostModelStreamForwardKimiTarget(ctx context.Context, hostCallbackID string, target kimiResponsesTarget, body []byte, pluginStreamID string) (int, bool, error) {
	reqBody, errBody := kimiResponsesBody(body, target.model, true)
	if errBody != nil {
		return 0, false, errBody
	}
	return hostModelStreamForwardRequest(ctx, hostCallbackID, pluginapi.HostModelExecutionRequest{
		EntryProtocol:  "openai-response",
		ExitProtocol:   "claude",
		Model:          target.model,
		Stream:         true,
		Body:           reqBody,
		ForcedProvider: target.provider,
		AuthID:         target.authID,
	}, pluginStreamID)
}
