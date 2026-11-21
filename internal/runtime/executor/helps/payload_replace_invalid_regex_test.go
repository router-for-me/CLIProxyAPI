package helps

import (
	"io"
	"reflect"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestPayloadReplaceInvalidRegexRuntimeIsolation(t *testing.T) {
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

	// SDK callers may supply configs without running the config sanitizer.
	cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{
		{
			Models: []config.PayloadModelRule{{Name: "*"}},
			Target: config.PayloadReplaceTarget{Role: "user", All: true},
			Replacements: []config.PayloadReplacement{
				{"hero": "friend"},
				{"pattern": "[PRIVATE-PATTERN", "replacement": "PRIVATE-DESTINATION"},
				{"pattern": "(friend)", "replacement": "${1}!"},
				{"pattern": "", "replacement": "PRIVATE-EMPTY-DESTINATION"},
				{"friend!": "ally"},
			},
		},
		{
			Models:       []config.PayloadModelRule{{Name: "*"}},
			Target:       config.PayloadReplaceTarget{Role: "user", All: true},
			Replacements: []config.PayloadReplacement{{"pattern": "[PRIVATE-SECOND", "replacement": "PRIVATE-SECOND-DESTINATION"}},
		},
		{
			Models:       []config.PayloadModelRule{{Name: "*"}},
			Target:       config.PayloadReplaceTarget{Role: "user", All: true},
			Replacements: []config.PayloadReplacement{{"ally": "teammate"}},
		},
	}}}
	before := cfg.CloneForRuntime()
	body := []byte(`{"messages":[{"role":"user","content":"hero"},{"role":"user","content":[{"type":"text","text":"hero"},{"type":"text","text":"hero"}]}]}`)
	out := ApplyPayloadConfigWithRoot(cfg, "model", "openai", "", body, nil, "", "")
	for _, path := range []string{"messages.0.content", "messages.1.content.0.text", "messages.1.content.1.text"} {
		if got := ojson.GetBytes(out, path).String(); got != "teammate" {
			t.Errorf("%s = %q, want teammate; body: %s", path, got, out)
		}
	}
	if !reflect.DeepEqual(cfg.Payload.Replace, before.Payload.Replace) {
		t.Fatal("runtime compilation mutated the shared config")
	}
	wantWarnings := []string{
		"payload.replace rule 1 replacement 2 skipped: invalid regex",
		"payload.replace rule 1 replacement 4 skipped: invalid regex",
		"payload.replace rule 2 replacement 1 skipped: invalid regex",
	}
	entries := hook.AllEntries()
	if len(entries) != len(wantWarnings) {
		t.Fatalf("warnings = %#v, want %v (once per invalid entry, not per text block)", entries, wantWarnings)
	}
	for i, entry := range entries {
		if entry.Level != log.WarnLevel || entry.Message != wantWarnings[i] || len(entry.Data) != 0 {
			t.Fatalf("warning must contain only rule indexes: %#v", entry)
		}
	}
}
