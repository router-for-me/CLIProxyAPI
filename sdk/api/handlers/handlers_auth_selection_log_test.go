package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

func TestEnrichAuthSelectionErrorLogsBlockedCredentials(t *testing.T) {
	hook := test.NewGlobal()
	t.Cleanup(hook.Reset)
	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() { log.SetLevel(previousLevel) })

	now := time.Now()
	auths := []*coreauth.Auth{
		{
			ID:            "codex-a",
			Quota:         coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Minute)},
			StatusMessage: "429 quota",
		},
		{ID: "codex-b", Disabled: true},
	}
	selector := &coreauth.FillFirstSelector{}
	_, selectionErr := selector.Pick(context.Background(), "codex", "gpt-5.6-terra", cliproxyexecutor.Options{}, auths)
	if selectionErr == nil {
		t.Fatal("expected selection to fail when every auth is blocked")
	}

	enriched := enrichAuthSelectionError(context.Background(), selectionErr, []string{"codex"}, "gpt-5.6-terra")

	entries := hook.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("logged %d entries, want exactly 1: %v", len(entries), entries)
	}
	entry := entries[0]
	if entry.Level != log.WarnLevel {
		t.Fatalf("level = %s, want warn so the reason survives debug: false", entry.Level)
	}
	for _, want := range []string{"providers=codex", "model=gpt-5.6-terra", "codex-a=cooldown", "429 quota", "codex-b=disabled"} {
		if !strings.Contains(entry.Message, want) {
			t.Fatalf("log message %q missing %q", entry.Message, want)
		}
	}
	if strings.Contains(enriched.Error(), "codex-a") {
		t.Fatalf("client-facing error %q must not carry per-credential diagnostics", enriched.Error())
	}
}
