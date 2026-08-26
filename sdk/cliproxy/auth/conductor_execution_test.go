package auth

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// TestRoutingKeyFromAuth covers the new routing-key helper that lets the
// per-model routing picker address a single built-in api-key upstream row
// (e.g. "claude:42") instead of collapsing it onto every other Claude
// API key auth.
func TestRoutingKeyFromAuth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		auth *Auth
		want string
	}{
		{
			name: "nil auth yields empty string",
			auth: nil,
			want: "",
		},
		{
			name: "legacy auth without provider_key returns executor key",
			auth: &Auth{Provider: "claude"},
			want: "claude",
		},
		{
			name: "compound provider_key wins over executor key",
			auth: &Auth{
				Provider:   "claude",
				Attributes: map[string]string{"provider_key": "claude:42"},
			},
			want: "claude:42",
		},
		{
			name: "compound provider_key lowercased",
			auth: &Auth{
				Provider:   "claude",
				Attributes: map[string]string{"provider_key": "CLAUDE:42"},
			},
			want: "claude:42",
		},
		{
			name: "openai-compat provider_key returned verbatim",
			auth: &Auth{
				Provider:   "openai-compatibility",
				Attributes: map[string]string{"provider_key": "openai-compatible-openrouter"},
			},
			want: "openai-compatible-openrouter",
		},
		{
			name: "whitespace-only provider_key falls back to executor key",
			auth: &Auth{
				Provider:   "claude",
				Attributes: map[string]string{"provider_key": "   "},
			},
			want: "claude",
		},
		{
			name: "vertex row with compound routing key",
			auth: &Auth{
				Provider:   "vertex",
				Attributes: map[string]string{"provider_key": "vertex:7"},
			},
			want: "vertex:7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := routingKeyFromAuth(tc.auth)
			if got != tc.want {
				t.Fatalf("routingKeyFromAuth() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestManagerLiveProviderKeysForModelIncludesCurrentCompoundAuthKey(t *testing.T) {
	model := "live-provider-key-model"
	authID := "live-provider-key-auth"
	registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "claude",
		Attributes: map[string]string{
			"provider_key": "claude:42",
		},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	got := manager.LiveProviderKeysForModel(model)
	if !slices.Contains(got, "claude:42") {
		t.Fatalf("LiveProviderKeysForModel() = %v, want claude:42", got)
	}
}

func TestManagerLiveProviderKeysForModelDoesNotInferUnsupportedCompoundAuth(t *testing.T) {
	model := "live-provider-key-oauth-model"
	oauthID := "live-provider-key-oauth-auth"
	apiKeyID := "live-provider-key-api-auth"
	registry.GetGlobalRegistry().RegisterClient(oauthID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(oauthID) })

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	for _, auth := range []*Auth{
		{ID: oauthID, Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}},
		{ID: apiKeyID, Provider: "claude", Attributes: map[string]string{"provider_key": "claude:42", "auth_kind": "api_key"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}

	got := manager.LiveProviderKeysForModel(model)
	if slices.Contains(got, "claude:42") {
		t.Fatalf("LiveProviderKeysForModel() = %v, must not infer unsupported claude:42", got)
	}
	if !slices.Contains(got, "claude") {
		t.Fatalf("LiveProviderKeysForModel() = %v, want bare OAuth claude", got)
	}
}

// TestExecutorKeyFromRoutingKey confirms the conductor can translate a
// routing pin like "claude:42" back into the "claude" channel key the
// executor manager registers executors under.
func TestExecutorKeyFromRoutingKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"compound claude key", "claude:42", "claude"},
		{"compound gemini key with whitespace", "  GEMINI:7  ", "gemini"},
		{"compound vertex key", "vertex:11", "vertex"},
		{"compound interactions key", "gemini-interactions:5", "gemini-interactions"},
		{"colon in openai-compat provider name is preserved", "openai-compatible-foo:bar", "openai-compatible-foo:bar"},
		{"colon in custom provider key is preserved", "custom:row", "custom:row"},
		{"nonnumeric built-in prefix is preserved", "claude:plugin", "claude:plugin"},
		{"zero built-in row id is preserved", "claude:0", "claude:0"},
		{"bare channel key passes through", "claude", "claude"},
		{"empty input returns", "", ""},
		{"only-colon input returns empty (no channel prefix)", ":", ""},
		{"colon at position 0 returns empty", ":42", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := executorKeyFromRoutingKey(tc.in)
			if got != tc.want {
				t.Fatalf("executorKeyFromRoutingKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAuthMatchesProvider covers the cross-version compatibility helper
// that lets a route pinned to bare "claude" still serve every Claude API
// key auth — both the legacy no-attribute auths and the new per-row
// compound-key auths from v7.2.138-0.1.2.
func TestAuthMatchesProvider(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		auth *Auth
		p    string
		want bool
	}{
		{
			name: "exact compound matches",
			auth: &Auth{
				Provider:   "claude",
				Attributes: map[string]string{"provider_key": "claude:42"},
			},
			p:    "claude:42",
			want: true,
		},
		{
			name: "different compound does not match",
			auth: &Auth{
				Provider:   "claude",
				Attributes: map[string]string{"provider_key": "claude:42"},
			},
			p:    "claude:99",
			want: false,
		},
		{
			name: "bare pin matches legacy no-attribute auth",
			auth: &Auth{Provider: "claude"},
			p:    "claude",
			want: true,
		},
		{
			name: "bare pin matches per-row compound auth (cross-version wildcard)",
			auth: &Auth{
				Provider:   "claude",
				Attributes: map[string]string{"provider_key": "claude:42"},
			},
			p:    "claude",
			want: true,
		},
		{
			name: "bare pin does not wildcard a nonnumeric built-in-looking key",
			auth: &Auth{
				Provider:   "claude",
				Attributes: map[string]string{"provider_key": "claude:plugin"},
			},
			p:    "claude",
			want: false,
		},
		{
			name: "bare pin for one channel does not match another channel's auth",
			auth: &Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"provider_key": "gemini:3"},
			},
			p:    "claude",
			want: false,
		},
		{
			name: "empty provider never matches",
			auth: &Auth{Provider: "claude"},
			p:    "",
			want: false,
		},
		{
			name: "different channel's compound never matches",
			auth: &Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"provider_key": "gemini:3"},
			},
			p:    "claude:42",
			want: false,
		},
		{
			name: "whitespace in provider is trimmed",
			auth: &Auth{
				Provider:   "claude",
				Attributes: map[string]string{"provider_key": "claude:42"},
			},
			p:    "  claude:42  ",
			want: true,
		},
		{
			name: "openai-compat per-name still matches exactly",
			auth: &Auth{
				Provider:   "openai-compatibility",
				Attributes: map[string]string{"provider_key": "openai-compatible-openrouter", "compat_name": "openrouter"},
			},
			p:    "openai-compatible-openrouter",
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := authMatchesProvider(tc.auth, tc.p)
			if got != tc.want {
				t.Fatalf("authMatchesProvider(%v, %q) = %v, want %v", tc.auth, tc.p, got, tc.want)
			}
		})
	}
}

// TestAuthMatchesAnyProvider guards the set-iteration helper used by the
// conductor's candidate filtering loops.
func TestAuthMatchesAnyProvider(t *testing.T) {
	t.Parallel()
	auth := &Auth{
		Provider:   "claude",
		Attributes: map[string]string{"provider_key": "claude:42"},
	}
	set := map[string]struct{}{
		"claude:99": {},
		"claude":    {},
	}
	if !authMatchesAnyProvider(auth, set) {
		t.Fatal("authMatchesAnyProvider() = false, want true (bare \"claude\" wildcard)")
	}

	onlyOtherChannel := map[string]struct{}{"gemini:1": {}, "gemini": {}}
	if authMatchesAnyProvider(auth, onlyOtherChannel) {
		t.Fatal("authMatchesAnyProvider() = true, want false (only gemini keys in set)")
	}

	exact := map[string]struct{}{"claude:42": {}}
	if !authMatchesAnyProvider(auth, exact) {
		t.Fatal("authMatchesAnyProvider() = false, want true (exact compound match)")
	}

	empty := map[string]struct{}{}
	if authMatchesAnyProvider(auth, empty) {
		t.Fatal("authMatchesAnyProvider() with empty set = true, want false")
	}

	if authMatchesAnyProvider(nil, map[string]struct{}{"claude": {}}) {
		t.Fatal("authMatchesAnyProvider(nil, ...) = true, want false")
	}
}

// TestAuthMatchesProviderCaseInsensitive ensures the routing pin survives
// whatever the dashboard or a hand-edited route does to the casing.
func TestAuthMatchesProviderCaseInsensitive(t *testing.T) {
	t.Parallel()
	auth := &Auth{
		Provider:   "claude",
		Attributes: map[string]string{"provider_key": "claude:42"},
	}
	if !authMatchesProvider(auth, "CLAUDE:42") {
		t.Fatal("uppercase pin should match lowercase auth key")
	}
	if !authMatchesProvider(auth, "Claude") {
		t.Fatal("mixed-case bare pin should match lowercase auth key")
	}
}

// helper used by some sub-tests above to keep assertions readable.
var _ = strings.TrimSpace

// TestAuthMatchesProviderOpenAIEntryKey pins down the entry-level routing
// branch introduced for OpenAI-compat entries: an exact entry-level pin must
// match the auth that carries that entry_provider_key, but the same auth must
// NOT match a built-in channel pin, and a built-in auth must NOT match an
// OpenAI entry-level pin either.
func TestAuthMatchesProviderOpenAIEntryKey(t *testing.T) {
	t.Parallel()

	openAICompat := &Auth{
		Provider: "openai-compatible-foo",
		Attributes: map[string]string{
			"provider_key":            "openai-compatible-foo",
			AttributeEntryProviderKey: "openai-compatible-foo:bar",
		},
	}
	if !authMatchesProvider(openAICompat, "openai-compatible-foo:bar") {
		t.Fatal(`entry pin "openai-compatible-foo:bar" must match OpenAI-compat auth carrying that entry_provider_key`)
	}
	if !authMatchesProvider(openAICompat, "OPENAI-COMPATIBLE-FOO:BAR") {
		t.Fatal("entry pin must match case-insensitively")
	}
	if authMatchesProvider(openAICompat, "claude") {
		t.Fatal(`OpenAI-compat auth must NOT match a built-in "claude" pin`)
	}
	if authMatchesProvider(openAICompat, "claude:42") {
		t.Fatal(`OpenAI-compat auth must NOT match a built-in compound pin`)
	}

	// Built-in auth must not be considered "live" for an OpenAI entry-level pin.
	claudeAuth := &Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"provider_key": "claude:42",
		},
	}
	if authMatchesProvider(claudeAuth, "openai-compatible-foo:bar") {
		t.Fatal(`built-in claude auth must NOT match an OpenAI entry pin`)
	}

	// Persisted but unnamed row fallback "key-<id>" form.
	persisted := &Auth{
		Provider: "openai-compatible-foo",
		Attributes: map[string]string{
			"provider_key":            "openai-compatible-foo",
			AttributeEntryProviderKey: "openai-compatible-foo:key-17",
		},
	}
	if !authMatchesProvider(persisted, "openai-compatible-foo:key-17") {
		t.Fatal(`"openai-compatible-foo:key-17" must match an OpenAI-compat auth carrying that fallback entry_provider_key`)
	}
}

// TestManagerLiveProviderKeysForModelIncludesOpenAIEntryKey confirms the
// picker exposes named/persisted OpenAI-compat entries as their own live
// rows (in addition to the provider-level row) so operators can pin a model
// to one specific entry.
func TestManagerLiveProviderKeysForModelIncludesOpenAIEntryKey(t *testing.T) {
	model := "live-provider-entry-model"
	entryAuthID := "live-provider-entry-auth"
	providerOnlyAuthID := "live-provider-only-auth"
	registry.GetGlobalRegistry().RegisterClient(entryAuthID, "openai-compatible-foo:bar", []*registry.ModelInfo{{ID: model}})
	registry.GetGlobalRegistry().RegisterClient(providerOnlyAuthID, "openai-compatible-foo", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(entryAuthID)
		registry.GetGlobalRegistry().UnregisterClient(providerOnlyAuthID)
	})

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	for _, auth := range []*Auth{
		{
			ID:       entryAuthID,
			Provider: "openai-compatible-foo",
			Attributes: map[string]string{
				"provider_key":            "openai-compatible-foo",
				AttributeEntryProviderKey: "openai-compatible-foo:bar",
			},
		},
		{
			ID:       providerOnlyAuthID,
			Provider: "openai-compatible-foo",
			Attributes: map[string]string{
				"provider_key": "openai-compatible-foo",
			},
		},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}

	got := manager.LiveProviderKeysForModel(model)
	if !slices.Contains(got, "openai-compatible-foo:bar") {
		t.Fatalf("LiveProviderKeysForModel() = %v, want entry pin \"openai-compatible-foo:bar\"", got)
	}
	if !slices.Contains(got, "openai-compatible-foo") {
		t.Fatalf("LiveProviderKeysForModel() = %v, want provider-level pin \"openai-compatible-foo\"", got)
	}
}
