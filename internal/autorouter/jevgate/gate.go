package jevgate

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevclient"
	log "github.com/sirupsen/logrus"
)

// Caller is the subset of jevclient.Client the gate needs, kept as an interface
// so tests can script outcomes without a server.
type Caller interface {
	Call(ctx context.Context, model string, state any, questions map[string]jevclient.Question) (jevclient.Response, error)
}

// Config is the resolved Jev configuration for one decision: the global master
// switch and credential presence, the per-router opt-in, and the tuning knobs.
type Config struct {
	GlobalEnabled bool
	APIKeySet     bool
	RouterEnabled bool
	Model         string
	MinConfidence float64
	Timeout       time.Duration
}

// Enabled reports whether the gate should consult the classifier at all.
func (c Config) Enabled() bool {
	return c.GlobalEnabled && c.APIKeySet && c.RouterEnabled
}

// Gate decides whether a classifier verdict should replace the heuristic tier.
type Gate struct {
	caller  Caller
	cache   *Cache
	breaker *Breaker
}

// NewGate builds a Gate. cache and breaker may be nil, in which case every call
// goes to the classifier and no breaker is tracked.
func NewGate(caller Caller, cache *Cache, breaker *Breaker) *Gate {
	return &Gate{caller: caller, cache: cache, breaker: breaker}
}

// Decide classifies st and reports whether the verdict should replace the
// heuristic tier. It never returns an error: every failure mode is reported
// through the returned Verdict's Verdict field with accepted=false, which is
// what makes the gate fail-open by construction.
//
// A low-confidence verdict is cached alongside accepted ones — the call was
// already paid for, and a prompt that keeps falling back should not keep
// re-paying for the same answer.
func (g *Gate) Decide(ctx context.Context, cfg Config, format, routerID string, st State) (Verdict, bool) {
	if g == nil || g.caller == nil || !cfg.Enabled() {
		return Verdict{}, false
	}
	if g.breaker.Open(routerID) {
		return Verdict{Verdict: VerdictBreakerOpen, Model: cfg.Model}, false
	}
	var key string
	if g.cache != nil {
		key = g.cache.Key(format, routerID, st, cfg.Model, QuestionHash())
		if cached, hit := g.cache.Get(key); hit {
			cached.Cache = CacheHit
			return cached, cached.Verdict == VerdictAccepted
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	start := time.Now()
	resp, errCall := g.caller.Call(callCtx, cfg.Model, st,
		map[string]jevclient.Question{QuestionID: TierQuestion()})
	v := Verdict{Model: cfg.Model, Cache: CacheMiss}
	if errCall != nil {
		v.Verdict = VerdictError
		var statusErr *jevclient.StatusError
		credentialRejected := errors.As(errCall, &statusErr) &&
			(statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden)
		if credentialRejected {
			g.breaker.Trip(routerID)
		}
		// Failing open must not mean failing silently: without this, a wrong
		// key or an unreachable endpoint turns the classifier into a no-op with
		// nothing in the logs to explain why routing never changed. The error
		// text carries no credential (see jevclient.StatusError), and the
		// request proceeds on the heuristic tier either way.
		log.WithError(errCall).WithFields(log.Fields{
			"router_id":         routerID,
			"model":             cfg.Model,
			"breaker_tripped":   credentialRejected,
			"fallback_decision": "heuristic_tier",
		}).Warn("jev gate: classifier unavailable; routing on the heuristic tier")
		return v, false
	}
	v.LatencyMs = time.Since(start).Milliseconds()
	v.InputTokens = resp.Usage.InputTokens
	answer, ok := resp.Answers[QuestionID]
	if !ok {
		// A 200 without our answer is a malformed response, not a verdict.
		v.Verdict = VerdictError
		log.WithFields(log.Fields{
			"router_id":         routerID,
			"model":             cfg.Model,
			"question_id":       QuestionID,
			"fallback_decision": "heuristic_tier",
		}).Warn("jev gate: classifier response had no tier answer; routing on the heuristic tier")
		return v, false
	}
	v.Choice = answer.Choice
	v.Confidence = answer.Confidence
	v.Probabilities = answer.Probabilities
	if v.Confidence >= cfg.MinConfidence {
		v.Verdict = VerdictAccepted
	} else {
		v.Verdict = VerdictLowConfidence
	}
	if g.cache != nil {
		g.cache.Put(key, v)
	}
	return v, v.Verdict == VerdictAccepted
}
