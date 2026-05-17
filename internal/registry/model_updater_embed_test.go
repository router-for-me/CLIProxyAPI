package registry

import (
	"encoding/json"
	"testing"
)

func TestEmbeddedModelsCatalogIsValid(t *testing.T) {
	var parsed staticModelsJSON
	if err := json.Unmarshal(embeddedModelsJSON, &parsed); err != nil {
		t.Fatalf("embedded models.json failed to decode: %v", err)
	}
	if err := validateModelsCatalog(&parsed); err != nil {
		t.Fatalf("embedded models.json failed validation: %v", err)
	}
}
