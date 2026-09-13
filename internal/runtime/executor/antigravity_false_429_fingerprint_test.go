package executor

import (
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

func newAntigravityFingerprintHook(t *testing.T) *test.Hook {
	t.Helper()
	previousLevel := log.GetLevel()
	log.SetLevel(log.WarnLevel)
	hook := test.NewLocal(log.StandardLogger())
	t.Cleanup(func() {
		hook.Reset()
		log.SetLevel(previousLevel)
	})
	return hook
}

func resetAntigravityFalse429Warnings() {
	antigravityFalse429Warned = sync.Map{}
}

func TestAntigravityWarnsOnKnownFalse429Fingerprint(t *testing.T) {
	resetAntigravityFalse429Warnings()
	t.Cleanup(resetAntigravityFalse429Warnings)
	hook := newAntigravityFingerprintHook(t)

	executor := NewAntigravityExecutor(&config.Config{})
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"<system-conventions>\nRFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."}]}}}`)

	masked := executor.obfuscateSensitiveWords(payload)
	if string(masked) != string(payload) {
		t.Fatalf("obfuscateSensitiveWords() rewrote a payload without configured words: %s", masked)
	}

	entries := hook.AllEntries()
	if len(entries) != 2 {
		t.Fatalf("warnings = %d, want one per flagged phrase (2): %#v", len(entries), entries)
	}
	for _, entry := range entries {
		if entry.Level != log.WarnLevel || !strings.Contains(entry.Message, "antigravity.sensitive-words") {
			t.Fatalf("warning = %#v, want a warn naming antigravity.sensitive-words", entry)
		}
	}
}

func TestAntigravityFalse429WarningFiresOncePerPhrase(t *testing.T) {
	resetAntigravityFalse429Warnings()
	t.Cleanup(resetAntigravityFalse429Warnings)
	hook := newAntigravityFingerprintHook(t)

	executor := NewAntigravityExecutor(&config.Config{})
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"RFC 2119"}]}}}`)

	executor.obfuscateSensitiveWords(payload)
	executor.obfuscateSensitiveWords(payload)

	if entries := hook.AllEntries(); len(entries) != 1 {
		t.Fatalf("warnings = %d, want 1 for a repeated request: %#v", len(entries), entries)
	}
}

func TestAntigravityFalse429WarningStaysSilentOnceMasked(t *testing.T) {
	resetAntigravityFalse429Warnings()
	t.Cleanup(resetAntigravityFalse429Warnings)
	hook := newAntigravityFingerprintHook(t)

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity: config.AntigravityConfig{SensitiveWords: []string{"RFC 2119", "system-conventions"}},
	})
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"<system-conventions>\nRFC 2119: MUST"}]}}}`)

	masked := executor.obfuscateSensitiveWords(payload)
	if strings.Contains(string(masked), "<system-conventions>") || strings.Contains(string(masked), "RFC 2119") {
		t.Fatalf("obfuscateSensitiveWords() left a flagged phrase in the payload: %s", masked)
	}
	if entries := hook.AllEntries(); len(entries) != 0 {
		t.Fatalf("warnings = %d, want none once every flagged phrase is masked: %#v", len(entries), entries)
	}
}

func TestAntigravityFalse429WarningIgnoresUserContent(t *testing.T) {
	resetAntigravityFalse429Warnings()
	t.Cleanup(resetAntigravityFalse429Warnings)
	hook := newAntigravityFingerprintHook(t)

	executor := NewAntigravityExecutor(&config.Config{})
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Answer briefly."}]},"contents":[{"role":"user","parts":[{"text":"what does RFC 2119 mean?"}]}]}}`)

	executor.obfuscateSensitiveWords(payload)

	if entries := hook.AllEntries(); len(entries) != 0 {
		t.Fatalf("warnings = %d, want none for a phrase quoted by the user: %#v", len(entries), entries)
	}
}
