package registry

import (
	"sort"
	"testing"
)

func modelIDSet(models []map[string]any) map[string]bool {
	out := make(map[string]bool, len(models))
	for _, m := range models {
		if id, ok := m["id"].(string); ok {
			out[id] = true
		}
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestGetAvailableModelsForKey_PrivateProviderHidden verifies that models served only by a
// provider with an allow-list are hidden from keys outside the list, while shared models
// (also served by a public provider) remain visible to everyone.
func TestGetAvailableModelsForKey_PrivateProviderHidden(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("public-client", "OpenAI", []*ModelInfo{
		{ID: "shared", OwnedBy: "team"},
		{ID: "pub-only", OwnedBy: "team"},
	})
	r.RegisterClient("private-client", "OpenAI", []*ModelInfo{
		{ID: "shared", OwnedBy: "team"},
		{ID: "priv-only", OwnedBy: "team"},
	})
	r.SetClientKeyACL("private-client", []string{"keyA"})

	allowed := modelIDSet(r.GetAvailableModelsForKey("openai", "keyA"))
	if !allowed["shared"] || !allowed["pub-only"] || !allowed["priv-only"] {
		t.Fatalf("allowed key should see all models, got %v", sortedKeys(allowed))
	}

	denied := modelIDSet(r.GetAvailableModelsForKey("openai", "keyB"))
	if !denied["shared"] || !denied["pub-only"] {
		t.Fatalf("denied key should still see public models, got %v", sortedKeys(denied))
	}
	if denied["priv-only"] {
		t.Fatalf("denied key must not see private-only model, got %v", sortedKeys(denied))
	}

	empty := modelIDSet(r.GetAvailableModelsForKey("openai", ""))
	if empty["priv-only"] {
		t.Fatalf("empty key must not see private-only model, got %v", sortedKeys(empty))
	}
}

// TestGetAvailableModelsForKey_NoACLUnchanged verifies that without any private providers the
// per-key listing is identical to the unfiltered listing.
func TestGetAvailableModelsForKey_NoACLUnchanged(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("c1", "OpenAI", []*ModelInfo{{ID: "m1", OwnedBy: "team"}})

	full := modelIDSet(r.GetAvailableModels("openai"))
	perKey := modelIDSet(r.GetAvailableModelsForKey("openai", "anything"))
	if len(full) != len(perKey) || !perKey["m1"] {
		t.Fatalf("expected identical listing without ACLs, full=%v perKey=%v", sortedKeys(full), sortedKeys(perKey))
	}
}

// TestIsModelAllowedForKey covers allow, deny, shared, public and unknown models.
func TestIsModelAllowedForKey(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("public-client", "OpenAI", []*ModelInfo{
		{ID: "shared", OwnedBy: "team"},
		{ID: "pub-only", OwnedBy: "team"},
	})
	r.RegisterClient("private-client", "OpenAI", []*ModelInfo{
		{ID: "shared", OwnedBy: "team"},
		{ID: "priv-only", OwnedBy: "team"},
	})
	r.SetClientKeyACL("private-client", []string{"keyA"})

	cases := []struct {
		model string
		key   string
		want  bool
	}{
		{"priv-only", "keyA", true},
		{"priv-only", "keyB", false},
		{"priv-only", "", false},
		{"shared", "keyB", true},   // also served by public provider
		{"pub-only", "keyB", true}, // public
		{"unknown", "keyB", true},  // unknown -> defer to normal not-found handling
	}
	for _, tc := range cases {
		if got := r.IsModelAllowedForKey(tc.model, tc.key); got != tc.want {
			t.Errorf("IsModelAllowedForKey(%q, %q) = %v, want %v", tc.model, tc.key, got, tc.want)
		}
	}
}

// TestSetClientKeyACL_EmptyClearsRestriction verifies that clearing the ACL makes a provider
// public again.
func TestSetClientKeyACL_EmptyClearsRestriction(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("private-client", "OpenAI", []*ModelInfo{{ID: "priv-only", OwnedBy: "team"}})
	r.SetClientKeyACL("private-client", []string{"keyA"})

	if r.IsModelAllowedForKey("priv-only", "keyB") {
		t.Fatalf("expected keyB to be denied before clearing ACL")
	}
	r.SetClientKeyACL("private-client", nil)
	if !r.IsModelAllowedForKey("priv-only", "keyB") {
		t.Fatalf("expected keyB to be allowed after clearing ACL")
	}
}
