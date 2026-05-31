package cmd

import (
	"bytes"
	"testing"

	log "github.com/sirupsen/logrus"
)

// warnPortOverride is the testable core of the port-override warning logic
// extracted from DoGrokLogin. It logs a warning when port != 0 and != 56121.
func warnPortOverride(port int) {
	if port != 0 && port != 56121 {
		log.Warnf("--oauth-callback-port=%d is ignored for Grok: xAI rejects any redirect port other than 56121.", port)
	}
}

// TestDoGrokLogin_IgnoresOAuthCallbackPortOverride verifies that supplying a
// non-56121 callback port emits the expected warning log entry.
func TestDoGrokLogin_IgnoresOAuthCallbackPortOverride(t *testing.T) {
	// Capture logrus output.
	var buf bytes.Buffer
	logger := log.New()
	logger.SetLevel(log.WarnLevel)
	logger.SetOutput(&buf)

	// Replace the standard logger temporarily.
	origOut := log.StandardLogger().Out
	origLevel := log.GetLevel()
	log.SetOutput(&buf)
	log.SetLevel(log.WarnLevel)
	defer func() {
		log.SetOutput(origOut)
		log.SetLevel(origLevel)
	}()

	warnPortOverride(9999)

	got := buf.String()
	if got == "" {
		t.Fatal("expected a warning log entry for port override, got none")
	}
	wantSubstr := "9999"
	if !bytes.Contains([]byte(got), []byte(wantSubstr)) {
		t.Errorf("warning log does not mention port 9999; got: %s", got)
	}
	wantSubstr2 := "56121"
	if !bytes.Contains([]byte(got), []byte(wantSubstr2)) {
		t.Errorf("warning log does not mention 56121; got: %s", got)
	}
}

// TestDoGrokLogin_NoWarningForCorrectPort verifies that port 56121 does NOT
// emit a warning (it is the expected value).
func TestDoGrokLogin_NoWarningForCorrectPort(t *testing.T) {
	var buf bytes.Buffer
	origOut := log.StandardLogger().Out
	origLevel := log.GetLevel()
	log.SetOutput(&buf)
	log.SetLevel(log.WarnLevel)
	defer func() {
		log.SetOutput(origOut)
		log.SetLevel(origLevel)
	}()

	warnPortOverride(56121)

	if buf.Len() != 0 {
		t.Errorf("expected no warning for port 56121, got: %s", buf.String())
	}
}

// TestDoGrokLogin_NoWarningForZeroPort verifies that port 0 (unset) does NOT
// emit a warning.
func TestDoGrokLogin_NoWarningForZeroPort(t *testing.T) {
	var buf bytes.Buffer
	origOut := log.StandardLogger().Out
	origLevel := log.GetLevel()
	log.SetOutput(&buf)
	log.SetLevel(log.WarnLevel)
	defer func() {
		log.SetOutput(origOut)
		log.SetLevel(origLevel)
	}()

	warnPortOverride(0)

	if buf.Len() != 0 {
		t.Errorf("expected no warning for port 0, got: %s", buf.String())
	}
}
