package grok

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestSaveTokenToFile_AtomicAndCorrect(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "grok-acct-1.json")

	ts := &GrokTokenStorage{
		AccessToken:  "atk-shouldbe-here",
		RefreshToken: "rtk-shouldbe-here",
		Email:        "user@example.com",
	}
	if err := ts.SaveTokenToFile(authPath); err != nil {
		t.Fatalf("SaveTokenToFile error: %v", err)
	}

	raw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["access_token"] != "atk-shouldbe-here" {
		t.Errorf("access_token mismatch: %v", got["access_token"])
	}
	if got["type"] != "grok" {
		t.Errorf("type field should be 'grok', got %v", got["type"])
	}
	// No leftover .tmp file
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestApplyRefresh_PreservesExistingRefreshTokenWhenServerOmits(t *testing.T) {
	ts := &GrokTokenStorage{
		AccessToken:  "old-atk",
		RefreshToken: "old-rtk",
	}
	ts.ApplyRefresh(&TokenResponse{
		AccessToken: "new-atk",
		ExpiresIn:   3600,
		// RefreshToken intentionally empty
	})
	if ts.AccessToken != "new-atk" {
		t.Errorf("access not updated")
	}
	if ts.RefreshToken != "old-rtk" {
		t.Errorf("refresh should be preserved, got %q", ts.RefreshToken)
	}
}

func TestRedactor_RedactsJWTInLogOutput(t *testing.T) {
	logger := log.New()
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetFormatter(&log.TextFormatter{DisableColors: true, DisableTimestamp: true})
	logger.AddHook(LogRedactorHook{})

	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjMiLCJuYW1lIjoiYWxpY2UifQ.signature-portion-here-1234567890"
	logger.Infof("authenticated with token %s for account", jwt)

	out := buf.String()
	if strings.Contains(out, jwt) {
		t.Errorf("raw JWT leaked into log output:\n%s", out)
	}
	if !strings.Contains(out, "<redacted-jwt>") {
		t.Errorf("expected <redacted-jwt> marker in output:\n%s", out)
	}
}

func TestRedactor_RedactsAuthorizationHeader(t *testing.T) {
	out := RedactTokens("Authorization: Bearer abc.def.ghi.this.is.a.token")
	if strings.Contains(out, "abc.def.ghi.this.is.a.token") {
		t.Errorf("raw bearer leaked: %s", out)
	}
}

func TestRedactor_RedactsKnownFieldNames(t *testing.T) {
	logger := log.New()
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetFormatter(&log.TextFormatter{DisableColors: true, DisableTimestamp: true})
	logger.AddHook(LogRedactorHook{})

	logger.WithField("access_token", "opaque-value-12345").Info("hi")
	out := buf.String()
	if strings.Contains(out, "opaque-value-12345") {
		t.Errorf("known-field token leaked: %s", out)
	}
}
