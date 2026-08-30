package main

import (
	"net/http"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func testIdentitySalt() []byte {
	return []byte("model-sequence-router-identity-salt")
}

// testIdentity resolves the identity for one request body exactly as routing does.
func testIdentity(req pluginapi.ModelRouteRequest) conversationIdentity {
	salt := testIdentitySalt()
	return newConversationIdentity(req, inspectRequest(req.Body, salt), salt)
}

func TestDurableCoreIdentityPassesThrough(t *testing.T) {
	req := pluginapi.ModelRouteRequest{
		Headers: http.Header{"X-Session-ID": {"durable-session"}},
		Body:    []byte(`{"messages":[{"role":"user","content":"opening"}]}`),
	}
	identity := testIdentity(req)
	if identity.Source != identitySourceCore {
		t.Fatalf("identity source = %q, want %q", identity.Source, identitySourceCore)
	}
	if !strings.Contains(identity.Value, "durable-session") {
		t.Fatalf("identity value = %q, want the client-supplied session", identity.Value)
	}
}

func TestContentHashIdentityIsReplacedAndHoldsAcrossTurns(t *testing.T) {
	opening := pluginapi.ModelRouteRequest{
		Body: []byte(`{"system":"shared instructions","messages":[{"role":"user","content":"opening"}]}`),
	}
	answered := pluginapi.ModelRouteRequest{
		Body: []byte(`{"system":"shared instructions","messages":[{"role":"user","content":"opening"},{"role":"assistant","content":"answer"},{"role":"user","content":"second turn"}]}`),
	}

	coreOpening := coreauth.ExtractSessionID(opening.Headers, opening.Body, opening.Metadata)
	if !strings.HasPrefix(coreOpening, coreMessageHashPrefix) {
		t.Fatalf("core identity = %q, want a content hash fallback", coreOpening)
	}

	first := testIdentity(opening)
	second := testIdentity(answered)
	if first.Source != identitySourceDerived || second.Source != identitySourceDerived {
		t.Fatalf("identity sources = %q / %q", first.Source, second.Source)
	}
	if first.Value != second.Value {
		t.Fatalf("identity drifted between turns: %q / %q", first.Value, second.Value)
	}
	if strings.HasPrefix(first.Value, coreMessageHashPrefix) {
		t.Fatalf("core content hash survived substitution: %q", first.Value)
	}
}

func TestDistinctOpeningsProduceDistinctIdentities(t *testing.T) {
	first := testIdentity(pluginapi.ModelRouteRequest{
		Body: []byte(`{"messages":[{"role":"user","content":"first conversation"}]}`),
	})
	second := testIdentity(pluginapi.ModelRouteRequest{
		Body: []byte(`{"messages":[{"role":"user","content":"second conversation"}]}`),
	})
	if first.Value == second.Value {
		t.Fatalf("distinct conversations share identity %q", first.Value)
	}
}

func TestAbsentContentYieldsNoIdentity(t *testing.T) {
	identity := testIdentity(pluginapi.ModelRouteRequest{Body: []byte(`{"model":"routed"}`)})
	if identity.Source != identitySourceAbsent || identity.Value != "" {
		t.Fatalf("identity = %#v, want an absent identity", identity)
	}
}

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
