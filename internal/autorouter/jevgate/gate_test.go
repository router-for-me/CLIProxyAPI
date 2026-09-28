package jevgate

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevclient"
)

// fakeCaller is a scripted Caller recording how many times it was invoked.
type fakeCaller struct {
	calls   int
	resp    jevclient.Response
	err     error
	lastReq map[string]jevclient.Question
}

func (f *fakeCaller) Call(_ context.Context, _ string, _ any, q map[string]jevclient.Question) (jevclient.Response, error) {
	f.calls++
	f.lastReq = q
	return f.resp, f.err
}

// blockingCaller blocks until the context expires, exercising the timeout path.
type blockingCaller struct{}

func (b *blockingCaller) Call(ctx context.Context, _ string, _ any, _ map[string]jevclient.Question) (jevclient.Response, error) {
	<-ctx.Done()
	return jevclient.Response{}, ctx.Err()
}

func choiceResp(choice string, confidence float64) jevclient.Response {
	return jevclient.Response{
		Model: "jev-1.13.0",
		Answers: map[string]jevclient.ChoiceAnswer{
			QuestionID: {Type: "choice", Choice: choice, Confidence: confidence,
				Probabilities: map[string]float64{choice: confidence}},
		},
		Usage: jevclient.Usage{InputTokens: 100},
	}
}

func enabledCfg() Config {
	return Config{GlobalEnabled: true, APIKeySet: true, RouterEnabled: true,
		Model: "jev-1.13.0", MinConfidence: 0.5, Timeout: 400 * time.Millisecond}
}

func TestDecideAcceptsHighConfidenceVerdict(t *testing.T) {
	fc := &fakeCaller{resp: choiceResp("complex", 0.72)}
	g := NewGate(fc, NewCache(8), nil)
	st := BuildState(StateInput{LatestUserText: "refactor this module"})

	v, accepted := g.Decide(context.Background(), enabledCfg(), "openai", "r1", st)
	if !accepted {
		t.Fatalf("want accepted, got %+v", v)
	}
	if v.Choice != "complex" {
		t.Errorf("choice = %q, want complex", v.Choice)
	}
	if v.Verdict != VerdictAccepted {
		t.Errorf("verdict = %q, want %q", v.Verdict, VerdictAccepted)
	}
	if v.Cache != CacheMiss {
		t.Errorf("cache = %q, want %q", v.Cache, CacheMiss)
	}
	if v.InputTokens != 100 {
		t.Errorf("input tokens = %d, want 100", v.InputTokens)
	}
	if fc.calls != 1 {
		t.Errorf("calls = %d, want 1", fc.calls)
	}
	if _, ok := fc.lastReq[QuestionID]; !ok {
		t.Errorf("call did not carry the tier question: %v", fc.lastReq)
	}
}

func TestDecideRejectsLowConfidenceButCachesIt(t *testing.T) {
	fc := &fakeCaller{resp: choiceResp("complex", 0.30)}
	g := NewGate(fc, NewCache(8), nil)
	st := BuildState(StateInput{LatestUserText: "hmm"})

	v, accepted := g.Decide(context.Background(), enabledCfg(), "openai", "r1", st)
	if accepted {
		t.Fatal("low confidence must not be accepted")
	}
	if v.Verdict != VerdictLowConfidence {
		t.Errorf("verdict = %q", v.Verdict)
	}
	if v.Confidence != 0.30 {
		t.Errorf("confidence must be reported for tuning, got %v", v.Confidence)
	}
	// Second call must come from the cache: the call was already paid for.
	v2, accepted2 := g.Decide(context.Background(), enabledCfg(), "openai", "r1", st)
	if accepted2 {
		t.Fatal("cached low-confidence must not be accepted")
	}
	if v2.Cache != CacheHit {
		t.Errorf("cache = %q, want %q", v2.Cache, CacheHit)
	}
	if fc.calls != 1 {
		t.Errorf("calls = %d, want 1 (cache must absorb the repeat)", fc.calls)
	}
}

func TestDecideCacheHitSkipsTheCall(t *testing.T) {
	fc := &fakeCaller{resp: choiceResp("simple", 0.95)}
	g := NewGate(fc, NewCache(8), nil)
	st := BuildState(StateInput{LatestUserText: "hi"})

	if _, accepted := g.Decide(context.Background(), enabledCfg(), "openai", "r1", st); !accepted {
		t.Fatal("first call must be accepted")
	}
	v, accepted := g.Decide(context.Background(), enabledCfg(), "openai", "r1", st)
	if !accepted {
		t.Fatal("cached accepted verdict must be accepted")
	}
	if v.Cache != CacheHit {
		t.Errorf("cache = %q, want %q", v.Cache, CacheHit)
	}
	if fc.calls != 1 {
		t.Errorf("calls = %d, want 1", fc.calls)
	}
}

func TestDecideIsDisabledWithoutFullGate(t *testing.T) {
	cases := map[string]Config{
		"global off": {GlobalEnabled: false, APIKeySet: true, RouterEnabled: true},
		"no key":     {GlobalEnabled: true, APIKeySet: false, RouterEnabled: true},
		"router off": {GlobalEnabled: true, APIKeySet: true, RouterEnabled: false},
	}
	for name, cfg := range cases {
		fc := &fakeCaller{resp: choiceResp("simple", 0.99)}
		g := NewGate(fc, NewCache(8), nil)
		if _, accepted := g.Decide(context.Background(), cfg, "openai", "r1", BuildState(StateInput{})); accepted {
			t.Errorf("%s: must not accept", name)
		}
		if fc.calls != 0 {
			t.Errorf("%s: calls = %d, want 0", name, fc.calls)
		}
	}
}

func TestDecideFailsOpenOnCallError(t *testing.T) {
	fc := &fakeCaller{err: errors.New("boom")}
	g := NewGate(fc, NewCache(8), nil)
	v, accepted := g.Decide(context.Background(), enabledCfg(), "openai", "r1", BuildState(StateInput{}))
	if accepted {
		t.Fatal("must not accept on error")
	}
	if v.Verdict != VerdictError {
		t.Errorf("verdict = %q, want %q", v.Verdict, VerdictError)
	}
}

func TestDecideFailsOpenOnMissingAnswer(t *testing.T) {
	// A 200 without our answer key is a malformed response, not a verdict.
	fc := &fakeCaller{resp: jevclient.Response{Answers: map[string]jevclient.ChoiceAnswer{}}}
	g := NewGate(fc, NewCache(8), nil)
	v, accepted := g.Decide(context.Background(), enabledCfg(), "openai", "r1", BuildState(StateInput{}))
	if accepted {
		t.Fatal("must not accept a response missing the answer")
	}
	if v.Verdict != VerdictError {
		t.Errorf("verdict = %q, want %q", v.Verdict, VerdictError)
	}
}

func TestDecideTripsBreakerOnAuthFailureAndSkipsFurtherCalls(t *testing.T) {
	fc := &fakeCaller{err: &jevclient.StatusError{StatusCode: http.StatusUnauthorized, Body: "bad key"}}
	b := NewBreaker(time.Minute)
	g := NewGate(fc, NewCache(8), b)
	cfg := enabledCfg()
	st := BuildState(StateInput{LatestUserText: "unique one"})

	if _, accepted := g.Decide(context.Background(), cfg, "openai", "r1", st); accepted {
		t.Fatal("must not accept on 401")
	}
	if fc.calls != 1 {
		t.Fatalf("calls = %d, want 1", fc.calls)
	}
	// A different prompt on the same router must not reach the API at all.
	v, accepted := g.Decide(context.Background(), cfg, "openai", "r1", BuildState(StateInput{LatestUserText: "unique two"}))
	if accepted {
		t.Fatal("must not accept while breaker is open")
	}
	if fc.calls != 1 {
		t.Errorf("calls = %d, want 1 (breaker must suppress the call)", fc.calls)
	}
	if v.Verdict != VerdictBreakerOpen {
		t.Errorf("verdict = %q, want %q", v.Verdict, VerdictBreakerOpen)
	}
}

func TestDecideDoesNotTripBreakerOnServerError(t *testing.T) {
	fc := &fakeCaller{err: &jevclient.StatusError{StatusCode: http.StatusInternalServerError, Body: "oops"}}
	b := NewBreaker(time.Minute)
	g := NewGate(fc, NewCache(8), b)

	_, _ = g.Decide(context.Background(), enabledCfg(), "openai", "r1", BuildState(StateInput{LatestUserText: "one"}))
	_, _ = g.Decide(context.Background(), enabledCfg(), "openai", "r1", BuildState(StateInput{LatestUserText: "two"}))
	if fc.calls != 2 {
		t.Errorf("calls = %d, want 2 (5xx must not open the breaker)", fc.calls)
	}
}

func TestDecideTimeoutFailsOpen(t *testing.T) {
	g := NewGate(&blockingCaller{}, NewCache(8), nil)
	cfg := enabledCfg()
	cfg.Timeout = 20 * time.Millisecond

	start := time.Now()
	_, accepted := g.Decide(context.Background(), cfg, "openai", "r1", BuildState(StateInput{}))
	if accepted {
		t.Fatal("must not accept on timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("gate blocked for %v; timeout not enforced", elapsed)
	}
}

func TestConfigEnabledRequiresAllThreeSwitches(t *testing.T) {
	if !enabledCfg().Enabled() {
		t.Fatal("all three switches on must enable")
	}
	for name, cfg := range map[string]Config{
		"global": {GlobalEnabled: false, APIKeySet: true, RouterEnabled: true},
		"key":    {GlobalEnabled: true, APIKeySet: false, RouterEnabled: true},
		"router": {GlobalEnabled: true, APIKeySet: true, RouterEnabled: false},
	} {
		if cfg.Enabled() {
			t.Errorf("%s off must disable", name)
		}
	}
}
