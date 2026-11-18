package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MoeForge/moego/json"
	"gopkg.in/yaml.v3"
)

const payloadReplaceFixture = `config-version: 8
requests:
  payload:
    replace:
      - models:
          - name: "*"
            protocol: claude
            from-protocol: openai
            headers: {X-Tier: "prod-*"}
            match: [{stage: ready}]
            not-match: [{blocked: true}]
            exist: [messages]
            not-exist: [disabled]
        target:
          role: user
          positions: [1, 2, 2]
        replacements:
          - "hero": "fool"
          - "fool": "friend"
          - "remove": ""
          - pattern: '(?i)\bvillain\b'
            replacement: "rival"
          - pattern: '(?s)<debug>.*?</debug>'
            replacement: ""
`

func TestPayloadReplaceConfigRoundTrip(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(payloadReplaceFixture))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if len(cfg.Payload.Replace) != 1 {
		t.Fatalf("missing replace config: %#v", cfg.Payload)
	}
	rule := cfg.Payload.Replace[0]
	if rule.Replacements[0]["hero"] != "fool" || rule.Replacements[1]["fool"] != "friend" || rule.Replacements[2]["remove"] != "" ||
		rule.Replacements[3]["pattern"] != `(?i)\bvillain\b` || rule.Replacements[3]["replacement"] != "rival" ||
		rule.Replacements[4]["pattern"] != `(?s)<debug>.*?</debug>` || rule.Replacements[4]["replacement"] != "" {
		t.Fatalf("replacement order/empty value lost: %#v", rule.Replacements)
	}
	if rule.Models[0].Protocol != "claude" || rule.Models[0].FromProtocol != "openai" {
		t.Fatalf("lost protocol gates: %#v", rule.Models)
	}
	for _, format := range []string{"yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			var data []byte
			var errMarshal error
			var got PayloadConfig
			if format == "yaml" {
				data, errMarshal = yaml.Marshal(cfg.Payload)
				if errMarshal == nil {
					errMarshal = yaml.Unmarshal(data, &got)
				}
			} else {
				data, errMarshal = json.Marshal(cfg.Payload)
				if errMarshal == nil {
					errMarshal = json.Unmarshal(data, &got)
				}
			}
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if !reflect.DeepEqual(cfg.Payload.Replace, got.Replace) {
				t.Fatalf("round trip changed config: %s", data)
			}
		})
	}
	clone := cfg.CloneForRuntime()
	clone.Payload.Replace[0].Target.Positions[0] = 9
	clone.Payload.Replace[0].Replacements[0]["hero"] = "changed"
	clone.Payload.Replace[0].Models[0].Headers["X-Tier"] = "changed"
	if cfg.Payload.Replace[0].Target.Positions[0] != 1 || cfg.Payload.Replace[0].Replacements[0]["hero"] != "fool" || cfg.Payload.Replace[0].Models[0].Headers["X-Tier"] != "prod-*" {
		t.Fatal("runtime clone shares replacement state")
	}

	file := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(file, []byte(payloadReplaceFixture), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errSave := SaveConfigPreserveComments(file, cfg, true); errSave != nil {
		t.Fatal(errSave)
	}
	reloaded, errLoad := LoadConfig(file)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if !reflect.DeepEqual(cfg.Payload.Replace, reloaded.Payload.Replace) {
		t.Fatalf("save/load changed replacement config: %#v", reloaded.Payload.Replace)
	}
	// Both selector transitions and literal/pattern transitions must discard stale YAML keys.
	for i, replacements := range [][]PayloadReplacement{
		{{"pattern": `user-(\d+)`, "replacement": "account-${1}"}},
		{{"new-source": "new-destination"}},
	} {
		target := PayloadReplaceTarget{Role: "user", All: true, Positions: []int{}}
		if i == 1 {
			target = PayloadReplaceTarget{Role: "user", Positions: []int{2}}
		}
		cfg.Payload.Replace[0].Target = target
		cfg.Payload.Replace[0].Replacements = replacements
		if errSave := SaveConfigPreserveComments(file, cfg, true); errSave != nil {
			t.Fatal(errSave)
		}
		reloaded, errLoad = LoadConfig(file)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		if !reflect.DeepEqual(cfg.Payload.Replace, reloaded.Payload.Replace) {
			t.Fatalf("selector/key edit retained stale config: %#v", reloaded.Payload.Replace)
		}
	}
}

func TestPayloadReplaceSavePreservesEmptyReplacements(t *testing.T) {
	for _, initial := range []struct {
		name, yaml string
	}{
		{"new-payload", "port: 8317\n"},
		{"new-replace", "payload: {}\n"},
		{"empty-replace", "payload: {replace: []}\n"},
		{"new-replacements", "payload:\n  replace:\n    - models: [{name: '*'}]\n      target: {role: user, all: true}\n"},
		{"existing-replacements", payloadReplaceFixture},
		{"v8-new-payload", "config-version: 8\nserver: {port: 8317}\n"},
	} {
		for _, replacements := range []struct {
			name   string
			values []PayloadReplacement
		}{
			{"delete-only", []PayloadReplacement{{"remove": ""}}},
			{"pattern-delete-only", []PayloadReplacement{{"pattern": `(?s)<debug>.*?</debug>`, "replacement": ""}}},
			{"mixed", []PayloadReplacement{{"hero": "fool"}, {"remove": ""}, {"pattern": `(?i)\bfool\b`, "replacement": "friend"}}},
		} {
			for _, migrate := range []bool{false, true} {
				layout := "preserve-layout"
				if migrate {
					layout = "migrate-v8"
				}
				t.Run(initial.name+"/"+replacements.name+"/"+layout, func(t *testing.T) {
					cfg, errParse := ParseConfigBytes([]byte(payloadReplaceFixture))
					if errParse != nil {
						t.Fatal(errParse)
					}
					cfg.Payload.Replace[0].Replacements = replacements.values
					file := filepath.Join(t.TempDir(), "config.yaml")
					if errWrite := os.WriteFile(file, []byte(initial.yaml), 0600); errWrite != nil {
						t.Fatal(errWrite)
					}
					// Verify both initial insertion and subsequent saves preserve deletion operands.
					for save := 0; save < 2; save++ {
						if errSave := SaveConfigPreserveComments(file, cfg, migrate); errSave != nil {
							t.Fatalf("save %d: %v", save+1, errSave)
						}
						reloaded, errLoad := LoadConfig(file)
						if errLoad != nil {
							t.Fatalf("reload %d: %v", save+1, errLoad)
						}
						if !reflect.DeepEqual(cfg.Payload.Replace, reloaded.Payload.Replace) {
							t.Fatalf("save %d changed replacement rules: got %#v, want %#v", save+1, reloaded.Payload.Replace, cfg.Payload.Replace)
						}
						cfg = reloaded
					}
				})
			}
		}
	}
}

func TestPayloadReplaceConfigValidation(t *testing.T) {
	valid := PayloadReplaceRule{Models: []PayloadModelRule{{Name: "*"}}, Target: PayloadReplaceTarget{Role: "user", Positions: []int{1, 2}}, Replacements: []PayloadReplacement{{"hero": "fool"}}}
	for _, tt := range []struct {
		name   string
		mutate func(*PayloadReplaceRule)
	}{
		{"missing role", func(r *PayloadReplaceRule) { r.Target.Role = "" }},
		{"assistant role", func(r *PayloadReplaceRule) { r.Target.Role = "assistant" }},
		{"tool role", func(r *PayloadReplaceRule) { r.Target.Role = "tool" }},
		{"other role", func(r *PayloadReplaceRule) { r.Target.Role = "model" }},
		{"case sensitive role", func(r *PayloadReplaceRule) { r.Target.Role = "User" }},
		{"missing target", func(r *PayloadReplaceRule) { r.Target.Positions = nil }},
		{"zero", func(r *PayloadReplaceRule) { r.Target.Positions = []int{0} }},
		{"negative", func(r *PayloadReplaceRule) { r.Target.Positions = []int{-1} }},
		{"conflicting target", func(r *PayloadReplaceRule) { r.Target.All = true }},
		{"no replacements", func(r *PayloadReplaceRule) { r.Replacements = nil }},
		{"empty mapping", func(r *PayloadReplaceRule) { r.Replacements = []PayloadReplacement{{}} }},
		{"multiple mappings", func(r *PayloadReplaceRule) { r.Replacements = []PayloadReplacement{{"a": "b", "c": "d"}} }},
		{"three mappings", func(r *PayloadReplaceRule) {
			r.Replacements = []PayloadReplacement{{"pattern": "a", "replacement": "b", "extra": "c"}}
		}},
		{"partial pattern mapping", func(r *PayloadReplaceRule) { r.Replacements = []PayloadReplacement{{"pattern": "a", "other": "b"}} }},
		{"empty source", func(r *PayloadReplaceRule) { r.Replacements = []PayloadReplacement{{"": "b"}} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rule := valid
			tt.mutate(&rule)
			if rule.Validate() == nil {
				t.Fatal("invalid rule accepted")
			}
			cfg := &Config{Payload: PayloadConfig{Replace: []PayloadReplaceRule{rule, valid}}}
			cfg.SanitizePayloadRules()
			if len(cfg.Payload.Replace) != 1 || !reflect.DeepEqual(cfg.Payload.Replace[0], valid) {
				t.Fatalf("sanitizer lost valid rule: %#v", cfg.Payload.Replace)
			}
		})
	}
	for _, role := range []string{"system", "developer", "user"} {
		for _, all := range []bool{false, true} {
			rule := valid
			rule.Target = PayloadReplaceTarget{Role: role, All: all}
			if !all {
				rule.Target.Positions = []int{1, 2}
			}
			if errValidate := rule.Validate(); errValidate != nil {
				t.Fatalf("role=%s all=%v: %v", role, all, errValidate)
			}
			cfg := &Config{Payload: PayloadConfig{Replace: []PayloadReplaceRule{rule}}}
			cfg.SanitizePayloadRules()
			if len(cfg.Payload.Replace) != 1 {
				t.Fatalf("sanitizer dropped role=%s all=%v", role, all)
			}
		}
	}
	valid.Target = PayloadReplaceTarget{Role: "user", All: true}
	valid.Replacements = []PayloadReplacement{
		{" ": ""},
		{"pattern": "schema"},
		{"pattern": `(?i)\bhero\b`, "replacement": ""},
		{"pattern": " ", "replacement": ""},
		// Regex syntax is handled per entry without rejecting the entire rule.
		{"pattern": "", "replacement": "b"},
		{"pattern": "[invalid", "replacement": ""},
	}
	if errValidate := valid.Validate(); errValidate != nil {
		t.Fatal(errValidate)
	}
}

func TestPayloadReplaceRejectsNonIntegerPositions(t *testing.T) {
	for _, input := range []string{`{role: user, positions: [1.5]}`, `{role: user, positions: ["1"]}`, `{role: user, positions: [true]}`, `{role: user, positions: [null]}`, `{role: user, positions: [999999999999999999999999999]}`} {
		var target PayloadReplaceTarget
		if errDecode := yaml.Unmarshal([]byte(input), &target); errDecode == nil {
			t.Fatalf("accepted noninteger or overflowing positions: %s", input)
		}
	}
}

func TestPayloadReplacementRejectsInvalidYAML(t *testing.T) {
	for _, input := range []string{`{1: "b"}`, `{"a": 1}`, `{"a": true}`, `{"a": null}`, `{"a": []}`, `{"a": "b", "c": "d"}`, `{"a": "b", "a": "c"}`, `{"pattern": "a", "pattern": "b"}`, `{"pattern": "a", "other": "b"}`, `{"pattern": "a", "replacement": null}`, `{"pattern": 1, "replacement": "b"}`, `{"pattern": "a", "replacement": "b", "extra": "c"}`, `[]`, `"a"`} {
		var replacement PayloadReplacement
		if errDecode := yaml.Unmarshal([]byte(input), &replacement); errDecode == nil {
			t.Fatalf("accepted invalid mapping %s", input)
		}
	}
	for _, input := range []string{`{"a":1}`, `{"a":true}`, `{"a":null}`, `null`, `{}`, `{"a":"b","c":"d"}`, `{"pattern":"a","other":"b"}`, `{"pattern":"a","replacement":null}`, `{"pattern":1,"replacement":"b"}`, `{"pattern":"a","replacement":"b","extra":"c"}`} {
		var replacement PayloadReplacement
		if errDecode := json.Unmarshal([]byte(input), &replacement); errDecode == nil {
			t.Fatalf("accepted nonstring JSON %s", input)
		}
	}
	// Numeric-looking text remains valid when explicitly quoted.
	var replacement PayloadReplacement
	if errDecode := yaml.Unmarshal([]byte(`{"1": "false"}`), &replacement); errDecode != nil {
		t.Fatal(errDecode)
	}
	// Pattern and replacement also accept YAML scalar aliases.
	var aliasRule struct {
		Replacements []PayloadReplacement `yaml:"replacements"`
	}
	if errDecode := yaml.Unmarshal([]byte("replacements:\n  - pattern: &p '(?i)hero'\n    replacement: ''\n  - pattern: *p\n    replacement: friend\n"), &aliasRule); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(aliasRule.Replacements) != 2 || aliasRule.Replacements[1]["pattern"] != "(?i)hero" || aliasRule.Replacements[1]["replacement"] != "friend" {
		t.Fatalf("unexpected alias decode: %#v", aliasRule.Replacements)
	}
}
