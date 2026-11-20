package config

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestPayloadReplaceInvalidRegexIsolation(t *testing.T) {
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel, previousOutput := logger.GetLevel(), logger.Out
	hook := logtest.NewLocal(logger)
	logger.SetLevel(log.WarnLevel)
	logger.SetOutput(io.Discard)
	t.Cleanup(func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetLevel(previousLevel)
		logger.SetOutput(previousOutput)
	})

	raw := []byte(`config-version: 8
requests:
  payload:
    replace:
      - models: [{name: '*'}]
        target: {role: user, all: true}
        replacements:
          - hero: friend
          - pattern: '[PRIVATE-FIRST'
            replacement: PRIVATE-DESTINATION
          - pattern: '(friend)'
            replacement: '${1}!'
          - pattern: ''
            replacement: PRIVATE-EMPTY-DESTINATION
          - pattern: schema
      - models: [{name: '*'}]
        target: {role: user, all: true}
        replacements:
          - pattern: '[PRIVATE-SECOND'
            replacement: PRIVATE-SECOND-DESTINATION
      - models: [{name: '*'}]
        target: {role: user, all: true}
        replacements:
          - 'friend!': ally
`)
	file := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(file, raw, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	for _, loader := range []string{"parse", "load"} {
		t.Run(loader, func(t *testing.T) {
			hook.Reset()
			var cfg *Config
			var errLoad error
			if loader == "parse" {
				cfg, errLoad = ParseConfigBytes(raw)
			} else {
				cfg, errLoad = LoadConfig(file)
			}
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			if len(cfg.Payload.Replace) != 2 {
				t.Fatalf("valid rules were lost: %#v", cfg.Payload.Replace)
			}
			want := []PayloadReplacement{{"hero": "friend"}, {"pattern": "(friend)", "replacement": "${1}!"}, {"pattern": "schema"}}
			if !reflect.DeepEqual(cfg.Payload.Replace[0].Replacements, want) {
				t.Fatalf("valid replacements changed: %#v", cfg.Payload.Replace[0].Replacements)
			}
			if !reflect.DeepEqual(cfg.Payload.Replace[1].Replacements, []PayloadReplacement{{"friend!": "ally"}}) {
				t.Fatalf("subsequent rule changed: %#v", cfg.Payload.Replace[1])
			}
			wantWarnings := []string{
				"payload.replace rule 1 replacement 2 skipped: invalid regex",
				"payload.replace rule 1 replacement 4 skipped: invalid regex",
				"payload.replace rule 2 replacement 1 skipped: invalid regex",
			}
			entries := hook.AllEntries()
			if len(entries) != len(wantWarnings) {
				t.Fatalf("warnings = %#v, want %v", entries, wantWarnings)
			}
			for i, entry := range entries {
				if entry.Level != log.WarnLevel || entry.Message != wantWarnings[i] || len(entry.Data) != 0 {
					t.Fatalf("warning must contain only rule indexes: %#v", entry)
				}
			}
			hook.Reset()
			cfg.SanitizePayloadRules()
			if len(hook.AllEntries()) != 0 {
				t.Fatal("sanitized rules repeated warnings")
			}
		})
	}
}

func TestPayloadReplaceSanitizeDoesNotMutateSharedReplacements(t *testing.T) {
	original := []PayloadReplacement{
		{"hero": "friend"},
		{"pattern": "[invalid", "replacement": ""},
		{"friend": "ally"},
	}
	cfg := &Config{Payload: PayloadConfig{Replace: []PayloadReplaceRule{{
		Target:       PayloadReplaceTarget{Role: "user", All: true},
		Replacements: original,
	}}}}
	cfg.SanitizePayloadRules()
	if len(cfg.Payload.Replace) != 1 || len(cfg.Payload.Replace[0].Replacements) != 2 {
		t.Fatalf("valid replacements were lost: %#v", cfg.Payload.Replace)
	}
	if len(original) != 3 || original[1]["pattern"] != "[invalid" || original[2]["friend"] != "ally" {
		t.Fatalf("shared replacement slice was changed: %#v", original)
	}
}
