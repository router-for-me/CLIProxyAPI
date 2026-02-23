package thinking

import (
	"bytes"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestApplyThinking_UnknownProviderLogDoesNotExposeModel(t *testing.T) {
	var buf bytes.Buffer
	prevOut := log.StandardLogger().Out
	prevLevel := log.GetLevel()
	log.SetOutput(&buf)
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetLevel(prevLevel)
	})

	model := "sensitive-user-model"
	if _, err := ApplyThinking([]byte(`{"messages":[]}`), model, "", "unknown-provider", ""); err != nil {
		t.Fatalf("ApplyThinking returned unexpected error: %v", err)
	}

	logs := buf.String()
	if !strings.Contains(logs, "thinking: unknown provider") {
		t.Fatalf("expected unknown provider log, got %q", logs)
	}
	if strings.Contains(logs, model) {
		t.Fatalf("log output leaked model value: %q", logs)
	}
}
