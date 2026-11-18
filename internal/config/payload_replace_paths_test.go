package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"gopkg.in/yaml.v3"
)

func TestPayloadReplacePathsValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target PayloadReplaceTarget
		valid  bool
	}{
		{"path", PayloadReplaceTarget{Paths: []string{"metadata.description"}}, true},
		{"query and duplicates", PayloadReplaceTarget{Paths: []string{`tools.#(type=="function")#.function.description`, "metadata.description", "metadata.description"}}, true},
		{"empty paths", PayloadReplaceTarget{Paths: []string{}}, false},
		{"empty path", PayloadReplaceTarget{Paths: []string{""}}, false},
		{"blank path", PayloadReplaceTarget{Paths: []string{"metadata.description", " \t"}}, false},
		{"role conflict", PayloadReplaceTarget{Paths: []string{"metadata.description"}, Role: "user"}, false},
		{"positions conflict", PayloadReplaceTarget{Paths: []string{"metadata.description"}, Positions: []int{1}}, false},
		{"all conflict", PayloadReplaceTarget{Paths: []string{"metadata.description"}, All: true}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rule := PayloadReplaceRule{Models: []PayloadModelRule{{Name: "*"}}, Target: tt.target, Replacements: []PayloadReplacement{{"old": "new"}}}
			if errValidate := rule.Validate(); (errValidate == nil) != tt.valid {
				t.Fatalf("Validate() = %v, valid = %v", errValidate, tt.valid)
			}
			cfg := &Config{Payload: PayloadConfig{Replace: []PayloadReplaceRule{rule}}}
			cfg.SanitizePayloadRules()
			if (len(cfg.Payload.Replace) == 1) != tt.valid {
				t.Fatalf("unexpected sanitized rules: %#v", cfg.Payload.Replace)
			}
		})
	}
}

func TestPayloadReplacePathsRejectsNonStrings(t *testing.T) {
	for _, input := range []string{`{paths: [1]}`, `{paths: [true]}`, `{paths: [null]}`, `{paths: [{}]}`, `{paths: [[]]}`, `{paths: metadata.description}`} {
		var target PayloadReplaceTarget
		if errDecode := yaml.Unmarshal([]byte(input), &target); errDecode == nil {
			t.Fatalf("accepted invalid YAML paths: %s", input)
		}
	}
	for _, input := range []string{`{"paths":[1]}`, `{"paths":[true]}`, `{"paths":[{}]}`, `{"paths":[[]]}`, `{"paths":"metadata.description"}`} {
		var target PayloadReplaceTarget
		if errDecode := json.Unmarshal([]byte(input), &target); errDecode == nil {
			t.Fatalf("accepted invalid JSON paths: %s", input)
		}
	}
	var target PayloadReplaceTarget
	if errDecode := yaml.Unmarshal([]byte("paths: [ &path metadata.description, *path, '1' ]"), &target); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !reflect.DeepEqual(target.Paths, []string{"metadata.description", "metadata.description", "1"}) {
		t.Fatalf("lost string paths or alias: %#v", target.Paths)
	}
}

func TestPayloadReplacePathsConfigRoundTrip(t *testing.T) {
	fixture := strings.Replace(payloadReplaceFixture, "          role: user\n          positions: [1, 2, 2]", "          paths:\n            - 'tools.#(type==\"function\")#.function.description'\n            - metadata.description", 1)
	cfg, errParse := ParseConfigBytes([]byte(fixture))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if len(cfg.Payload.Replace) != 1 || len(cfg.Payload.Replace[0].Target.Paths) != 2 {
		t.Fatalf("missing path target: %#v", cfg.Payload.Replace)
	}
	// YAML emits an empty sequence for the unused positions selector.
	cfg.Payload.Replace[0].Target.Positions = []int{}
	for _, format := range []string{"yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			var got PayloadConfig
			var data []byte
			var errRoundTrip error
			if format == "yaml" {
				data, errRoundTrip = yaml.Marshal(cfg.Payload)
				if errRoundTrip == nil {
					errRoundTrip = yaml.Unmarshal(data, &got)
				}
			} else {
				data, errRoundTrip = json.Marshal(cfg.Payload)
				if errRoundTrip == nil {
					errRoundTrip = json.Unmarshal(data, &got)
				}
			}
			if errRoundTrip != nil {
				t.Fatal(errRoundTrip)
			}
			if !reflect.DeepEqual(cfg.Payload.Replace, got.Replace) {
				t.Fatalf("round trip changed paths: %s", data)
			}
		})
	}
	clone := cfg.CloneForRuntime()
	clone.Payload.Replace[0].Target.Paths[0] = "changed"
	if cfg.Payload.Replace[0].Target.Paths[0] == "changed" {
		t.Fatal("runtime clone shares target paths")
	}
	pathsTarget := cfg.Payload.Replace[0].Target
	for _, migrate := range []bool{false, true} {
		file := filepath.Join(t.TempDir(), "config.yaml")
		if errWrite := os.WriteFile(file, []byte(payloadReplaceFixture), 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
		for _, target := range []PayloadReplaceTarget{
			pathsTarget,
			{Role: "user", All: true, Positions: []int{}},
			{Paths: []string{"metadata.description"}, Positions: []int{}},
			{Role: "system", Positions: []int{1}},
		} {
			cfg.Payload.Replace[0].Target = target
			if errSave := SaveConfigPreserveComments(file, cfg, migrate); errSave != nil {
				t.Fatal(errSave)
			}
			reloaded, errLoad := LoadConfig(file)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			if !reflect.DeepEqual(cfg.Payload.Replace, reloaded.Payload.Replace) {
				t.Fatalf("selector transition retained stale keys: got %#v, want %#v", reloaded.Payload.Replace, cfg.Payload.Replace)
			}
		}
	}
}
