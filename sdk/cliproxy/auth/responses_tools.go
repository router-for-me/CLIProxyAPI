package auth

import (
	"bytes"
	"context"
	"strings"
	"sync"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/responsestools"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// WireContract aliases the shared executor wire contract for the final send
// guard. The attempt and the original client request never enter Metadata:
// only this narrow view crosses the executor boundary.
type WireContract = cliproxyexecutor.WireContract

// responsesToolsState owns the Manager-held limiter and the compiled policy
// snapshot. The limiter is long-lived: config updates replace limits without
// resetting already-counted usage.
type responsesToolsState struct {
	mu      sync.Mutex
	limiter *responsestools.Limiter
	policy  responsestools.Policy
	enabled bool
}

// syncResponsesToolsPolicyFromConfig compiles the already-cloned runtime config
// snapshot into the immutable policy and refreshes limiter quotas without
// resetting already-counted usage.
func (m *Manager) syncResponsesToolsPolicyFromConfig(cfg *internalconfig.Config) {
	if m == nil || cfg == nil {
		return
	}
	policy, err := cfg.ResponsesTools.Compile()
	if err != nil {
		return
	}
	m.syncResponsesToolsPolicy(policy)
}

// responsesToolsSnapshot returns the Manager-owned state for this
// configuration generation, creating the limiter on first use.
func (m *Manager) responsesToolsSnapshot() *responsesToolsState {
	if m == nil {
		return nil
	}
	if state, ok := m.responsesTools.Load("state"); ok {
		if result, okResult := state.(*responsesToolsState); okResult && result != nil {
			return result
		}
	}
	state := &responsesToolsState{limiter: responsestools.NewLimiter(responsestools.DefaultLimits())}
	actual, _ := m.responsesTools.LoadOrStore("state", state)
	result, _ := actual.(*responsesToolsState)
	if result == nil {
		return state
	}
	return result
}

// syncResponsesToolsPolicy compiles the runtime config snapshot into the
// immutable policy and refreshes limiter quotas without resetting usage.
// Invalid reconfigurations keep the previous policy: a partial route table
// is never published before budget validation completes.
func (m *Manager) syncResponsesToolsPolicy(cfgPolicy responsestools.Policy) {
	if m == nil {
		return
	}
	state := m.responsesToolsSnapshot()
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.policy = cfgPolicy
	state.enabled = cfgPolicy.Enabled && len(cfgPolicy.Routes) > 0
	if state.limiter == nil {
		state.limiter = responsestools.NewLimiter(cfgPolicy.Limits)
	} else {
		state.limiter.UpdateLimits(cfgPolicy.Limits)
	}
}

// responsesToolsRoute builds the actual routed call identity from the
// selected credential and executor format. Only non-sensitive endpoint
// metadata is read; keys and tokens never enter the policy path.
func responsesToolsRoute(auth *Auth, provider string, upstreamModel string, toFormat sdktranslator.Format) responsestools.Route {
	route := responsestools.Route{
		Provider:       strings.TrimSpace(provider),
		UpstreamModel:  strings.TrimSpace(upstreamModel),
		UpstreamFormat: strings.TrimSpace(toFormat.String()),
	}
	if auth != nil {
		route.AuthKind = auth.AuthKind()
		if auth.Attributes != nil {
			route.BaseURL = strings.TrimSpace(auth.Attributes["base_url"])
		}
	}
	return route
}

// isResponsesFamily reports whether both the client input and the downstream
// target belong to the registered Responses family. Anything else (or
// unknown) never enters tool-contract parsing, even when a route matches.
func isResponsesFamily(source, target sdktranslator.Format) bool {
	return isResponsesFormatValue(source) && isResponsesFormatValue(target)
}

func isResponsesFormatValue(format sdktranslator.Format) bool {
	switch sdktranslator.Format(strings.ToLower(strings.TrimSpace(format.String()))) {
	case sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex:
		return true
	default:
		return false
	}
}

// prepareResponsesToolsAttempt parses one client request against the
// effective route policy and returns the normalized wire body. It is pure
// computation: no network or tool execution starts here. Unmatched or
// untouched requests return the original payload with a nil attempt and no
// state reservation.
func (m *Manager) prepareResponsesToolsAttempt(route responsestools.Route, payload []byte) (outPayload []byte, attempt *responsestools.Attempt, wire *WireContract, err error) {
	outPayload = payload
	if m == nil || len(payload) == 0 {
		return outPayload, nil, nil, nil
	}
	state := m.responsesToolsSnapshot()
	if state == nil {
		return outPayload, nil, nil, nil
	}
	state.mu.Lock()
	policy := state.policy
	enabled := state.enabled
	limiter := state.limiter
	state.mu.Unlock()
	if !enabled || limiter == nil {
		return outPayload, nil, nil, nil
	}
	routePolicy, matched, errMatch := responsestools.ResolvePolicy(policy, route)
	if errMatch != nil || !matched {
		return outPayload, nil, nil, errMatch
	}
	prepared, errPrepare := responsestools.Prepare(payload, routePolicy, policy.Limits, limiter)
	if errPrepare != nil {
		return outPayload, nil, nil, errPrepare
	}
	if prepared.Attempt == nil {
		return outPayload, nil, nil, nil
	}
	guard := &WireContract{ActiveToolBytes: responsestools.ActiveToolArraysBytesOf(prepared.Body)}
	collectResponsesToolsAliases(prepared.Attempt, guard)
	return prepared.Body, prepared.Attempt, guard, nil
}

// withResponsesToolsContract attaches the read-only wire contract for the
// final send guard.
func withResponsesToolsContract(ctx context.Context, wire *WireContract) context.Context {
	return cliproxyexecutor.WithWireContract(ctx, wire)
}

// responsesToolsContractFromContext reads the guard contract back.
func responsesToolsContractFromContext(ctx context.Context) *WireContract {
	return cliproxyexecutor.WireContractFromContext(ctx)
}

// markResponsesToolsNeutral records a bridge result as availability-neutral:
// it neither penalizes the credential nor refreshes quota, while preserving
// real upstream usage accounting done elsewhere.
func (m *Manager) markResponsesToolsNeutral(ctx context.Context, result Result) {
	if m == nil {
		return
	}
	result.Success = false
	m.recordAvailabilityNeutralResult(ctx, result)
}

// isResponsesToolsError reports whether err is a core bridge failure that
// must take the request-scoped stop branch regardless of user-configured
// error regex rules.
func isResponsesToolsError(err error) bool {
	return responsestools.IsRequestScopedError(err)
}

// normalizeAuthKindForPolicy maps internal auth kinds to policy spellings.
func normalizeAuthKindForPolicy(kind string) string {
	trimmed := strings.ToLower(strings.TrimSpace(kind))
	trimmed = strings.ReplaceAll(trimmed, "-", "")
	trimmed = strings.ReplaceAll(trimmed, "_", "")
	switch trimmed {
	case "apikey", "api-key", "key":
		return "api-key"
	case "oauth":
		return "oauth"
	default:
		return strings.TrimSpace(kind)
	}
}

// responsesToolsPayload selects the client contract bytes for adaptation:
// the normalized original request when present, else the executor payload.
func responsesToolsPayload(execReq cliproxyexecutor.Request, execOpts cliproxyexecutor.Options) []byte {
	if len(execOpts.OriginalRequest) > 0 {
		return bytes.Clone(execOpts.OriginalRequest)
	}
	return bytes.Clone(execReq.Payload)
}

// collectResponsesToolsAliases fills the guard alias lists from the attempt
// contract so the outbound guard can verify bridged tools survive post rules.
func collectResponsesToolsAliases(attempt *responsestools.Attempt, wire *WireContract) {
	if attempt == nil || wire == nil {
		return
	}
	wire.SearchAliases, wire.CustomAliases = attempt.WireAliases()
}
