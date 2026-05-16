package registry

import "testing"

func TestEmbeddedModelsCatalogIsValid(t *testing.T) {
	if err := loadModelsFromBytes(embeddedModelsJSON, "embed"); err != nil {
		t.Fatalf("embedded models.json failed validation: %v", err)
	}
}
