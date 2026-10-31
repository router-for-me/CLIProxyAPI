package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func grantRetentionExpireOutcome(m *Manager, id string) {
	state := m.authRefreshState(id)
	state.stateMu.Lock()
	if state.outcome != nil {
		state.outcome.retryAt = time.Now().Add(-time.Hour)
	}
	state.stateMu.Unlock()
}

func TestRequestRefreshRetainsConsumedGrant(t *testing.T) {
	for _, change := range []string{"register-old", "register-pending", "expiry", "version", "file-name", "path", "provider", "policy", "absent-old", "absent-pending", "load-file-name", "load-path"} {
		t.Run(change, func(t *testing.T) {
			m, original, store, executor := demandManager(t, time.Now().Add(-time.Hour))
			failure := errors.New("synthetic consumed rotation save failure")
			var rotated *Auth
			executor.refresh = func(_ context.Context, next *Auth) (*Auth, error) {
				next.Metadata["access_token"] = "new"
				next.Metadata["refresh_token"] = "new-refresh"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
				rotated = next.Clone()
				return next, nil
			}
			store.mu.Lock()
			store.fail = failure
			store.mu.Unlock()
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := m.EnsureRequestAuth(context.Background(), original, ""); !errors.Is(err, failure) {
					t.Fatalf("original save failure was lost: %v", err)
				}
			}
			current, _ := m.GetByID(original.ID)
			snapshot := current.Clone()
			switch change {
			case "register-old", "register-pending":
				if change == "register-pending" {
					snapshot = rotated.Clone()
				}
				if _, err := m.Register(WithSkipPersist(context.Background()), snapshot); err != nil {
					t.Fatal(err)
				}
			case "absent-old", "absent-pending":
				store.mu.Lock()
				store.items = nil
				store.mu.Unlock()
				if err := m.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, exists := m.GetByID(original.ID); exists {
					t.Fatal("absent load retained a registration")
				}
				if change == "absent-pending" {
					snapshot = rotated.Clone()
				}
				store.mu.Lock()
				store.items = []*Auth{snapshot}
				store.mu.Unlock()
				if err := m.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "load-file-name", "load-path":
				if change == "load-file-name" {
					snapshot.FileName = "moved.json"
				} else {
					snapshot.Attributes = map[string]string{AttributePath: "moved.json"}
				}
				store.mu.Lock()
				store.items = []*Auth{snapshot}
				store.mu.Unlock()
				if err := m.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
			default:
				switch change {
				case "expiry":
					snapshot.Metadata["expired"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339Nano)
				case "version":
					snapshot.Metadata["id_token"] = "edited-session-same-pair"
				case "file-name":
					snapshot.FileName = "moved.json"
				case "path":
					snapshot.Attributes = map[string]string{AttributePath: "moved.json"}
				case "provider":
					snapshot.Provider = "different-provider-same-grant"
				case "policy":
					snapshot.RefreshPolicy = pluginapi.RefreshDefault
					snapshot.Metadata["expired"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339Nano)
				}
				if _, err := m.Update(WithSkipPersist(context.Background()), snapshot); err != nil {
					t.Fatal(err)
				}
			}
			current, _ = m.GetByID(original.ID)
			if change == "version" && (current.CredentialVersion <= original.CredentialVersion || current.Metadata["access_token"] != "old" || current.Metadata["refresh_token"] != "old-refresh") {
				t.Fatal("version mutation did not preserve the original token pair")
			}
			if change == "expiry" && current.CredentialVersion != original.CredentialVersion {
				t.Fatal("expiry-only mutation changed credential version")
			}
			store.mu.Lock()
			store.fail = nil
			store.mu.Unlock()
			for attempt := 0; attempt < 3; attempt++ {
				grantRetentionExpireOutcome(m, original.ID)
				if fresh, err := m.ForceRefreshAuth(context.Background(), original.ID); err == nil || fresh != nil {
					t.Errorf("force attempt %d admitted a source-fenced grant: %v", attempt, err)
				}
				grantRetentionExpireOutcome(m, original.ID)
				current, _ = m.GetByID(original.ID)
				if fresh, err := m.EnsureRequestAuth(context.Background(), current, ""); err == nil || fresh != nil {
					t.Errorf("demand attempt %d admitted a source-fenced grant: %v", attempt, err)
				}
				if _, err := m.Execute(context.Background(), []string{current.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{}); err == nil {
					t.Errorf("request attempt %d admitted an unsettled grant", attempt)
				}
				store.mu.Lock()
				saves := store.saves
				store.mu.Unlock()
				if executor.refreshes.Load() != 1 || executor.calls.Load() != 0 || saves != 2 {
					t.Errorf("attempt %d replayed or saved a fenced rotation: refresh=%d inference=%d saves=%d", attempt, executor.refreshes.Load(), executor.calls.Load(), saves)
				}
			}
		})
	}
}

func TestRequestRefreshRetainsConsumedGrantDuringSourceChange(t *testing.T) {
	for _, change := range []string{"register", "expiry", "version", "file-name", "path", "absent"} {
		t.Run(change, func(t *testing.T) {
			m, original, store, executor := demandManager(t, time.Now().Add(-time.Hour))
			started, release := make(chan struct{}), make(chan struct{})
			executor.refresh = func(_ context.Context, next *Auth) (*Auth, error) {
				if executor.refreshes.Load() == 1 {
					close(started)
					<-release
				}
				next.Metadata["access_token"] = "new"
				next.Metadata["refresh_token"] = "new-refresh"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
				return next, nil
			}
			result := make(chan error, 1)
			go func() { _, err := m.EnsureRequestAuth(context.Background(), original, ""); result <- err }()
			<-started
			current, _ := m.GetByID(original.ID)
			switch change {
			case "register":
				if _, err := m.Register(WithSkipPersist(context.Background()), current); err != nil {
					t.Fatal(err)
				}
			case "absent":
				store.mu.Lock()
				store.items = nil
				store.mu.Unlock()
				if err := m.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
			default:
				switch change {
				case "expiry":
					current.Metadata["expired"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339Nano)
				case "version":
					current.Metadata["id_token"] = "edited-session-same-pair"
				case "file-name":
					current.FileName = "moved.json"
				case "path":
					current.Attributes = map[string]string{AttributePath: "moved.json"}
				}
				if _, err := m.Update(WithSkipPersist(context.Background()), current); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			if err := <-result; err == nil {
				t.Fatal("source-changed rotation published a stale result")
			}
			if change == "absent" {
				store.mu.Lock()
				store.items = []*Auth{original.Clone()}
				store.mu.Unlock()
				if err := m.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			for attempt := 0; attempt < 3; attempt++ {
				grantRetentionExpireOutcome(m, original.ID)
				if fresh, err := m.ForceRefreshAuth(context.Background(), original.ID); err == nil || fresh != nil {
					t.Errorf("force attempt %d replayed a consumed source-changed grant: %v", attempt, err)
				}
				current, _ = m.GetByID(original.ID)
				if fresh, err := m.EnsureRequestAuth(context.Background(), current, ""); err == nil || fresh != nil {
					t.Errorf("demand attempt %d admitted an unsettled grant: %v", attempt, err)
				}
				_, err := m.Execute(context.Background(), []string{original.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
				if err == nil || executor.refreshes.Load() != 1 || executor.calls.Load() != 0 {
					t.Errorf("source-changed success lost its hold: error=%v refresh=%d inference=%d", err, executor.refreshes.Load(), executor.calls.Load())
				}
			}
		})
	}
}

func TestRequestRefreshPersistenceRequiresOriginalSource(t *testing.T) {
	for _, change := range []string{"expiry", "file-name", "path", "provider"} {
		t.Run(change, func(t *testing.T) {
			m, base, store, executor := demandManager(t, time.Now().Add(-time.Hour))
			updated := base.Clone()
			updated.Metadata["access_token"] = "new"
			updated.Metadata["refresh_token"] = "new-refresh"
			updated.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
			current := base.Clone()
			switch change {
			case "expiry":
				current.Metadata["expired"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339Nano)
			case "file-name":
				current.FileName = "moved.json"
			case "path":
				current.Attributes = map[string]string{AttributePath: "moved.json"}
			case "provider":
				current.Provider = "different-provider-same-grant"
			}
			if _, err := m.Update(WithSkipPersist(context.Background()), current); err != nil {
				t.Fatal(err)
			}
			if saved, err := m.UpdateRefreshedAuth(strictRefreshContext(context.Background()), base, updated); err == nil || saved != nil {
				t.Errorf("strict persistence admitted a stale source: error=%v", err)
			}
			after, _ := m.GetByID(base.ID)
			store.mu.Lock()
			saves := store.saves
			store.mu.Unlock()
			if after.Metadata["access_token"] != "old" || saves != 1 || executor.refreshes.Load() != 0 {
				t.Fatalf("strict persistence overwrote source-changed credentials: saves=%d refresh=%d", saves, executor.refreshes.Load())
			}
		})
	}
}

func TestRequestRefreshSaveOnlyRetryAndDistinctLogin(t *testing.T) {
	for _, replacement := range []string{"original-source", "fresh-login", "expired-login"} {
		t.Run(replacement, func(t *testing.T) {
			m, original, store, executor := demandManager(t, time.Now().Add(-time.Hour))
			failure := errors.New("synthetic save-only control failure")
			store.mu.Lock()
			store.fail = failure
			store.mu.Unlock()
			if _, err := m.EnsureRequestAuth(context.Background(), original, ""); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			store.mu.Lock()
			store.fail = nil
			store.mu.Unlock()
			current, _ := m.GetByID(original.ID)
			wantRefresh := int32(1)
			if replacement != "original-source" {
				current.Metadata["access_token"] = "distinct-login"
				current.Metadata["refresh_token"] = "distinct-login-refresh"
				current.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
				if replacement == "expired-login" {
					current.Metadata["expired"] = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
					wantRefresh = 2
				}
				if _, err := m.Register(context.Background(), current); err != nil {
					t.Fatal(err)
				}
			} else if saved, err := m.ForceRefreshAuth(context.Background(), original.ID); err != nil || saved.Metadata["refresh_token"] != "new-refresh" {
				t.Fatalf("save-only retry failed: %v", err)
			}
			if _, err := m.Execute(context.Background(), []string{original.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{}); err != nil || executor.refreshes.Load() != wantRefresh || executor.calls.Load() != 1 {
				t.Fatalf("replacement did not recover: error=%v refresh=%d inference=%d", err, executor.refreshes.Load(), executor.calls.Load())
			}
		})
	}
}
