package auth

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

type persistenceTestSelector struct{ pick string }

func (s persistenceTestSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	for _, auth := range auths {
		if auth.ID == s.pick {
			return auth, nil
		}
	}
	return nil, nil
}

func TestSessionAffinityPersistenceSurvivesSelectorRestart(t *testing.T) {
	path := t.TempDir() + "/session-affinity.json"
	auths := []*Auth{{ID: "auth-a", Provider: "claude"}, {ID: "auth-b", Provider: "claude"}}
	opts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"8046da35-c12f-4b6b-8c9e-86ecb162fcee"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}

	first := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: persistenceTestSelector{pick: "auth-b"}, TTL: time.Hour, PersistencePath: path,
	})
	picked, err := first.Pick(context.Background(), "claude", "claude-opus-5", opts, auths)
	if err != nil || picked == nil || picked.ID != "auth-b" {
		t.Fatalf("initial pick = %#v, %v", picked, err)
	}
	status := first.FlushPersistence()
	if status.Error != "" || status.CacheEntries == 0 {
		t.Fatalf("flush status = %#v", status)
	}
	first.Stop()

	second := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: persistenceTestSelector{pick: "auth-a"}, TTL: time.Hour, PersistencePath: path,
	})
	defer second.Stop()
	picked, err = second.Pick(context.Background(), "claude", "claude-opus-5", opts, auths)
	if err != nil || picked == nil || picked.ID != "auth-b" {
		t.Fatalf("restored pick = %#v, %v; want auth-b", picked, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %v, err=%v", info.Mode().Perm(), err)
	}
}

func TestSessionAffinityPersistenceDisabledWhenPathEmpty(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: persistenceTestSelector{pick: "auth-a"}, TTL: time.Hour,
	})
	defer selector.Stop()
	if selector.persistence != nil {
		t.Fatal("persistence enabled without a path")
	}
	if status := selector.FlushPersistence(); status.Enabled || status.Path != "" {
		t.Fatalf("flush status without path = %#v, want disabled", status)
	}
	if status := selector.PersistenceStatus(); status.Enabled {
		t.Fatalf("persistence status without path = %#v, want disabled", status)
	}
}

func TestSessionAffinityPersistenceIgnoresUnreadableSnapshot(t *testing.T) {
	path := t.TempDir() + "/session-affinity.json"
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: persistenceTestSelector{pick: "auth-a"}, TTL: time.Hour, PersistencePath: path,
	})
	defer selector.Stop()
	if status := selector.PersistenceStatus(); !status.Enabled || status.Error == "" || status.CacheEntries != 0 {
		t.Fatalf("restore status = %#v, want enabled with error and no entries", status)
	}
	auths := []*Auth{{ID: "auth-a", Provider: "claude"}}
	opts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"5b1f0c6e-2f7a-4f5e-9c3d-1a2b3c4d5e6f"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	if picked, err := selector.Pick(context.Background(), "claude", "claude-opus-5", opts, auths); err != nil || picked == nil || picked.ID != "auth-a" {
		t.Fatalf("pick after bad snapshot = %#v, %v", picked, err)
	}
	if status := selector.FlushPersistence(); status.Error != "" || status.CacheEntries == 0 {
		t.Fatalf("flush after bad snapshot = %#v", status)
	}
}

func TestSessionAffinityPersistenceStopIsIdempotent(t *testing.T) {
	path := t.TempDir() + "/session-affinity.json"
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: persistenceTestSelector{pick: "auth-a"}, TTL: time.Hour, PersistencePath: path,
	})
	auths := []*Auth{{ID: "auth-a", Provider: "claude"}}
	opts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"0f5d2c1a-7b3e-4c9d-8e2f-6a1b3c5d7e9f"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	if _, err := selector.Pick(context.Background(), "claude", "claude-opus-5", opts, auths); err != nil {
		t.Fatal(err)
	}
	selector.Stop()
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	selector.Stop() // must neither panic nor overwrite the final snapshot
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("second Stop rewrote the snapshot")
	}
}

func TestReportSessionAffinityFlushLogsTransitionsOnce(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	failed := SessionAffinityPersistenceStatus{Enabled: true, Error: "disk full"}
	ok := SessionAffinityPersistenceStatus{Enabled: true}
	last := ""
	for _, status := range []SessionAffinityPersistenceStatus{failed, failed, failed, ok, ok} {
		last = reportSessionAffinityFlush(status, last)
	}
	var warnings, infos int
	for _, entry := range hook.AllEntries() {
		switch entry.Level {
		case log.WarnLevel:
			warnings++
		case log.InfoLevel:
			infos++
		}
	}
	if warnings != 1 || infos != 1 {
		t.Fatalf("warnings=%d infos=%d, want one failure warning and one recovery message", warnings, infos)
	}
}
