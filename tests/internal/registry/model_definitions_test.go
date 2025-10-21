package registry_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestGetZhipuModels(t *testing.T) {
	models := registry.GetZhipuModels()
	if len(models) == 0 {
		t.Fatalf("expected non-empty zhipu models")
	}
	found45 := false
	found46 := false
	for _, m := range models {
		if m == nil {
			continue
		}
		if m.ID == "glm-4.5" {
			found45 = true
		}
		if m.ID == "glm-4.6" {
			found46 = true
		}
		if m.OwnedBy != "zhipu" || m.Type != "zhipu" {
			t.Errorf("expected zhipu owner/type, got %q/%q", m.OwnedBy, m.Type)
		}
	}
	if !found45 || !found46 {
		t.Errorf("expected glm-4.5 and glm-4.6 present, got: 4.5=%v 4.6=%v", found45, found46)
	}
}
