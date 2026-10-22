package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSessionAffinityGuardianParentCredential(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		provider           string
		authProvider       string
		reviewModel        string
		subagent           string
		parentID           string
		disableAffinity    bool
		excludeParentAuth  bool
		disableParentAuth  bool
		olderReviewBinding bool
		refreshOlderResult bool
		reviewProvider     string
		headerKey          string
		existingChild      bool
		fork               bool
		want               string
	}{
		{
			name: "guardian_cross_model", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", want: "auth-b",
		},
		{
			name: "guardian_custom_review_model", provider: "codex", authProvider: "codex",
			reviewModel: "custom-review-model", subagent: "guardian", parentID: "main-thread", want: "auth-b",
		},
		{
			name: "guardian_openai_provider", provider: "openai", authProvider: "openai",
			reviewModel: "custom-review-model", subagent: "guardian", parentID: "main-thread", want: "auth-b",
		},
		{
			name: "guardian_mixed_pool", provider: "mixed", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", want: "auth-b",
		},
		{
			name: "guardian_uses_latest_parent_binding", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", olderReviewBinding: true, want: "auth-b",
		},
		{
			name: "guardian_header_case_and_whitespace", provider: "codex", authProvider: "codex",
			reviewModel: "custom-review-model", subagent: "  GuArDiAn  ", headerKey: "x-openai-subagent", parentID: "main-thread", want: "auth-b",
		},
		{
			name: "guardian_mixed_parent_real_provider_child", provider: "mixed", authProvider: "codex", reviewProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", want: "auth-b",
		},
		{
			name: "guardian_real_provider_parent_mixed_child", provider: "codex", authProvider: "codex", reviewProvider: "mixed",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", want: "auth-b",
		},
		{
			name: "guardian_result_refresh_does_not_reorder_bindings", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", olderReviewBinding: true, refreshOlderResult: true, want: "auth-b",
		},
		{
			name: "guardian_existing_child_binding_wins", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", existingChild: true, want: "auth-a",
		},
		{
			name: "guardian_header_does_not_change_fork_affinity", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", fork: true, want: "auth-a",
		},
		{
			name: "ordinary_subagent_stays_model_scoped", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "collab_spawn", parentID: "main-thread", want: "auth-a",
		},
		{
			name: "review_model_without_guardian_header", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", parentID: "main-thread", want: "auth-a",
		},
		{
			name: "guardian_without_parent", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", want: "auth-a",
		},
		{
			name: "guardian_affinity_disabled", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", disableAffinity: true, want: "auth-a",
		},
		{
			name: "guardian_parent_not_candidate", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", excludeParentAuth: true, want: "auth-a",
		},
		{
			name: "guardian_parent_disabled", provider: "codex", authProvider: "codex",
			reviewModel: "codex-auto-review", subagent: "guardian", parentID: "main-thread", disableParentAuth: true, want: "auth-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enabled := !tt.disableAffinity
			selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback:         &FillFirstSelector{},
				TTL:              time.Minute,
				SubagentAffinity: &enabled,
			})
			t.Cleanup(selector.Stop)

			authA := &Auth{ID: "auth-a", Provider: tt.authProvider}
			authB := &Auth{ID: "auth-b", Provider: tt.authProvider}
			parentOpts := cliproxyexecutor.Options{
				Headers:  http.Header{"Session_id": []string{"main-thread"}},
				Metadata: map[string]any{},
			}
			olderOpts := cliproxyexecutor.Options{Headers: parentOpts.Headers, Metadata: map[string]any{}}
			if tt.olderReviewBinding {
				olderAuth, errOlder := selector.Pick(context.Background(), tt.provider, tt.reviewModel, olderOpts, []*Auth{authA})
				if errOlder != nil {
					t.Fatalf("older parent Pick() error = %v", errOlder)
				}
				if olderAuth == nil || olderAuth.ID != "auth-a" {
					t.Fatalf("older parent Pick() = %v, want auth-a", olderAuth)
				}
			}

			reviewProvider := tt.reviewProvider
			if reviewProvider == "" {
				reviewProvider = tt.provider
			}
			if tt.existingChild {
				childOpts := guardianTestOptions("review-thread", "main-thread")
				guardianTestPick(t, selector, reviewProvider, tt.reviewModel, childOpts, []*Auth{authA}, "auth-a")
			}

			// Establish the parent through the real selection path, not a seeded cache.
			parentAuth, errParent := selector.Pick(context.Background(), tt.provider, "gpt-6.1-sol", parentOpts, []*Auth{authB})
			if errParent != nil {
				t.Fatalf("parent Pick() error = %v", errParent)
			}
			if parentAuth == nil || parentAuth.ID != "auth-b" {
				t.Fatalf("parent Pick() = %v, want auth-b", parentAuth)
			}

			if tt.refreshOlderResult {
				selector.OnResult(Result{AuthID: "auth-a", Provider: tt.authProvider, Model: tt.reviewModel, Success: true, Options: olderOpts})
			}

			reviewOpts := cliproxyexecutor.Options{
				Headers: http.Header{
					"Session_id":               []string{"review-thread"},
					"X-Codex-Parent-Thread-Id": []string{tt.parentID},
					"X-Openai-Subagent":        []string{tt.subagent},
				},
				Metadata: map[string]any{},
			}
			if tt.headerKey != "" {
				delete(reviewOpts.Headers, "X-Openai-Subagent")
				reviewOpts.Headers[tt.headerKey] = []string{tt.subagent}
			}
			if tt.fork {
				reviewOpts.OriginalRequest = []byte(`{"forked_from_thread_id":"main-thread"}`)
			}
			// The real fallback chooses auth-a, making accidental fallback observable.
			reviewCandidates := []*Auth{authA, authB}
			if tt.excludeParentAuth {
				reviewCandidates = []*Auth{authA}
			}
			authB.Disabled = tt.disableParentAuth
			reviewAuth, errReview := selector.Pick(context.Background(), reviewProvider, tt.reviewModel, reviewOpts, reviewCandidates)
			if errReview != nil {
				t.Fatalf("review Pick() error = %v", errReview)
			}
			if reviewAuth == nil {
				t.Fatal("review Pick() returned nil auth")
			}
			if reviewAuth.ID != tt.want {
				t.Fatalf("review auth = %q, want %q (parent auth-b, fallback auth-a)", reviewAuth.ID, tt.want)
			}
		})
	}
}

func guardianTestOptions(sessionID, parentID string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Headers: http.Header{
			"Session_id":               []string{sessionID},
			"X-Codex-Parent-Thread-Id": []string{parentID},
			"X-Openai-Subagent":        []string{"guardian"},
		},
		Metadata: map[string]any{},
	}
}

func guardianTestPick(t *testing.T, selector *SessionAffinitySelector, provider, model string, opts cliproxyexecutor.Options, candidates []*Auth, want string) {
	t.Helper()
	auth, errPick := selector.Pick(context.Background(), provider, model, opts, candidates)
	if errPick != nil {
		t.Fatalf("Pick(%q, %q) error = %v", provider, model, errPick)
	}
	if auth == nil {
		t.Fatalf("Pick(%q, %q) returned nil, want %s", provider, model, want)
	}
	if auth.ID != want {
		t.Fatalf("Pick(%q, %q) auth = %q, want %q", provider, model, auth.ID, want)
	}
}

func TestSessionAffinityGuardianParentLifecycle(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"read_only", "expired", "invalidated", "auth_invalidated", "failed", "evicted"} {
		t.Run(action, func(t *testing.T) {
			selector := NewSessionAffinitySelector(&FillFirstSelector{})
			t.Cleanup(selector.Stop)
			authA := &Auth{ID: "auth-a", Provider: "codex"}
			authB := &Auth{ID: "auth-b", Provider: "codex"}
			parentOpts := guardianTestOptions("main-thread", "")
			guardianTestPick(t, selector, "codex", "parent-model", parentOpts, []*Auth{authB}, "auth-b")
			parentKey := "codex::codex:main-thread::parent-model"

			selector.cache.mu.RLock()
			before := selector.cache.entries[parentKey].expiresAt
			selector.cache.mu.RUnlock()
			want := "auth-a"
			switch action {
			case "read_only":
				want = "auth-b"
			case "expired":
				// Move the real binding into the past without depending on wall-clock sleeps.
				selector.cache.mu.Lock()
				entry := selector.cache.entries[parentKey]
				entry.expiresAt = time.Now().Add(-time.Hour)
				for _, alias := range entry.aliases {
					selector.cache.entries[alias] = entry
				}
				selector.cache.groups[entry.aliases[0]] = entry
				selector.cache.mu.Unlock()
			case "invalidated":
				selector.cache.Invalidate(parentKey)
			case "auth_invalidated":
				selector.InvalidateAuth("auth-b")
			case "failed":
				selector.OnResult(Result{AuthID: "auth-b", Success: false, Options: parentOpts})
			case "evicted":
				selector.cache.mu.Lock()
				selector.cache.maxEntries = 1
				selector.cache.mu.Unlock()
				guardianTestPick(t, selector, "codex", "other-model", guardianTestOptions("unrelated", ""), []*Auth{authA}, "auth-a")
			}

			reviewOpts := guardianTestOptions("review-thread", "main-thread")
			guardianTestPick(t, selector, "codex", "custom-review", reviewOpts, []*Auth{authA, authB}, want)
			if action != "read_only" {
				return
			}
			selector.OnResult(Result{AuthID: "auth-b", Success: true, Options: reviewOpts})
			selector.cache.mu.RLock()
			after := selector.cache.entries[parentKey].expiresAt
			selector.cache.mu.RUnlock()
			if !after.Equal(before) {
				t.Fatalf("parent expiry changed on inheritance: before %v, after %v", before, after)
			}

			selector.OnResult(Result{AuthID: "auth-b", Success: false, Options: reviewOpts})
			guardianTestPick(t, selector, "codex", "custom-review", reviewOpts, []*Auth{authA}, "auth-a")
			guardianTestPick(t, selector, "codex", "parent-model", parentOpts, []*Auth{authA, authB}, "auth-b")
			guardianTestPick(t, selector, "codex", "custom-review", guardianTestOptions("another-review", "main-thread"), []*Auth{authA, authB}, "auth-b")
		})
	}
}

func TestSessionAffinityGuardianMixedProviderIsolation(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	t.Cleanup(selector.Stop)
	authA := &Auth{ID: "auth-a", Provider: "codex"}
	authB := &Auth{ID: "auth-b", Provider: "codex"}
	authC := &Auth{ID: "auth-c", Provider: "openai"}
	parentOpts := guardianTestOptions("main-thread", "")
	guardianTestPick(t, selector, "mixed", "codex-parent-model", parentOpts, []*Auth{authB}, "auth-b")
	guardianTestPick(t, selector, "mixed", "openai-parent-model", parentOpts, []*Auth{authC}, "auth-c")
	guardianTestPick(t, selector, "mixed", "review-model", guardianTestOptions("codex-review", "main-thread"), []*Auth{authA, authB}, "auth-b")
	guardianTestPick(t, selector, "codex", "review-model", guardianTestOptions("direct-review", "main-thread"), []*Auth{authA, authB}, "auth-b")

	// A different real provider must not inherit the Codex-only parent's binding.
	guardianTestPick(t, selector, "mixed", "parent-model", guardianTestOptions("codex-only", ""), []*Auth{authB}, "auth-b")
	authD := &Auth{ID: "auth-d", Provider: "openai"}
	guardianTestPick(t, selector, "mixed", "review-model", guardianTestOptions("openai-review", "codex-only"), []*Auth{authC, authD}, "auth-c")
}

func TestSessionAffinityGuardianSchedulingProvider(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		provider string
		auth     Auth
	}{
		{name: "kimi_ai", provider: "kimi-ai", auth: Auth{Provider: "kimi.ai"}},
		{name: "kimi_com", provider: "kimi", auth: Auth{Provider: "kimi.com"}},
		{name: "normalized_provider", provider: "codex", auth: Auth{Provider: " Codex "}},
		{name: "compatible_attributes", provider: "openai-compatible-review", auth: Auth{
			Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "Review", "provider_key": "review"},
		}},
		{name: "compatible_label", provider: "openai-compatible-review", auth: Auth{Provider: "openai-compatibility", Label: "Review"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, route := range []struct {
				name   string
				parent string
				child  string
			}{
				{name: "direct", parent: tt.provider, child: tt.provider},
				{name: "mixed_parent", parent: "mixed", child: tt.provider},
				{name: "mixed_child", parent: tt.provider, child: "mixed"},
			} {
				t.Run(route.name, func(t *testing.T) {
					selector := NewSessionAffinitySelector(&FillFirstSelector{})
					t.Cleanup(selector.Stop)
					authA, authB := tt.auth, tt.auth
					authA.ID, authB.ID = "auth-a", "auth-b"
					guardianTestPick(t, selector, route.parent, "parent-model", guardianTestOptions("main-thread", ""), []*Auth{&authB}, "auth-b")
					guardianTestPick(t, selector, route.child, "review-model", guardianTestOptions("review-thread", "main-thread"), []*Auth{&authA, &authB}, "auth-b")
				})
			}
		})
	}
}

func TestSessionAffinityGuardianQuotaPreservedParent(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &FillFirstSelector{}, RebindOn: SessionAffinityRebindOnQuota})
	t.Cleanup(selector.Stop)
	authA := &Auth{ID: "auth-a", Provider: "codex"}
	authB := &Auth{ID: "auth-b", Provider: "codex"}
	parentOpts := guardianTestOptions("main-thread", "")
	guardianTestPick(t, selector, "codex", "parent-model", parentOpts, []*Auth{authB}, "auth-b")
	authB.ModelStates = map[string]*ModelState{
		"parent-model": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour), LastError: &Error{HTTPStatus: 503}},
	}
	guardianTestPick(t, selector, "codex", "parent-model", parentOpts, []*Auth{authA, authB}, "auth-a")
	guardianTestPick(t, selector, "codex", "review-model", guardianTestOptions("review-thread", "main-thread"), []*Auth{authA, authB}, "auth-b")
}

func TestSessionAffinityQuotaPreservedBindingAddsConversationAlias(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &FillFirstSelector{}, RebindOn: SessionAffinityRebindOnQuota})
	t.Cleanup(selector.Stop)
	authA := &Auth{ID: "auth-a", Provider: "codex"}
	authB := &Auth{ID: "auth-b", Provider: "codex"}
	prompt := cliproxyexecutor.Options{OriginalRequest: []byte(`{"prompt_cache_key":"prompt"}`)}
	combined := cliproxyexecutor.Options{OriginalRequest: []byte(`{"prompt_cache_key":"prompt","conversation":"conversation"}`)}
	conversation := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":"conversation"}`)}
	guardianTestPick(t, selector, "codex", "model", prompt, []*Auth{authB}, "auth-b")
	authB.ModelStates = map[string]*ModelState{
		"model": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour), LastError: &Error{HTTPStatus: 503}},
	}
	guardianTestPick(t, selector, "codex", "model", combined, []*Auth{authA, authB}, "auth-a")
	authB.ModelStates = nil
	guardianTestPick(t, selector, "codex", "model", conversation, []*Auth{authA, authB}, "auth-b")
}

func TestSessionAffinityGuardianCandidateEligibility(t *testing.T) {
	t.Parallel()

	for _, eligibility := range []string{"lower_priority", "model_cooldown", "zero_weight"} {
		t.Run(eligibility, func(t *testing.T) {
			var fallback Selector = &FillFirstSelector{}
			if eligibility == "zero_weight" {
				fallback = &WeightedRoundRobinSelector{}
			}
			selector := NewSessionAffinitySelector(fallback)
			t.Cleanup(selector.Stop)
			authA := &Auth{ID: "auth-a", Provider: "codex", Attributes: map[string]string{"priority": "1"}}
			authB := &Auth{ID: "auth-b", Provider: "codex", Attributes: map[string]string{"priority": "0"}}
			guardianTestPick(t, selector, "codex", "parent-model", guardianTestOptions("main-thread", ""), []*Auth{authB}, "auth-b")
			want := "auth-b"
			switch eligibility {
			case "model_cooldown":
				authB.ModelStates = map[string]*ModelState{
					"review-model": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)},
				}
				want = "auth-a"
			case "zero_weight":
				authB.Attributes["weight"] = "0"
				want = "auth-a"
			}
			guardianTestPick(t, selector, "codex", "review-model", guardianTestOptions("review-thread", "main-thread"), []*Auth{authA, authB}, want)
		})
	}
}
