package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7757CreditsExecutor struct {
	mu             sync.Mutex
	prepareAuthIDs []string
	executeAuthIDs []string
	executeModels  []string
	executeHeaders []http.Header
	streamAuthIDs  []string
	streamModels   []string
}

var _ RequestAuthPreparer = (*issue7757CreditsExecutor)(nil)

func (e *issue7757CreditsExecutor) Identifier() string { return "antigravity" }

func (e *issue7757CreditsExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }

func (e *issue7757CreditsExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.prepareAuthIDs = append(e.prepareAuthIDs, auth.ID)
	e.mu.Unlock()
	return auth, nil
}

func (e *issue7757CreditsExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeAuthIDs = append(e.executeAuthIDs, auth.ID)
	e.executeModels = append(e.executeModels, req.Model)
	if opts.Headers != nil {
		e.executeHeaders = append(e.executeHeaders, opts.Headers.Clone())
	} else {
		e.executeHeaders = append(e.executeHeaders, nil)
	}
	e.mu.Unlock()
	if !AntigravityCreditsRequested(ctx) {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
	}
	return cliproxyexecutor.Response{Payload: []byte("credits-ok:" + auth.ID + ":" + req.Model)}, nil
}

func (e *issue7757CreditsExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamAuthIDs = append(e.streamAuthIDs, auth.ID)
	e.streamModels = append(e.streamModels, req.Model)
	e.mu.Unlock()
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if !AntigravityCreditsRequested(ctx) {
		ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("credits-stream-ok:" + auth.ID + ":" + req.Model)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *issue7757CreditsExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *issue7757CreditsExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *issue7757CreditsExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *issue7757CreditsExecutor) PrepareAuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.prepareAuthIDs))
	copy(out, e.prepareAuthIDs)
	return out
}

func (e *issue7757CreditsExecutor) ExecuteAuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeAuthIDs))
	copy(out, e.executeAuthIDs)
	return out
}

func TestFindAllAntigravityCreditsCandidateAuths_ExcludesTerminalUnauthorized_Issue7757(t *testing.T) {
	const model = "claude-sonnet-4-6"
	reg := registry.GetGlobalRegistry()
	healthyID := "ag-issue7757-healthy-cooldown"
	unauthID := "ag-issue7757-terminal-401"
	reg.RegisterClient(healthyID, "antigravity", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(unauthID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(healthyID)
		reg.UnregisterClient(unauthID)
		SetAntigravityCreditsHint(healthyID, AntigravityCreditsHint{})
		SetAntigravityCreditsHint(unauthID, AntigravityCreditsHint{})
	})

	cooldownUntil := time.Now().Add(30 * time.Minute)
	m := &Manager{
		auths: map[string]*Auth{
			healthyID: {
				ID:       healthyID,
				Provider: "antigravity",
				Status:   StatusActive,
				ModelStates: map[string]*ModelState{
					model: {
						Unavailable:    true,
						NextRetryAfter: cooldownUntil,
						Quota: QuotaState{
							Exceeded:      true,
							NextRecoverAt: cooldownUntil,
						},
					},
				},
			},
			unauthID: {
				ID:          unauthID,
				Provider:    "antigravity",
				Status:      StatusError,
				Unavailable: true,
				LastError: &Error{
					Code:       "unauthorized",
					Message:    "invalid credentials",
					HTTPStatus: http.StatusUnauthorized,
				},
			},
		},
		executors: map[string]ProviderExecutor{
			"antigravity": schedulerTestExecutor{},
		},
	}

	candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(context.Background(), model, cliproxyexecutor.Options{})
	if errCandidates != nil {
		t.Fatalf("findAllAntigravityCreditsCandidateAuths() error = %v", errCandidates)
	}
	if len(candidates) != 1 || candidates[0].auth.ID != healthyID {
		got := make([]string, 0, len(candidates))
		for _, c := range candidates {
			got = append(got, c.auth.ID)
		}
		t.Fatalf("candidates = %v, want [%s] (terminal 401 auth must be excluded)", got, healthyID)
	}
}

func TestFindAllAntigravityCreditsCandidateAuths_EnforcesEligibilityAndRouteModelSupport_Issue7757(t *testing.T) {
	const (
		claudeModel = "claude-sonnet-4-6"
		geminiModel = "gemini-3-flash"
	)

	t.Run("unsupported_model_excluded_and_skips_prepare", func(t *testing.T) {
		reg := registry.GetGlobalRegistry()
		claudeID := "ag-issue7757-has-claude"
		geminiOnlyID := "ag-issue7757-gemini-only"
		reg.RegisterClient(claudeID, "antigravity", []*registry.ModelInfo{{ID: claudeModel}, {ID: geminiModel}})
		reg.RegisterClient(geminiOnlyID, "antigravity", []*registry.ModelInfo{{ID: geminiModel}})
		t.Cleanup(func() {
			reg.UnregisterClient(claudeID)
			reg.UnregisterClient(geminiOnlyID)
			SetAntigravityCreditsHint(claudeID, AntigravityCreditsHint{})
			SetAntigravityCreditsHint(geminiOnlyID, AntigravityCreditsHint{})
		})

		exec := &issue7757CreditsExecutor{}
		m := NewManager(nil, nil, nil)
		m.SetConfig(&internalconfig.Config{
			QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
		})
		m.RegisterExecutor(exec)
		if _, errReg := m.Register(context.Background(), &Auth{ID: claudeID, Provider: "antigravity", Status: StatusActive}); errReg != nil {
			t.Fatalf("register claude auth: %v", errReg)
		}
		if _, errReg := m.Register(context.Background(), &Auth{ID: geminiOnlyID, Provider: "antigravity", Status: StatusActive}); errReg != nil {
			t.Fatalf("register gemini-only auth: %v", errReg)
		}

		candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(context.Background(), claudeModel, cliproxyexecutor.Options{})
		if errCandidates != nil {
			t.Fatalf("findAllAntigravityCreditsCandidateAuths() error = %v", errCandidates)
		}
		if len(candidates) != 1 || candidates[0].auth.ID != claudeID {
			got := make([]string, 0, len(candidates))
			for _, c := range candidates {
				got = append(got, c.auth.ID)
			}
			t.Fatalf("candidates = %v, want [%s]", got, claudeID)
		}

		// Positive control + negative check: in the mixed pool, PrepareRequestAuth must run
		// for claudeID (proving the counter fires) and never for geminiOnlyID.
		if _, okExec, errExec := m.tryAntigravityCreditsExecute(context.Background(), cliproxyexecutor.Request{Model: claudeModel}, cliproxyexecutor.Options{}); errExec != nil || !okExec {
			t.Fatalf("tryAntigravityCreditsExecute() = (ok=%v, err=%v), want (true, nil)", okExec, errExec)
		}
		streamRes, okStream, errStream := m.tryAntigravityCreditsExecuteStream(context.Background(), cliproxyexecutor.Request{Model: claudeModel}, cliproxyexecutor.Options{Stream: true})
		if errStream != nil || !okStream || streamRes == nil {
			t.Fatalf("tryAntigravityCreditsExecuteStream() = (res=%v, ok=%v, err=%v), want (non-nil, true, nil)", streamRes != nil, okStream, errStream)
		}
		for chunk := range streamRes.Chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
			}
		}
		prepared := exec.PrepareAuthIDs()
		if len(prepared) != 2 || prepared[0] != claudeID || prepared[1] != claudeID {
			t.Fatalf("PrepareAuthIDs() = %v, want [%s %s] (geminiOnlyID must never be prepared)", prepared, claudeID, claudeID)
		}
	})

	t.Run("selection_model_override_still_requires_route_model_support", func(t *testing.T) {
		reg := registry.GetGlobalRegistry()
		geminiOnlyID := "ag-issue7757-seloverride-gemini-only"
		claudeOnlyID := "ag-issue7757-seloverride-claude-only"
		bothID := "ag-issue7757-seloverride-both"
		reg.RegisterClient(geminiOnlyID, "antigravity", []*registry.ModelInfo{{ID: geminiModel}})
		reg.RegisterClient(claudeOnlyID, "antigravity", []*registry.ModelInfo{{ID: claudeModel}})
		reg.RegisterClient(bothID, "antigravity", []*registry.ModelInfo{{ID: geminiModel}, {ID: claudeModel}})
		t.Cleanup(func() {
			reg.UnregisterClient(geminiOnlyID)
			reg.UnregisterClient(claudeOnlyID)
			reg.UnregisterClient(bothID)
			SetAntigravityCreditsHint(geminiOnlyID, AntigravityCreditsHint{})
			SetAntigravityCreditsHint(claudeOnlyID, AntigravityCreditsHint{})
			SetAntigravityCreditsHint(bothID, AntigravityCreditsHint{})
		})

		exec := &issue7757CreditsExecutor{}
		m := NewManager(nil, nil, nil)
		m.SetConfig(&internalconfig.Config{
			QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
		})
		m.RegisterExecutor(exec)
		for _, id := range []string{geminiOnlyID, claudeOnlyID, bothID} {
			if _, errReg := m.Register(context.Background(), &Auth{ID: id, Provider: "antigravity", Status: StatusActive}); errReg != nil {
				t.Fatalf("register %s: %v", id, errReg)
			}
		}

		opts := cliproxyexecutor.Options{
			Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: geminiModel},
		}
		candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(context.Background(), claudeModel, opts)
		if errCandidates != nil {
			t.Fatalf("findAllAntigravityCreditsCandidateAuths() error = %v", errCandidates)
		}
		if len(candidates) != 1 || candidates[0].auth.ID != bothID {
			got := make([]string, 0, len(candidates))
			for _, c := range candidates {
				got = append(got, c.auth.ID)
			}
			t.Fatalf("candidates = %v, want [%s] (must enforce both selectionModel and routeModel)", got, bothID)
		}

		resp, okExec, errExec := m.tryAntigravityCreditsExecute(context.Background(), cliproxyexecutor.Request{Model: claudeModel}, opts)
		if errExec != nil || !okExec {
			t.Fatalf("tryAntigravityCreditsExecute() = (ok=%v, err=%v), want (true, nil)", okExec, errExec)
		}
		if got := string(resp.Payload); got != "credits-ok:"+bothID+":"+claudeModel {
			t.Fatalf("execute payload = %q, want %q", got, "credits-ok:"+bothID+":"+claudeModel)
		}

		streamOpts := cliproxyexecutor.Options{
			Stream:   true,
			Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: geminiModel},
		}
		streamRes, okStream, errStream := m.tryAntigravityCreditsExecuteStream(context.Background(), cliproxyexecutor.Request{Model: claudeModel}, streamOpts)
		if errStream != nil || !okStream || streamRes == nil {
			t.Fatalf("tryAntigravityCreditsExecuteStream() = (res=%v, ok=%v, err=%v), want (non-nil, true, nil)", streamRes != nil, okStream, errStream)
		}
		var streamed []byte
		for chunk := range streamRes.Chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
			}
			streamed = append(streamed, chunk.Payload...)
		}
		if got := string(streamed); got != "credits-stream-ok:"+bothID+":"+claudeModel {
			t.Fatalf("stream payload = %q, want %q", got, "credits-stream-ok:"+bothID+":"+claudeModel)
		}
		prepared := exec.PrepareAuthIDs()
		if len(prepared) != 2 || prepared[0] != bothID || prepared[1] != bothID {
			t.Fatalf("PrepareAuthIDs() = %v, want [%s %s]", prepared, bothID, bothID)
		}
	})

	t.Run("required_kind_and_credential_policy_enforced", func(t *testing.T) {
		reg := registry.GetGlobalRegistry()
		oauthID := "ag-issue7757-oauth-kind"
		reg.RegisterClient(oauthID, "antigravity", []*registry.ModelInfo{{ID: claudeModel}})
		t.Cleanup(func() {
			reg.UnregisterClient(oauthID)
			SetAntigravityCreditsHint(oauthID, AntigravityCreditsHint{})
		})

		m := &Manager{
			auths: map[string]*Auth{
				oauthID: {
					ID:         oauthID,
					Provider:   "antigravity",
					Status:     StatusActive,
					Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
				},
			},
			executors: map[string]ProviderExecutor{
				"antigravity": schedulerTestExecutor{},
			},
		}

		ctxAPIKey := withRequiredAuthKind(context.Background(), AuthKindAPIKey)
		candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(ctxAPIKey, claudeModel, cliproxyexecutor.Options{})
		if errCandidates != nil {
			t.Fatalf("findAllAntigravityCreditsCandidateAuths(requiredKind) error = %v", errCandidates)
		}
		if len(candidates) != 0 {
			t.Fatalf("candidates with requiredKind=apikey len = %d, want 0", len(candidates))
		}

		ctxPolicy := withCredentialPolicy(context.Background(), CredentialPolicyCodexAlphaSearchV1)
		candidates, errCandidates = m.findAllAntigravityCreditsCandidateAuths(ctxPolicy, claudeModel, cliproxyexecutor.Options{})
		if errCandidates != nil {
			t.Fatalf("findAllAntigravityCreditsCandidateAuths(credentialPolicy) error = %v", errCandidates)
		}
		if len(candidates) != 0 {
			t.Fatalf("candidates with codex credentialPolicy len = %d, want 0", len(candidates))
		}
	})
}

func TestFindAllAntigravityCreditsCandidateAuths_ResolvesOAuthModelAlias_Issue7757(t *testing.T) {
	t.Run("channel_alias_without_claude_in_alias_name", func(t *testing.T) {
		const (
			aliasModel    = "coding-pro"
			upstreamModel = "claude-opus-5-5-high"
		)
		reg := registry.GetGlobalRegistry()
		authID := "ag-issue7757-alias-claude"
		reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: upstreamModel}})
		t.Cleanup(func() {
			reg.UnregisterClient(authID)
			SetAntigravityCreditsHint(authID, AntigravityCreditsHint{})
		})

		exec := &issue7757CreditsExecutor{}
		m := NewManager(nil, nil, nil)
		m.SetConfig(&internalconfig.Config{
			QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
		})
		m.RegisterExecutor(exec)
		m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
			"antigravity": {{
				Name:  upstreamModel,
				Alias: aliasModel,
			}},
		})
		if _, errReg := m.Register(context.Background(), &Auth{ID: authID, Provider: "antigravity", Status: StatusActive}); errReg != nil {
			t.Fatalf("register auth: %v", errReg)
		}

		for _, requested := range []string{aliasModel, aliasModel + "(high)"} {
			candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(context.Background(), requested, cliproxyexecutor.Options{})
			if errCandidates != nil {
				t.Fatalf("findAllAntigravityCreditsCandidateAuths(%q) error = %v", requested, errCandidates)
			}
			if len(candidates) != 1 || candidates[0].auth.ID != authID {
				t.Fatalf("candidates(%q) len = %d, want 1 (%s)", requested, len(candidates), authID)
			}
		}

		resp, okExec, errExec := m.tryAntigravityCreditsExecute(context.Background(), cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
		if errExec != nil || !okExec {
			t.Fatalf("tryAntigravityCreditsExecute(%q) = (ok=%v, err=%v), want (true, nil)", aliasModel, okExec, errExec)
		}
		if got := string(resp.Payload); got != "credits-ok:"+authID+":"+upstreamModel {
			t.Fatalf("response payload = %q, want %q", got, "credits-ok:"+authID+":"+upstreamModel)
		}

		streamRes, okStream, errStream := m.tryAntigravityCreditsExecuteStream(context.Background(), cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{Stream: true})
		if errStream != nil || !okStream || streamRes == nil {
			t.Fatalf("tryAntigravityCreditsExecuteStream(%q) = (res=%v, ok=%v, err=%v), want (non-nil, true, nil)", aliasModel, streamRes != nil, okStream, errStream)
		}
		var streamed []byte
		for chunk := range streamRes.Chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
			}
			streamed = append(streamed, chunk.Payload...)
		}
		if got := string(streamed); got != "credits-stream-ok:"+authID+":"+upstreamModel {
			t.Fatalf("stream payload = %q, want %q", got, "credits-stream-ok:"+authID+":"+upstreamModel)
		}
	})

	t.Run("per_auth_alias_claude_included_and_claude_alias_to_gemini_excluded", func(t *testing.T) {
		const (
			aliasToClaude = "coding-pro"
			claudeTarget  = "claude-opus-5-5-high"
			aliasToGemini = "claude-fast"
			geminiTarget  = "gemini-3-flash"
		)
		reg := registry.GetGlobalRegistry()
		claudeAuthID := "ag-issue7757-per-auth-claude"
		geminiAuthID := "ag-issue7757-per-auth-gemini"
		reg.RegisterClient(claudeAuthID, "antigravity", []*registry.ModelInfo{{ID: claudeTarget}})
		reg.RegisterClient(geminiAuthID, "antigravity", []*registry.ModelInfo{{ID: geminiTarget}})
		t.Cleanup(func() {
			reg.UnregisterClient(claudeAuthID)
			reg.UnregisterClient(geminiAuthID)
			SetAntigravityCreditsHint(claudeAuthID, AntigravityCreditsHint{})
			SetAntigravityCreditsHint(geminiAuthID, AntigravityCreditsHint{})
		})

		claudeAuth := &Auth{ID: claudeAuthID, Provider: "antigravity", Status: StatusActive}
		SetOAuthModelAliasesAttribute(claudeAuth, []internalconfig.OAuthModelAlias{{
			Name:  claudeTarget,
			Alias: aliasToClaude,
		}})
		geminiAuth := &Auth{ID: geminiAuthID, Provider: "antigravity", Status: StatusActive}
		SetOAuthModelAliasesAttribute(geminiAuth, []internalconfig.OAuthModelAlias{{
			Name:  geminiTarget,
			Alias: aliasToGemini,
		}})

		m := &Manager{
			auths: map[string]*Auth{
				claudeAuthID: claudeAuth,
				geminiAuthID: geminiAuth,
			},
			executors: map[string]ProviderExecutor{
				"antigravity": schedulerTestExecutor{},
			},
		}

		candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(context.Background(), aliasToClaude, cliproxyexecutor.Options{})
		if errCandidates != nil {
			t.Fatalf("findAllAntigravityCreditsCandidateAuths(%q) error = %v", aliasToClaude, errCandidates)
		}
		if len(candidates) != 1 || candidates[0].auth.ID != claudeAuthID {
			t.Fatalf("candidates(%q) len = %d, want [%s]", aliasToClaude, len(candidates), claudeAuthID)
		}

		nonClaudeCandidates, errNonClaude := m.findAllAntigravityCreditsCandidateAuths(context.Background(), aliasToGemini, cliproxyexecutor.Options{})
		if errNonClaude != nil {
			t.Fatalf("findAllAntigravityCreditsCandidateAuths(%q) error = %v", aliasToGemini, errNonClaude)
		}
		if len(nonClaudeCandidates) != 0 {
			t.Fatalf("candidates(%q -> %q) len = %d, want 0", aliasToGemini, geminiTarget, len(nonClaudeCandidates))
		}
	})

	t.Run("thinking_suffix_containing_claude_does_not_match_gemini_target", func(t *testing.T) {
		const (
			aliasName    = "fast"
			geminiTarget = "gemini-3-flash"
		)
		reg := registry.GetGlobalRegistry()
		authID := "ag-issue7757-suffix-gemini"
		reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: geminiTarget}})
		t.Cleanup(func() {
			reg.UnregisterClient(authID)
			SetAntigravityCreditsHint(authID, AntigravityCreditsHint{})
		})

		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(&issue7757CreditsExecutor{})
		m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
			"antigravity": {{
				Name:  geminiTarget,
				Alias: aliasName,
			}},
		})
		if _, errReg := m.Register(context.Background(), &Auth{ID: authID, Provider: "antigravity", Status: StatusActive}); errReg != nil {
			t.Fatalf("register auth: %v", errReg)
		}

		for _, requested := range []string{aliasName + "(claude)", geminiTarget + "(claude)"} {
			candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(context.Background(), requested, cliproxyexecutor.Options{})
			if errCandidates != nil {
				t.Fatalf("findAllAntigravityCreditsCandidateAuths(%q) error = %v", requested, errCandidates)
			}
			if len(candidates) != 0 {
				t.Fatalf("candidates(%q) len = %d, want 0 (thinking suffix must not trigger claude credits)", requested, len(candidates))
			}
		}
	})
}

func TestTryAntigravityCreditsExecute_InvokesRequestAfterAuthInterceptor_Issue7757(t *testing.T) {
	const model = "claude-sonnet-4-6"
	reg := registry.GetGlobalRegistry()
	authID := "ag-issue7757-interceptor"
	reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		SetAntigravityCreditsHint(authID, AntigravityCreditsHint{})
	})

	exec := &issue7757CreditsExecutor{}
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	m.RegisterExecutor(exec)
	if _, errReg := m.Register(context.Background(), &Auth{ID: authID, Provider: "antigravity", Status: StatusActive}); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	interceptCalls := 0
	optsPass := cliproxyexecutor.Options{
		RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			interceptCalls++
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{
				Headers: http.Header{"X-Guard-Checked": []string{"yes"}},
			}
		},
	}
	resp, okExec, errExec := m.tryAntigravityCreditsExecute(context.Background(), cliproxyexecutor.Request{Model: model}, optsPass)
	if errExec != nil || !okExec {
		t.Fatalf("tryAntigravityCreditsExecute(pass) = (ok=%v, err=%v), want (true, nil)", okExec, errExec)
	}
	if interceptCalls != 1 {
		t.Fatalf("interceptor calls = %d, want 1", interceptCalls)
	}
	if len(exec.executeHeaders) != 1 || exec.executeHeaders[0].Get("X-Guard-Checked") != "yes" {
		t.Fatalf("upstream headers = %#v, want X-Guard-Checked=yes", exec.executeHeaders)
	}
	if got := string(resp.Payload); got != "credits-ok:"+authID+":"+model {
		t.Fatalf("response payload = %q, want %q", got, "credits-ok:"+authID+":"+model)
	}

	// Verify fail-closed termination blocks upstream Execute.
	optsDeny := cliproxyexecutor.Options{
		RequestAfterAuthInterceptor: func(_ context.Context, _ cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{
				Terminate:    true,
				StatusCode:   http.StatusForbidden,
				ResponseBody: []byte("blocked by guard"),
			}
		},
	}
	_, okDeny, errDeny := m.tryAntigravityCreditsExecute(context.Background(), cliproxyexecutor.Request{Model: model}, optsDeny)
	if okDeny || errDeny == nil || statusCodeFromError(errDeny) != http.StatusForbidden {
		t.Fatalf("tryAntigravityCreditsExecute(deny) = (ok=%v, err=%v), want (false, 403 error)", okDeny, errDeny)
	}
	if gotCalls := exec.ExecuteAuthIDs(); len(gotCalls) != 1 {
		t.Fatalf("upstream Execute calls after deny = %d, want 1 (pass-through only)", len(gotCalls))
	}
}

func TestTryAntigravityCreditsExecute_EmptyModelsSkipsPrepareRequestAuth_Issue7757(t *testing.T) {
	const model = "claude-sonnet-4-6"
	reg := registry.GetGlobalRegistry()
	emptyID := "copilot-issue7757-empty-models"
	eligibleID := "ag-issue7757-eligible-models"
	reg.RegisterClient(eligibleID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(emptyID)
		reg.UnregisterClient(eligibleID)
		SetAntigravityCreditsHint(eligibleID, AntigravityCreditsHint{})
	})

	exec := &issue7757CreditsExecutor{}
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	// Cap at 1 attempted credential so that if the zero-model candidate were counted
	// or prepared before len(models) == 0, the eligible candidate could not run.
	m.SetRetryConfig(0, 0, 1)
	m.RegisterExecutor(exec)

	emptyAuth := &Auth{ID: emptyID, Provider: "copilot", Status: StatusActive}
	eligibleAuth := &Auth{ID: eligibleID, Provider: "antigravity", Status: StatusActive}
	if _, errReg := m.Register(context.Background(), emptyAuth); errReg != nil {
		t.Fatalf("register empty-model auth: %v", errReg)
	}
	if _, errReg := m.Register(context.Background(), eligibleAuth); errReg != nil {
		t.Fatalf("register eligible auth: %v", errReg)
	}

	if models, _, _, _ := m.executionModelCandidatesWithAlias(emptyAuth, model); len(models) != 0 {
		t.Fatalf("precondition failed: executionModelCandidatesWithAlias(emptyAuth) = %v, want empty", models)
	}

	candidates := []creditsCandidateEntry{
		{auth: emptyAuth.Clone(), executor: exec, provider: "antigravity"},
		{auth: eligibleAuth.Clone(), executor: exec, provider: "antigravity"},
	}

	resp, okExec, errExec := m.tryAntigravityCreditsExecuteCandidates(context.Background(), cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}, model, candidates)
	if errExec != nil || !okExec {
		t.Fatalf("tryAntigravityCreditsExecuteCandidates() = (ok=%v, err=%v), want (true, nil)", okExec, errExec)
	}
	if got := string(resp.Payload); got != "credits-ok:"+eligibleID+":"+model {
		t.Fatalf("response payload = %q, want %q", got, "credits-ok:"+eligibleID+":"+model)
	}

	streamRes, okStream, errStream := m.tryAntigravityCreditsExecuteStreamCandidates(context.Background(), cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true}, model, candidates)
	if errStream != nil || !okStream || streamRes == nil {
		t.Fatalf("tryAntigravityCreditsExecuteStreamCandidates() = (res=%v, ok=%v, err=%v), want (non-nil, true, nil)", streamRes != nil, okStream, errStream)
	}
	var streamed []byte
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		streamed = append(streamed, chunk.Payload...)
	}
	if got := string(streamed); got != "credits-stream-ok:"+eligibleID+":"+model {
		t.Fatalf("stream payload = %q, want %q", got, "credits-stream-ok:"+eligibleID+":"+model)
	}

	prepared := exec.PrepareAuthIDs()
	if len(prepared) != 2 || prepared[0] != eligibleID || prepared[1] != eligibleID {
		t.Fatalf("PrepareAuthIDs() = %v, want [%s %s] (zero-model credential must never trigger PrepareRequestAuth)", prepared, eligibleID, eligibleID)
	}
}
