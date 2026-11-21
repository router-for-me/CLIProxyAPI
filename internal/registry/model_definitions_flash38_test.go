package registry

import "testing"

func TestAntigravityFlash38TiersPresent(t *testing.T) {
	ids := map[string]bool{}
	for _, m := range GetAntigravityModels() {
		ids[m.ID] = true
	}
	for _, id := range []string{"gemini-3.8-flash-high", "gemini-3.8-flash-medium", "gemini-3.8-flash-low"} {
		if !ids[id] {
			t.Fatalf("antigravity catalog is missing %s", id)
		}
		if LookupStaticModelInfo(id) == nil {
			t.Fatalf("LookupStaticModelInfo(%s) = nil", id)
		}
	}
}
