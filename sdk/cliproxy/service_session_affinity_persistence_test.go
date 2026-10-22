package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func buildSessionPersistenceService(t *testing.T, cfg *internalconfig.Config) *Service {
	t.Helper()
	service, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")).Build()
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	t.Cleanup(func() {
		if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
			t.Error(errShutdown)
		}
	})
	return service
}

func pickPersistenceSession(t *testing.T, selector coreauth.Selector, sessionID string, auths ...*coreauth.Auth) string {
	t.Helper()
	headers := make(http.Header)
	headers.Set("X-Session-ID", sessionID)
	got, errPick := selector.Pick(context.Background(), "openai", "gpt-test", cliproxyexecutor.Options{Headers: headers}, auths)
	if errPick != nil || got == nil {
		t.Fatalf("Pick = %+v, %v", got, errPick)
	}
	return got.ID
}

func pickPersistenceLCPSession(t *testing.T, selector coreauth.Selector, message string, auths ...*coreauth.Auth) string {
	t.Helper()
	options := cliproxyexecutor.Options{
		OriginalRequest: []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, message)),
		Metadata:        map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller"},
	}
	got, errPick := selector.Pick(context.Background(), "openai", "gpt-test", options, auths)
	if errPick != nil || got == nil {
		t.Fatalf("LCP Pick = %+v, %v", got, errPick)
	}
	return got.ID
}

func TestBuilderSessionAffinityPersistenceDefaultOff(t *testing.T) {
	for _, test := range []struct {
		name                        string
		affinity, persistence, home bool
	}{
		{name: "existing memory affinity", affinity: true},
		{name: "affinity disabled", persistence: true},
		{name: "Home authority", affinity: true, persistence: true, home: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &internalconfig.Config{AuthDir: t.TempDir(), Routing: internalconfig.RoutingConfig{
				SessionAffinity: test.affinity, SessionAffinityPersistence: test.persistence,
			}}
			cfg.Home.Enabled = test.home
			service := buildSessionPersistenceService(t, cfg)
			pickPersistenceSession(t, service.coreManager.Selector(), "memory", &coreauth.Auth{ID: "a"})
			if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
				t.Fatal(errShutdown)
			}
			if _, errStat := os.Stat(filepath.Join(cfg.AuthDir, ".runtime")); !os.IsNotExist(errStat) {
				t.Fatalf("inactive persistence created runtime artifacts: %v", errStat)
			}
		})
	}
}

func TestBuilderSessionAffinityPersistenceRestartAndHotReload(t *testing.T) {
	cfg := &internalconfig.Config{AuthDir: t.TempDir(), Routing: internalconfig.RoutingConfig{
		Strategy: "fill-first", SessionAffinity: true, SessionAffinityPersistence: true,
	}}
	path := filepath.Join(cfg.AuthDir, ".runtime", "session-affinity.cache")
	service := buildSessionPersistenceService(t, cfg)
	original := service.coreManager.Selector()
	if got := pickPersistenceSession(t, original, "stable", &coreauth.Auth{ID: "b"}); got != "b" {
		t.Fatal("initial binding did not use available credential")
	}
	pickPersistenceLCPSession(t, original, "stable LCP", &coreauth.Auth{ID: "b"})
	unrelated := *cfg
	unrelated.Debug = true
	service.applyWatcherConfigUpdate(&unrelated)
	if service.coreManager.Selector() != original {
		t.Fatal("unrelated hot reload replaced selector")
	}
	changed := unrelated
	changed.Routing.SessionAffinityTTL = "2h"
	service.applyWatcherConfigUpdate(&changed)
	replacement := service.coreManager.Selector()
	if replacement == original {
		t.Fatal("TTL update did not replace selector")
	}
	// Calls that retained the old selector must still update the live exact-ID cache.
	pickPersistenceSession(t, original, "late", &coreauth.Auth{ID: "b"})
	pickPersistenceLCPSession(t, original, "late LCP", &coreauth.Auth{ID: "b"})
	if got := pickPersistenceSession(t, replacement, "late", &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "b" {
		t.Fatal("late old selector binding was discarded")
	}
	rebind := changed
	rebind.Routing.SessionAffinityRebindOn = coreauth.SessionAffinityRebindOnQuota
	service.applyWatcherConfigUpdate(&rebind)
	if service.coreManager.Selector() == replacement {
		t.Fatal("rebind policy update did not replace selector")
	}
	if got := pickPersistenceSession(t, service.coreManager.Selector(), "late", &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "b" {
		t.Fatal("rebind policy update discarded the persisted binding")
	}
	if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	if _, errStat := os.Stat(path); errStat != nil {
		t.Fatal(errStat)
	}
	restarted := buildSessionPersistenceService(t, &rebind)
	for _, session := range []string{"stable", "late"} {
		if got := pickPersistenceSession(t, restarted.coreManager.Selector(), session, &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "b" {
			t.Fatalf("restarted session %q selected %q, want b", session, got)
		}
		if got := pickPersistenceLCPSession(t, restarted.coreManager.Selector(), session+" LCP", &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "b" {
			t.Fatalf("restarted LCP session %q selected %q, want b", session, got)
		}
	}
}

func TestServiceSessionAffinityPersistenceHotEnableDisableAndPathChange(t *testing.T) {
	cfg := &internalconfig.Config{AuthDir: t.TempDir(), Routing: internalconfig.RoutingConfig{
		Strategy: "fill-first", SessionAffinity: true,
	}}
	service := buildSessionPersistenceService(t, cfg)
	original := service.coreManager.Selector()
	pickPersistenceSession(t, original, "live", &coreauth.Auth{ID: "b"})
	pickPersistenceLCPSession(t, original, "live LCP", &coreauth.Auth{ID: "b"})
	enabled := *cfg
	enabled.Routing.SessionAffinityPersistence = true
	service.applyWatcherConfigUpdate(&enabled)
	if service.coreManager.Selector() != original {
		t.Fatal("persistence toggle replaced the selector")
	}
	disabled := enabled
	disabled.Routing.SessionAffinityPersistence = false
	service.applyWatcherConfigUpdate(&disabled)
	path := filepath.Join(cfg.AuthDir, ".runtime", "session-affinity.cache")
	if _, errStat := os.Stat(path); errStat != nil {
		t.Fatal("disable did not flush the writer", errStat)
	}
	// Change a binding while disk still contains the former credential.
	pickPersistenceSession(t, original, "live", &coreauth.Auth{ID: "a"})
	original.(*coreauth.SessionAffinitySelector).InvalidateAuth("b")
	pickPersistenceLCPSession(t, original, "live LCP", &coreauth.Auth{ID: "a"})
	service.applyWatcherConfigUpdate(&enabled)
	if got := pickPersistenceSession(t, service.coreManager.Selector(), "live", &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "a" {
		t.Fatal("hot enable restored stale disk state")
	}
	if got := pickPersistenceLCPSession(t, service.coreManager.Selector(), "live LCP", &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "a" {
		t.Fatal("hot enable restored stale LCP state")
	}
	pickPersistenceSession(t, original, "new-path", &coreauth.Auth{ID: "b"})
	pickPersistenceLCPSession(t, original, "new-path LCP", &coreauth.Auth{ID: "b"})
	moved := enabled
	moved.AuthDir = t.TempDir()
	service.applyWatcherConfigUpdate(&moved)
	if service.coreManager.Selector() != original {
		t.Fatal("snapshot path change replaced selector")
	}
	if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	restarted := buildSessionPersistenceService(t, &moved)
	if got := pickPersistenceSession(t, restarted.coreManager.Selector(), "new-path", &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "b" {
		t.Fatal("path transition lost current bindings")
	}
	if got := pickPersistenceLCPSession(t, restarted.coreManager.Selector(), "new-path LCP", &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "b" {
		t.Fatal("path transition lost current LCP bindings")
	}
	oldPath := buildSessionPersistenceService(t, &enabled)
	if got := pickPersistenceSession(t, oldPath.coreManager.Selector(), "new-path", &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "b" {
		t.Fatal("path transition failed to flush former file")
	}
}

func TestSessionAffinityPersistencePathUsesAuthDirConvention(t *testing.T) {
	cfg := &internalconfig.Config{AuthDir: "~/.cli-proxy-api-test", Routing: internalconfig.RoutingConfig{
		SessionAffinity: true, SessionAffinityPersistence: true,
	}}
	dir, errResolve := util.ResolveAuthDir(cfg.AuthDir)
	if errResolve != nil {
		t.Fatal(errResolve)
	}
	if got := normalizedRoutingRuntimeState(cfg).persistencePath; got != filepath.Join(dir, ".runtime", "session-affinity.cache") {
		t.Fatalf("snapshot path = %q, want resolved auth directory", got)
	}
}

func TestServiceSessionAffinityPersistenceHotEnableEmptyCacheReplacesStaleFile(t *testing.T) {
	cfg := &internalconfig.Config{AuthDir: t.TempDir(), Routing: internalconfig.RoutingConfig{
		Strategy: "fill-first", SessionAffinity: true, SessionAffinityPersistence: true,
	}}
	old := buildSessionPersistenceService(t, cfg)
	pickPersistenceSession(t, old.coreManager.Selector(), "stale", &coreauth.Auth{ID: "b"})
	if errShutdown := old.Shutdown(context.Background()); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	off := *cfg
	off.Routing.SessionAffinityPersistence = false
	current := buildSessionPersistenceService(t, &off)
	current.applyWatcherConfigUpdate(cfg)
	if errShutdown := current.Shutdown(context.Background()); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	restarted := buildSessionPersistenceService(t, cfg)
	if got := pickPersistenceSession(t, restarted.coreManager.Selector(), "stale", &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "a" {
		t.Fatal("enabling an empty live cache revived stale snapshot on restart")
	}
}

func TestServiceSessionAffinityPersistenceRejectsConfigApplyAfterShutdown(t *testing.T) {
	cfg := &internalconfig.Config{AuthDir: t.TempDir(), Routing: internalconfig.RoutingConfig{
		Strategy: "fill-first", SessionAffinity: true, SessionAffinityPersistence: true,
	}}
	service := buildSessionPersistenceService(t, cfg)
	original := service.coreManager.Selector()
	pickPersistenceSession(t, original, "stable", &coreauth.Auth{ID: "b"})
	pending := *cfg
	pending.Routing.Strategy = "round-robin"
	commit := service.commitConfigUpdate(&pending)
	release := make(chan struct{})
	applied := make(chan bool, 1)
	go func() {
		<-release
		applied <- service.applyConfigRuntime(context.Background(), commit, false)
	}()
	if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
		close(release)
		<-applied
		t.Fatal(errShutdown)
	}
	close(release)
	if <-applied {
		t.Error("pending config application succeeded after shutdown")
	}
	if service.coreManager.Selector() != original {
		t.Error("late config application recreated selector after final snapshot")
	}
	// Stop any incorrectly restarted writer so its final state is observable.
	service.coreManager.StopAutoRefresh()
	restarted := buildSessionPersistenceService(t, cfg)
	if got := pickPersistenceSession(t, restarted.coreManager.Selector(), "stable", &coreauth.Auth{ID: "a"}, &coreauth.Auth{ID: "b"}); got != "b" {
		t.Fatal("late config application overwrote shutdown snapshot")
	}
}
