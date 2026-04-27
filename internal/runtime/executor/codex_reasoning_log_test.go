package executor

import (
	"bytes"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestLogCodexFinalReasoningEffort(t *testing.T) {
	logger := log.StandardLogger()
	prevOut := logger.Out
	prevLevel := logger.GetLevel()
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLevel(log.DebugLevel)
	defer func() {
		logger.SetOutput(prevOut)
		logger.SetLevel(prevLevel)
	}()

	logCodexFinalReasoningEffort([]byte(`{"reasoning":{"effort":"low"}}`), "gpt-5.4-mini")

	got := buf.String()
	if !strings.Contains(got, "codex: final reasoning effort after payload config") {
		t.Fatalf("expected final reasoning effort log, got %q", got)
	}
	if !strings.Contains(got, "low") {
		t.Fatalf("expected final effort value in log, got %q", got)
	}
}
