package executor

import (
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityModelEnumLookupHomeDispatch(t *testing.T) {
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)
	const model = "gemini-2.5-pro"
	const enum = "MODEL_GOOGLE_GEMINI_2_5_PRO"
	const scopeAttribute = "antigravity_model_enum_scope"
	scope := strings.Repeat("a", 64)
	auth := &cliproxyauth.Auth{ID: "home-credential", Provider: "antigravity", RegistrationEpoch: 19, Attributes: map[string]string{scopeAttribute: scope}}
	key := "antigravity:model-enums:v1:" + kvstore.HashKeyPart(auth.ID) + ":" + scope
	client.values[key] = mustAntigravityJSON(t, map[string]any{"scope": scope, "models": map[string]string{model: enum}})
	assertEnum := func(auth *cliproxyauth.Auth, modelID, want string) {
		t.Helper()
		got, ok := cliproxyauth.LookupAntigravityModelEnum(t.Context(), auth, modelID)
		if got != want || ok != (want != "") {
			t.Fatalf("lookup = %q, %v; want %q", got, ok, want)
		}
	}
	assertEnum(auth, model, enum)
	auth.RegistrationEpoch++
	assertEnum(auth, model, enum)
	assertEnum(auth, "alias", "")
	newRegistration := auth.Clone()
	newRegistration.Attributes[scopeAttribute] = strings.Repeat("b", 64)
	assertEnum(newRegistration, model, "")
	client.getErr = errors.New("KV unavailable")
	assertEnum(auth, model, "")
	if client.setCount != 0 {
		t.Fatal("enum lookup wrote to Home KV")
	}
}
