package auth

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// quotaRecoveryBinding belongs to one observed quota cause, not the composed
// scheduler deadline. Names stay correlated across SDK restoration and routing.
type quotaRecoveryBinding struct {
	Selection string    `json:"selection"`
	Execution string    `json:"execution"`
	Requested string    `json:"requested"`
	Until     time.Time `json:"until"`
}

type quotaRecoveryBindingKey struct{}

func withQuotaRecoveryBinding(ctx context.Context, auth *Auth, route string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) context.Context {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return ctx
	}
	binding := quotaRecoveryBinding{Selection: route, Execution: req.Model, Requested: requestedModelAliasFromOptions(opts, route)}
	return context.WithValue(ctx, quotaRecoveryBindingKey{}, binding)
}

func mergeQuotaRecoveryBindings(now time.Time, sets ...[]quotaRecoveryBinding) []quotaRecoveryBinding {
	type names struct{ selection, execution, requested string }
	byName := make(map[names]quotaRecoveryBinding)
	for _, set := range sets {
		for _, b := range set {
			if !b.Until.After(now) || strings.TrimSpace(b.Execution) == "" || strings.TrimSpace(b.Selection) == "" {
				continue
			}
			b.Until = b.Until.Round(0)
			key := names{b.Selection, b.Execution, b.Requested}
			if old, ok := byName[key]; !ok || b.Until.After(old.Until) {
				byName[key] = b
			}
		}
	}
	if len(byName) == 0 {
		return nil
	}
	out := make([]quotaRecoveryBinding, 0, len(byName))
	for _, b := range byName {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Selection != b.Selection {
			return a.Selection < b.Selection
		}
		if a.Execution != b.Execution {
			return a.Execution < b.Execution
		}
		return a.Requested < b.Requested
	})
	return out
}

func quotaBindingsForCause(ctx context.Context, result Result, previous []quotaRecoveryBinding, deadline, now time.Time) []quotaRecoveryBinding {
	if !strings.EqualFold(strings.TrimSpace(result.Provider), "codex") || result.Error == nil || !quotaCodexSubscriptionError(result.Error) {
		return mergeQuotaRecoveryBindings(now, previous)
	}
	binding, ok := ctx.Value(quotaRecoveryBindingKey{}).(quotaRecoveryBinding)
	if !ok {
		return mergeQuotaRecoveryBindings(now, previous)
	} // Direct MarkResult has no dispatch receipt.
	binding.Until = deadline
	return mergeQuotaRecoveryBindings(now, previous, []quotaRecoveryBinding{binding})
}

func authQuotaRecoveryBindings(auth *Auth, now time.Time) []quotaRecoveryBinding {
	if auth == nil {
		return nil
	}
	sets := [][]quotaRecoveryBinding{auth.Quota.recoveryBindings}
	for _, s := range auth.ModelStates {
		if s != nil {
			sets = append(sets, s.Quota.recoveryBindings)
			if s.credentialQuotaShadow != nil {
				sets = append(sets, s.credentialQuotaShadow.Quota.recoveryBindings)
			}
		}
	}
	return mergeQuotaRecoveryBindings(now, sets...)
}

func authQuotaRecoveryBindingsEqual(a, b *Auth, now time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	type layer struct {
		model  string
		shadow bool
	}
	snapshot := func(auth *Auth) map[layer][]quotaRecoveryBinding {
		out := map[layer][]quotaRecoveryBinding{}
		if bindings := mergeQuotaRecoveryBindings(now, auth.Quota.recoveryBindings); len(bindings) > 0 {
			out[layer{}] = bindings
		}
		for model, state := range auth.ModelStates {
			if state == nil {
				continue
			}
			if bindings := mergeQuotaRecoveryBindings(now, state.Quota.recoveryBindings); len(bindings) > 0 {
				out[layer{model: model}] = bindings
			}
			if state.credentialQuotaShadow != nil {
				if bindings := mergeQuotaRecoveryBindings(now, state.credentialQuotaShadow.Quota.recoveryBindings); len(bindings) > 0 {
					out[layer{model: model, shadow: true}] = bindings
				}
			}
		}
		return out
	}
	return reflect.DeepEqual(snapshot(a), snapshot(b))
}

func preserveQuotaRecoveryBindings(auth, previous *Auth, now time.Time) {
	if auth == nil || previous == nil || !strings.EqualFold(auth.Provider, "codex") {
		return
	}
	auth.ModelStates = auth.Clone().ModelStates
	changed := quotaRecoveryBindingIdentityChanged(auth, previous)
	if changed {
		auth.Quota.recoveryBindings = nil
		for _, s := range auth.ModelStates {
			if s != nil {
				s.Quota.recoveryBindings = nil
				if s.credentialQuotaShadow != nil {
					s.credentialQuotaShadow.Quota.recoveryBindings = nil
				}
			}
		}
		return
	}
	if auth.Quota.Exceeded {
		auth.Quota.recoveryBindings = mergeQuotaRecoveryBindings(now, auth.Quota.recoveryBindings, previous.Quota.recoveryBindings)
	}
	for model, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		old := previous.ModelStates[model]
		if old == nil {
			continue
		}
		if state.Quota.Exceeded {
			state.Quota.recoveryBindings = mergeQuotaRecoveryBindings(now, state.Quota.recoveryBindings, old.Quota.recoveryBindings)
		}
		if state.Quota.Exceeded && state.Quota.NextRecoverAt.After(now) && state.credentialQuotaShadow == nil && old.credentialQuotaShadow != nil && isModelStateActiveCooldown(old.credentialQuotaShadow, now) {
			state.credentialQuotaShadow = old.credentialQuotaShadow.cloneShallow()
			state.credentialQuotaPropagated = old.credentialQuotaPropagated
		}
		if state.credentialQuotaShadow != nil && old.credentialQuotaShadow != nil && state.credentialQuotaShadow.Quota.Exceeded {
			state.credentialQuotaShadow.Quota.recoveryBindings = mergeQuotaRecoveryBindings(now, state.credentialQuotaShadow.Quota.recoveryBindings, old.credentialQuotaShadow.Quota.recoveryBindings)
		}
	}
}

// Request provenance belongs to retained account quota, rather than an OAuth
// grant version. Probe snapshot/version guards independently fence rotations.
func quotaRecoveryBindingIdentityChanged(auth, previous *Auth) bool {
	if auth.Provider != previous.Provider {
		return true
	}
	account := strings.TrimSpace(authMetadataString(auth, "account_id"))
	oldAccount := strings.TrimSpace(authMetadataString(previous, "account_id"))
	if account != "" || oldAccount != "" {
		return account != oldAccount
	}
	// Without a known account, only an unchanged effective inference credential
	// establishes continuity. Refresh-token-only changes do not replace it.
	token, oldToken := authAccessToken(auth), authAccessToken(previous)
	key, oldKey := auth.Attributes[AttributeAPIKey], previous.Attributes[AttributeAPIKey]
	return token != oldToken || key != oldKey || IsCodexUseSIWC(auth) != IsCodexUseSIWC(previous)
}
