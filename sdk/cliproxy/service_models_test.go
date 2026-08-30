package cliproxy

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestListUnprefixedModelsEffectiveValueControlsCatalogVisibility(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*config.Config)
		wantHidden bool
	}{
		{
			name:       "programmatic zero value uses default true",
			wantHidden: false,
		},
		{
			name: "programmatic explicit false hides bare alias",
			configure: func(cfg *config.Config) {
				cfg.SetListUnprefixedModels(false)
			},
			wantHidden: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := &config.Config{}
			if testCase.configure != nil {
				testCase.configure(cfg)
			}
			service := &Service{cfg: cfg}
			models := applyModelPrefixes([]*ModelInfo{{ID: "model"}}, "team", false, service.listUnprefixedModels())
			if got := models[0].HiddenFromModelCatalog; got != testCase.wantHidden {
				t.Fatalf("bare model hidden = %t, want %t", got, testCase.wantHidden)
			}
		})
	}
}

func TestApplyModelPrefixesListUnprefixedModels(t *testing.T) {
	tests := []struct {
		name                 string
		prefix               string
		forceModelPrefix     bool
		listUnprefixedModels bool
		wantIDs              []string
		wantHidden           []bool
	}{
		{
			name:                 "default lists bare and prefixed forms",
			prefix:               "team",
			listUnprefixedModels: true,
			wantIDs:              []string{"model", "team/model"},
			wantHidden:           []bool{false, false},
		},
		{
			name:                 "disabled hides bare form from catalog",
			prefix:               "team",
			listUnprefixedModels: false,
			wantIDs:              []string{"model", "team/model"},
			wantHidden:           []bool{true, false},
		},
		{
			name:             "force keeps bare form when prefix matches model",
			prefix:           "model",
			forceModelPrefix: true,
			wantIDs:          []string{"model", "model/model"},
			wantHidden:       []bool{false, false},
		},
		{
			name:             "force suppresses bare form for a different prefix",
			prefix:           "team",
			forceModelPrefix: true,
			wantIDs:          []string{"team/model"},
			wantHidden:       []bool{false},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			models := []*ModelInfo{{ID: "model"}}
			gotModels := applyModelPrefixes(models, testCase.prefix, testCase.forceModelPrefix, testCase.listUnprefixedModels)
			gotIDs := make([]string, 0, len(gotModels))
			for _, model := range gotModels {
				gotIDs = append(gotIDs, model.ID)
			}
			if !reflect.DeepEqual(gotIDs, testCase.wantIDs) {
				t.Fatalf("model IDs = %#v, want %#v", gotIDs, testCase.wantIDs)
			}
			for index, model := range gotModels {
				if model.HiddenFromModelCatalog != testCase.wantHidden[index] {
					t.Fatalf("model %q hidden = %t, want %t", model.ID, model.HiddenFromModelCatalog, testCase.wantHidden[index])
				}
			}
		})
	}
}
