package cliproxy

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	resetTriggerInterval           = 30 * time.Second
	resetTriggerDefaultClaudeModel = "claude-haiku-5-5"
	resetTriggerDefaultCodexModel  = "gpt-5.6-luna"
	resetTriggerDefaultMinDelay    = 30 * time.Second
	resetTriggerDefaultMaxDelay    = 5 * time.Minute
	// resetTriggerRetryBackoff spaces attempts after a failed trigger.
	resetTriggerRetryBackoff = 15 * time.Minute
	// resetTriggerMaxAttempts caps attempts per idle period of one credential.
	resetTriggerMaxAttempts = 3
)

// resetTriggerPrompts are rotated so trigger requests do not all look alike.
var resetTriggerPrompts = []string{
	"Reply with the single word OK.",
	"Say hi in one word.",
	"Answer with yes or no: is water wet?",
	"Name one primary color. One word only.",
	"What is 2+2? Answer with a digit.",
	"Reply with a single emoji.",
}

type resetTriggerSettings struct {
	enabled     bool
	claudeModel string
	codexModel  string
	minDelay    time.Duration
	maxDelay    time.Duration
}

func resetTriggerSettingsFromConfig(cfg *config.Config) resetTriggerSettings {
	settings := resetTriggerSettings{
		claudeModel: resetTriggerDefaultClaudeModel,
		codexModel:  resetTriggerDefaultCodexModel,
		minDelay:    resetTriggerDefaultMinDelay,
		maxDelay:    resetTriggerDefaultMaxDelay,
	}
	if cfg == nil {
		return settings
	}
	raw := cfg.Routing.ResetTrigger
	settings.enabled = raw.Enabled
	if model := strings.TrimSpace(raw.ClaudeModel); model != "" {
		settings.claudeModel = model
	}
	if model := strings.TrimSpace(raw.CodexModel); model != "" {
		settings.codexModel = model
	}
	if d, err := time.ParseDuration(strings.TrimSpace(raw.MinDelay)); err == nil && d >= 0 {
		settings.minDelay = d
	}
	if d, err := time.ParseDuration(strings.TrimSpace(raw.MaxDelay)); err == nil && d >= 0 {
		settings.maxDelay = d
	}
	if settings.maxDelay < settings.minDelay {
		settings.maxDelay = settings.minDelay
	}
	return settings
}

func (s resetTriggerSettings) model(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return s.claudeModel
	case "codex":
		return s.codexModel
	default:
		return ""
	}
}

// resetTriggerPlan tracks one credential's pending trigger for one idle period.
type resetTriggerPlan struct {
	window    string
	idleSince time.Time
	fireAt    time.Time
	attempts  int
	inFlight  bool
	done      bool
}

func (p *resetTriggerPlan) matches(state coreauth.ResetTriggerState) bool {
	return p.window == state.Window && p.idleSince.Equal(state.IdleSince)
}

type resetTriggerExecFunc func(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)

// resetTrigger starts idle short quota windows (Claude "5h", Codex primary)
// with a cheap pinned request a random delay after they reset. Soonest-reset
// routing concentrates traffic on one credential, so without this the other
// credentials' five-hour windows stay idle and their quota is never used.
type resetTrigger struct {
	manager  *coreauth.Manager
	settings func() resetTriggerSettings
	execute  resetTriggerExecFunc
	now      func() time.Time
	randInt  func(n int64) int64

	mu    sync.Mutex
	plans map[string]*resetTriggerPlan
	wg    sync.WaitGroup

	stopOnce sync.Once
	cancel   context.CancelFunc
	done     chan struct{}
}

func newResetTrigger(manager *coreauth.Manager, settings func() resetTriggerSettings) *resetTrigger {
	t := &resetTrigger{
		manager:  manager,
		settings: settings,
		now:      time.Now,
		randInt:  rand.Int64N,
		plans:    make(map[string]*resetTriggerPlan),
	}
	if manager != nil {
		t.execute = manager.Execute
	}
	return t
}

func (t *resetTrigger) randomDuration(span time.Duration) time.Duration {
	if span <= 0 {
		return 0
	}
	return time.Duration(t.randInt(int64(span) + 1))
}

// tick evaluates every credential once and fires due triggers in background
// goroutines bound to ctx.
func (t *resetTrigger) tick(ctx context.Context) {
	if t == nil || t.manager == nil || t.execute == nil {
		return
	}
	settings := t.settings()
	if !settings.enabled || t.manager.HomeEnabled() {
		t.mu.Lock()
		clear(t.plans)
		t.mu.Unlock()
		return
	}
	now := t.now()
	auths := t.manager.List()
	seen := make(map[string]struct{}, len(auths))
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		model := settings.model(auth.Provider)
		if model == "" {
			continue
		}
		seen[auth.ID] = struct{}{}
		plan := t.plans[auth.ID]
		if plan != nil && plan.inFlight {
			continue
		}
		state := coreauth.ResetTriggerStateForAuth(auth, model, now)
		// Keep the plan while not due: a cooldown after a failed trigger must
		// not reset its attempts or backoff. A new idle period replaces it.
		if !state.Due {
			continue
		}
		if plan == nil || !plan.matches(state) {
			plan = &resetTriggerPlan{window: state.Window, idleSince: state.IdleSince}
			plan.fireAt = t.firstFireAt(state.IdleSince, now, settings)
			t.plans[auth.ID] = plan
		}
		if plan.done || now.Before(plan.fireAt) {
			continue
		}
		plan.inFlight = true
		plan.attempts++
		t.wg.Add(1)
		go t.fire(ctx, auth.ID, auth.Provider, model, plan)
	}
	for id, plan := range t.plans {
		if _, ok := seen[id]; !ok && !plan.inFlight {
			delete(t.plans, id)
		}
	}
}

// firstFireAt places the trigger a random delay after the reset. When that
// moment already passed (restart, newly added credential) the trigger is
// spread over the delay span from now so credentials do not fire together.
func (t *resetTrigger) firstFireAt(idleSince, now time.Time, settings resetTriggerSettings) time.Time {
	span := settings.maxDelay - settings.minDelay
	if !idleSince.IsZero() {
		if fireAt := idleSince.Add(settings.minDelay + t.randomDuration(span)); fireAt.After(now) {
			return fireAt
		}
	}
	return now.Add(t.randomDuration(settings.maxDelay))
}

func (t *resetTrigger) fire(ctx context.Context, authID, provider, model string, plan *resetTriggerPlan) {
	defer t.wg.Done()
	errExec := t.send(ctx, authID, provider, model)
	t.mu.Lock()
	defer t.mu.Unlock()
	plan.inFlight = false
	if errExec == nil {
		plan.done = true
		log.Infof("reset trigger: started %s quota window of %s credential %s with %s", windowLabel(plan.window), provider, authID, model)
		return
	}
	if ctx.Err() != nil {
		return
	}
	if plan.attempts >= resetTriggerMaxAttempts {
		plan.done = true
		log.Warnf("reset trigger: giving up on %s credential %s after %d attempts: %v", provider, authID, plan.attempts, errExec)
		return
	}
	plan.fireAt = t.now().Add(resetTriggerRetryBackoff)
	log.Warnf("reset trigger: %s credential %s failed (attempt %d), retrying in %s: %v", provider, authID, plan.attempts, resetTriggerRetryBackoff, errExec)
}

func windowLabel(window string) string {
	if window == "" {
		return "unknown"
	}
	return window
}

func (t *resetTrigger) send(ctx context.Context, authID, provider, model string) error {
	prompt := resetTriggerPrompts[t.randInt(int64(len(resetTriggerPrompts)))]
	payload, format, errBuild := buildResetTriggerPayload(provider, model, prompt)
	if errBuild != nil {
		return errBuild
	}
	// A unique explicit session keeps session affinity from binding the
	// trigger to (or rejecting it for) an unrelated client session.
	sessionID := fmt.Sprintf("reset-trigger-%s-%d", authID, t.now().UnixNano())
	headers := http.Header{}
	headers.Set("X-Session-ID", sessionID)
	req := cliproxyexecutor.Request{Model: model, Payload: payload, Format: format}
	opts := cliproxyexecutor.Options{
		Headers:         headers,
		OriginalRequest: payload,
		SourceFormat:    format,
		Metadata: map[string]any{
			cliproxyexecutor.PinnedAuthMetadataKey:     authID,
			cliproxyexecutor.RequestedModelMetadataKey: model,
		},
	}
	_, errExec := t.execute(ctx, []string{provider}, req, opts)
	return errExec
}

// buildResetTriggerPayload returns a minimal request in the provider's native
// client format so translation stays a pass-through.
func buildResetTriggerPayload(provider, model, prompt string) ([]byte, sdktranslator.Format, error) {
	doc := ojson.NewDocString("{}")
	var format sdktranslator.Format
	var errs []error
	set := func(path string, value any) {
		if errSet := doc.Set(path, value); errSet != nil {
			errs = append(errs, errSet)
		}
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		format = sdktranslator.FromString("claude")
		set("model", model)
		set("max_tokens", 16)
		set("messages.0.role", "user")
		set("messages.0.content", prompt)
	case "codex":
		format = sdktranslator.FromString("openai-response")
		set("model", model)
		set("instructions", "Answer in as few words as possible.")
		set("input.0.type", "message")
		set("input.0.role", "user")
		set("input.0.content.0.type", "input_text")
		set("input.0.content.0.text", prompt)
		set("reasoning.effort", "low")
		set("store", false)
	default:
		return nil, "", fmt.Errorf("reset trigger: unsupported provider %q", provider)
	}
	if len(errs) > 0 {
		return nil, "", fmt.Errorf("reset trigger: build payload: %w", errs[0])
	}
	return doc.Bytes(), format, nil
}

func (t *resetTrigger) start(ctx context.Context) {
	if t == nil {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	t.done = make(chan struct{})
	go func() {
		defer close(t.done)
		ticker := time.NewTicker(resetTriggerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				t.tick(loopCtx)
			}
		}
	}()
}

// stop ends the loop and cancels in-flight trigger requests.
func (t *resetTrigger) stop() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		if t.cancel != nil {
			t.cancel()
			<-t.done
		}
		t.wg.Wait()
	})
}

// startResetTrigger launches the reset trigger loop. Settings are read from
// the live config on every tick, so enabling it does not need a restart.
func (s *Service) startResetTrigger(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	trigger := newResetTrigger(s.coreManager, func() resetTriggerSettings {
		return resetTriggerSettingsFromConfig(s.currentConfig())
	})
	trigger.start(ctx)
	s.resetTrigger = trigger
}

func (s *Service) stopResetTrigger() {
	if s == nil || s.resetTrigger == nil {
		return
	}
	s.resetTrigger.stop()
}
