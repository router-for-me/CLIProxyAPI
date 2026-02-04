package registry

import (
	"strings"
	"testing"
)

func TestRovoModelsHavePrefix(t *testing.T) {
	models := GetRovoModels()
	if len(models) == 0 {
		t.Fatal("GetRovoModels returned no models")
	}

	for _, m := range models {
		if !strings.HasPrefix(m.ID, "rovo-") {
			t.Errorf("Model ID %q does not start with prefix 'rovo-'", m.ID)
		}
	}
}
