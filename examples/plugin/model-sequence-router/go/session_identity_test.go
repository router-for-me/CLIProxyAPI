package main

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// testIdentitySalt returns the stable fingerprint salt used by identity tests.
func testIdentitySalt() []byte {
	return []byte("model-sequence-router-identity-salt")
}

// TestConversationIdentityHoldsWhenPromptCacheKeyAppears proves a conversation keeps
// one cursor key when a prompt cache key joins its later turns.
func TestConversationIdentityHoldsWhenPromptCacheKeyAppears(t *testing.T) {
	opening := newConversationIdentity(pluginapi.ModelRouteRequest{
		SourceFormat: "openai-response",
		Body:         []byte(`{"input":[{"role":"user","content":"opening"}]}`),
	})
	cached := newConversationIdentity(pluginapi.ModelRouteRequest{
		SourceFormat: "openai-response",
		Body:         []byte(`{"prompt_cache_key":"cache-lane","input":[{"role":"user","content":"opening"},{"role":"assistant","content":"answer"},{"role":"user","content":"second turn"}]}`),
	})

	if opening.source() != identitySourceDerived || cached.source() != identitySourceDerived {
		t.Fatalf("identity sources = %q / %q, want %q", opening.source(), cached.source(), identitySourceDerived)
	}
	if opening != cached {
		t.Fatalf("identity drifted when the prompt cache key appeared: %q / %q", opening, cached)
	}
}

// TestConversationsSharingPromptCacheKeyStayDistinct proves a shared cache lane cannot
// merge two conversation cursor keys.
func TestConversationsSharingPromptCacheKeyStayDistinct(t *testing.T) {
	first := newConversationIdentity(pluginapi.ModelRouteRequest{
		SourceFormat: "openai-response",
		Body:         []byte(`{"prompt_cache_key":"shared-lane","input":[{"role":"user","content":"first conversation"}]}`),
	})
	second := newConversationIdentity(pluginapi.ModelRouteRequest{
		SourceFormat: "openai-response",
		Body:         []byte(`{"prompt_cache_key":"shared-lane","input":[{"role":"user","content":"second conversation"}]}`),
	})
	if first == second {
		t.Fatalf("conversations sharing one cache lane share identity %q", first)
	}
}

// TestPromptCacheKeyAloneNeverKeysAConversation proves a cache-only signal yields a
// content-derived cursor key rather than a cache-lane key.
func TestPromptCacheKeyAloneNeverKeysAConversation(t *testing.T) {
	identity := newConversationIdentity(pluginapi.ModelRouteRequest{
		SourceFormat: "openai-response",
		Body:         []byte(`{"prompt_cache_key":"cache-lane","input":[{"role":"user","content":"opening"}]}`),
	})
	if identity.source() != identitySourceDerived {
		t.Fatalf("identity source = %q, want %q", identity.source(), identitySourceDerived)
	}
	if strings.Contains(string(identity), "cache-lane") {
		t.Fatalf("cache lane reached the cursor key: %q", identity)
	}
}

// TestDerivedIdentityHoldsAcrossTurns proves protocol-aware derivation stays fixed as
// later assistant and user turns extend the transcript.
func TestDerivedIdentityHoldsAcrossTurns(t *testing.T) {
	opening := newConversationIdentity(pluginapi.ModelRouteRequest{
		Body: []byte(`{"system":"shared instructions","messages":[{"role":"user","content":"opening"}]}`),
	})
	answered := newConversationIdentity(pluginapi.ModelRouteRequest{
		Body: []byte(`{"system":"shared instructions","messages":[{"role":"user","content":"opening"},{"role":"assistant","content":"answer"},{"role":"user","content":"second turn"}]}`),
	})

	if opening.source() != identitySourceDerived || answered.source() != identitySourceDerived {
		t.Fatalf("identity sources = %q / %q, want %q", opening.source(), answered.source(), identitySourceDerived)
	}
	if opening != answered {
		t.Fatalf("identity drifted between turns: %q / %q", opening, answered)
	}
}

// TestDistinctOpeningsProduceDistinctIdentities proves different first user content
// separates two conversations.
func TestDistinctOpeningsProduceDistinctIdentities(t *testing.T) {
	first := newConversationIdentity(pluginapi.ModelRouteRequest{
		Body: []byte(`{"messages":[{"role":"user","content":"first conversation"}]}`),
	})
	second := newConversationIdentity(pluginapi.ModelRouteRequest{
		Body: []byte(`{"messages":[{"role":"user","content":"second conversation"}]}`),
	})
	if first == second {
		t.Fatalf("distinct conversations share identity %q", first)
	}
}

// TestAbsentContentYieldsNoIdentity proves a request with no user input remains
// stateless and creates no cursor key.
func TestAbsentContentYieldsNoIdentity(t *testing.T) {
	identity := newConversationIdentity(pluginapi.ModelRouteRequest{Body: []byte(`{"model":"routed"}`)})
	if identity != "" || identity.source() != identitySourceAbsent {
		t.Fatalf("identity = %q with source %q, want an absent identity", identity, identity.source())
	}
}

// TestTurnIdentityRequiresHistory proves only recognized history shapes participate in
// replay detection, while absent history never compares equal.
func TestTurnIdentityRequiresHistory(t *testing.T) {
	salt := testIdentitySalt()
	absent := newTurnIdentity(inspectRequest([]byte(`{"system":"instructions only"}`), salt))
	if absent != nil {
		t.Fatalf("turn identity = %#v, want none", absent)
	}
	if absent.equals(newTurnIdentity(inspectRequest([]byte(`{"system":"instructions only"}`), salt))) {
		t.Fatal("two absent identities compared equal")
	}
	opening := newTurnIdentity(inspectRequest([]byte(`{"messages":[{"role":"user","content":"a"}]}`), salt))
	repeated := newTurnIdentity(inspectRequest([]byte(`{"messages":[{"role":"user","content":"a"}]}`), salt))
	extended := newTurnIdentity(inspectRequest([]byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`), salt))
	if !opening.equals(repeated) {
		t.Fatal("identical conversation states compared unequal")
	}
	if opening.equals(extended) {
		t.Fatal("extended conversation state compared equal")
	}
	if opening.equals(nil) {
		t.Fatal("present identity matched an absent one")
	}
}
