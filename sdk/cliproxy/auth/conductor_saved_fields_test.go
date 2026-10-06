package auth

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type enrichingAuthStore struct{ err error }

func (*enrichingAuthStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (*enrichingAuthStore) Delete(context.Context, string) error  { return nil }
func (s *enrichingAuthStore) Save(_ context.Context, auth *Auth) (string, error) {
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[AttributePath] = "/fixture/" + auth.ID + ".json"
	auth.Attributes[AttributeSource] = auth.Attributes[AttributePath]
	auth.Attributes[AttributeSourceBackend] = AuthSourceFile
	auth.FileName = auth.ID + ".json"
	auth.Metadata["disabled"] = auth.Disabled
	return auth.Attributes[AttributePath], s.err
}

type savedFieldsHook struct{ auth *Auth }

func (h *savedFieldsHook) OnAuthRegistered(_ context.Context, auth *Auth) { h.auth = auth.Clone() }
func (h *savedFieldsHook) OnAuthUpdated(_ context.Context, auth *Auth)    { h.auth = auth.Clone() }
func (*savedFieldsHook) OnResult(context.Context, Result)                 {}

func TestManagerSavedFieldsPublished(t *testing.T) {
	for _, saveErr := range []error{nil, errors.New("save failed after enrichment")} {
		name := "success"
		if saveErr != nil {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			testManagerSavedFieldsPublished(t, saveErr)
		})
	}
}

func testManagerSavedFieldsPublished(t *testing.T, saveErr error) {
	for _, operation := range []string{"Register", "Update", "Prepare", "Refresh"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			hook := &savedFieldsHook{}
			manager := NewManager(&enrichingAuthStore{err: saveErr}, nil, hook)
			input := &Auth{ID: "saved-fields", Provider: "audit", Status: StatusActive, Metadata: map[string]any{"type": "audit"}}
			var returned *Auth
			var err error
			if operation == "Register" {
				returned, err = manager.Register(ctx, input)
			} else {
				base, errRegister := manager.Register(ctx, input)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				input = base.Clone()
				input.FileName = ""
				input.Attributes = nil
				delete(input.Metadata, "disabled")
				switch operation {
				case "Update":
					returned, err = manager.Update(ctx, input)
				case "Prepare":
					returned, err = manager.UpdatePreparedAuth(ctx, base, input)
				case "Refresh":
					returned, err = manager.UpdateRefreshedAuth(ctx, base, input)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			published, ok := manager.GetByID(input.ID)
			if !ok {
				t.Fatal("missing published auth")
			}
			for name, snapshot := range map[string]*Auth{"manager": published, "return": returned, "hook": hook.auth} {
				if snapshot == nil || snapshot.FileName != "saved-fields.json" || snapshot.Attributes[AttributePath] != "/fixture/saved-fields.json" || snapshot.Attributes[AttributeSource] != "/fixture/saved-fields.json" || snapshot.Attributes[AttributeSourceBackend] != AuthSourceFile || snapshot.Metadata["disabled"] != false {
					t.Errorf("%s snapshot missing Store.Save enrichment: %+v", name, snapshot)
				}
				if !reflect.DeepEqual(snapshot, published) {
					t.Errorf("%s snapshot differs from manager", name)
				}
			}
			manager.scheduler.mu.Lock()
			scheduled := manager.scheduler.providers["audit"].auths[input.ID].auth.Clone()
			manager.scheduler.mu.Unlock()
			if !reflect.DeepEqual(scheduled, published) {
				t.Error("scheduler snapshot differs from manager")
			}
		})
	}
}

// blockingEnrichingAuthStore pauses one save before enriching the auth.
type blockingEnrichingAuthStore struct {
	enrichingAuthStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	saved   []*Auth
}

func (s *blockingEnrichingAuthStore) Save(ctx context.Context, auth *Auth) (string, error) {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	path, err := s.enrichingAuthStore.Save(ctx, auth)
	s.mu.Lock()
	s.saved = append(s.saved, auth.Clone())
	s.mu.Unlock()
	return path, err
}

func TestManagerSavedFieldsConcurrentLifecycle(t *testing.T) {
	for _, saveErr := range []error{nil, errors.New("save failed after enrichment")} {
		name := "success"
		if saveErr != nil {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) { testManagerSavedFieldsConcurrentLifecycle(t, saveErr) })
	}
}

func testManagerSavedFieldsConcurrentLifecycle(t *testing.T, saveErr error) {
	for _, operation := range []string{"Register", "Update"} {
		for _, concurrent := range []string{"MarkResult", "Update", "Remove", "ReRegister"} {
			t.Run(operation+"/"+concurrent, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx := context.Background()
					store := &blockingEnrichingAuthStore{enrichingAuthStore: enrichingAuthStore{err: saveErr}, entered: make(chan struct{}), release: make(chan struct{})}
					var releaseOnce sync.Once
					release := func() { releaseOnce.Do(func() { close(store.release) }) }
					defer release()
					manager := NewManager(store, nil, nil)
					input := &Auth{ID: "saved-fields", Provider: "audit", Status: StatusActive, Label: "old", Metadata: map[string]any{"type": "audit"}}
					if operation == "Update" {
						var errRegister error
						input, errRegister = manager.Register(WithSkipPersist(ctx), input)
						if errRegister != nil {
							t.Fatal(errRegister)
						}
					}
					next := input.Clone()
					next.Label = "new"
					done := make(chan error, 1)
					go func() {
						var err error
						if operation == "Register" {
							_, err = manager.Register(ctx, input)
						} else {
							_, err = manager.Update(ctx, input)
						}
						done <- err
					}()
					<-store.entered
					started := make(chan struct{})
					concurrentDone := make(chan error, 1)
					go func() {
						close(started)
						var err error
						switch concurrent {
						case "MarkResult":
							manager.MarkResult(ctx, Result{AuthID: next.ID, Provider: next.Provider, Model: "model", Error: &Error{HTTPStatus: 429, Message: "quota exceeded"}})
						case "Update":
							_, err = manager.Update(ctx, next)
						case "Remove":
							manager.Remove(ctx, next.ID)
						case "ReRegister":
							manager.Remove(ctx, next.ID)
							_, err = manager.Register(ctx, next)
						}
						concurrentDone <- err
					}()
					<-started
					synctest.Wait()
					select {
					case <-concurrentDone:
						t.Fatal("same-credential mutation completed before the active save")
					default:
					}
					release()
					if errDone := <-done; errDone != nil {
						t.Fatal(errDone)
					}
					if errConcurrent := <-concurrentDone; errConcurrent != nil {
						t.Fatal(errConcurrent)
					}
					published, ok := manager.GetByID(input.ID)
					manager.scheduler.mu.Lock()
					var scheduled *Auth
					if provider := manager.scheduler.providers["audit"]; provider != nil {
						if meta := provider.auths[input.ID]; meta != nil {
							scheduled = meta.auth.Clone()
						}
					}
					manager.scheduler.mu.Unlock()
					if concurrent == "Remove" {
						if ok || scheduled != nil {
							t.Fatal("completed save resurrected removed auth")
						}
						return
					}
					if !ok || published.FileName != "saved-fields.json" || published.Attributes[AttributePath] != "/fixture/saved-fields.json" {
						t.Fatalf("missing saved fields: %+v", published)
					}
					if concurrent == "MarkResult" {
						state := published.ModelStates["model"]
						if published.Failed != 1 || published.LastError == nil || state == nil || !state.Quota.Exceeded || !state.Unavailable {
							t.Fatalf("save overwrote concurrent result: %+v", published)
						}
					} else if published.Label != "new" {
						t.Fatalf("save overwrote newer lifecycle state: %+v", published)
					}
					if !reflect.DeepEqual(scheduled, published) {
						t.Fatal("scheduler retained an obsolete save snapshot")
					}
					store.mu.Lock()
					saved := store.saved[len(store.saved)-1].Clone()
					store.mu.Unlock()
					if !reflect.DeepEqual(saved, published) {
						t.Fatal("store retained an obsolete auth version")
					}
					if concurrent == "ReRegister" {
						if published.RegistrationEpoch <= input.RegistrationEpoch {
							t.Fatal("re-registration did not advance epoch")
						}
						if _, errUpdate := manager.UpdateRefreshedAuth(ctx, input, input.Clone()); errUpdate == nil {
							t.Fatal("accepted stale registration after re-register")
						}
					}
				})
			})
		}
	}
}

func TestManagerBlockedSaveAllowsUnrelatedCredential(t *testing.T) {
	for _, operation := range []string{"Register", "Update"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			store := &blockingEnrichingAuthStore{entered: make(chan struct{}), release: make(chan struct{})}
			manager := NewManager(store, nil, nil)
			manager.RegisterExecutor(&aliasRoutingExecutor{id: "audit"})
			if _, err := manager.Register(WithSkipPersist(ctx), &Auth{ID: "B", Provider: "audit", Status: StatusActive}); err != nil {
				t.Fatal(err)
			}
			input := &Auth{ID: "A", Provider: "audit-a", Status: StatusActive, Metadata: map[string]any{"type": "audit-a"}}
			if operation == "Update" {
				if _, err := manager.Register(WithSkipPersist(ctx), input); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				if operation == "Register" {
					_, _ = manager.Register(ctx, input)
				} else {
					_, _ = manager.Update(ctx, input)
				}
			}()
			<-store.entered
			readDone := make(chan error, 1)
			go func() {
				if _, ok := manager.GetByID("B"); !ok {
					readDone <- errors.New("missing B")
					return
				}
				selected, err := manager.SelectAuth(ctx, "audit", "", cliproxyexecutor.Options{})
				if err == nil && (selected == nil || selected.ID != "B") {
					err = errors.New("did not select B")
				}
				readDone <- err
			}()
			select {
			case err := <-readDone:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(2 * time.Second):
				t.Error("blocked Save(A) prevented reading/selecting unrelated B")
			}
			close(store.release)
			<-done
		})
	}
}

type callbackAuthStore struct{ save func(*Auth) error }

func (*callbackAuthStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (*callbackAuthStore) Delete(context.Context, string) error  { return nil }
func (s *callbackAuthStore) Save(_ context.Context, auth *Auth) (string, error) {
	return "", s.save(auth)
}

func TestManagerSaveReadCallbackAndCustomFields(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		store := &callbackAuthStore{}
		manager := NewManager(store, nil, nil)
		store.save = func(auth *Auth) error {
			manager.GetByID(auth.ID)
			manager.List()
			auth.Prefix = "custom-prefix"
			auth.ProxyURL = "http://custom.invalid"
			auth.Metadata["custom"] = map[string]any{"value": "saved"}
			delete(auth.Metadata, "remove")
			return nil
		}
		for _, operation := range []string{"Register", "Update"} {
			input := &Auth{ID: "callback", Provider: "audit", Status: StatusActive, Metadata: map[string]any{"remove": true}}
			var result *Auth
			var err error
			if operation == "Register" {
				result, err = manager.Register(ctx, input)
			} else {
				result, err = manager.Update(ctx, input)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _ := manager.GetByID(input.ID)
			if got.Prefix != "custom-prefix" || got.ProxyURL != "http://custom.invalid" || got.Metadata["custom"] == nil {
				t.Fatalf("custom store fields missing: %+v", got)
			}
			if _, ok := got.Metadata["remove"]; ok {
				t.Fatal("store deletion lost")
			}
			if !reflect.DeepEqual(result, got) {
				t.Fatal("return differs from manager")
			}
		}
	})
}

func TestManagerSavePreservesConcurrentRefreshState(t *testing.T) {
	for _, operation := range []string{"Update", "MarkResult"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				store := &blockingEnrichingAuthStore{entered: make(chan struct{}), release: make(chan struct{})}
				manager := NewManager(store, nil, nil)
				base, err := manager.Register(WithSkipPersist(ctx), &Auth{ID: "runtime", Provider: "audit", Status: StatusActive, Metadata: map[string]any{"type": "audit"}})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					if operation == "Update" {
						_, _ = manager.Update(ctx, base.Clone())
					} else {
						manager.MarkResult(ctx, Result{AuthID: base.ID, Provider: base.Provider, Success: true})
					}
				}()
				<-store.entered
				job := manager.markRefreshPending(nil, base.ID, base.RegistrationEpoch, time.Now())
				if job == nil {
					t.Fatal("failed to schedule concurrent refresh")
				}
				close(store.release)
				<-done
				got, _ := manager.GetByID(base.ID)
				if !got.NextRefreshAfter.Equal(job.pendingUntil) {
					t.Fatal("save lost concurrent refresh scheduling")
				}
				if got.Attributes[AttributePath] == "" {
					t.Fatal("save enrichment lost")
				}
				if operation == "MarkResult" && got.Success != 1 {
					t.Fatal("save lost result counter")
				}
			})
		})
	}
}

func TestManagerSavedFieldsSkipPersistence(t *testing.T) {
	ctx := context.Background()
	store := &callbackAuthStore{save: func(*Auth) error { t.Fatal("unexpected Save"); return nil }}
	for _, mode := range []string{"context", "nil-metadata", "runtime-only", "config-api-key", "plugin-virtual"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewManager(store, nil, nil)
			input := &Auth{ID: mode, Provider: "audit", Status: StatusActive, Metadata: map[string]any{"type": "audit"}}
			saveCtx := ctx
			switch mode {
			case "context":
				saveCtx = WithSkipPersist(ctx)
			case "nil-metadata":
				input.Metadata = nil
			case "runtime-only":
				input.Attributes = map[string]string{"runtime_only": "true"}
			case "config-api-key":
				input.Attributes = map[string]string{"source": "config", "api_key": "test"}
			case "plugin-virtual":
				MarkPluginVirtualAuth(input, "fixture", 0)
			}
			for _, operation := range []string{"Register", "Update"} {
				var result *Auth
				var err error
				if operation == "Register" {
					result, err = manager.Register(saveCtx, input)
				} else {
					result, err = manager.Update(saveCtx, input)
				}
				if err != nil {
					t.Fatal(err)
				}
				got, _ := manager.GetByID(input.ID)
				if !reflect.DeepEqual(got, result) {
					t.Fatal("skip return differs from manager")
				}
			}
		})
	}
}

func TestMergeAuthSaveDeltaMapChanges(t *testing.T) {
	t.Run("empty-map", func(t *testing.T) {
		before := &Auth{}
		after := &Auth{Attributes: map[string]string{}, Metadata: map[string]any{}}
		target := before.Clone()
		mergeAuthSaveDelta(target, before, after, true)
		if !reflect.DeepEqual(target, after) {
			t.Fatal("lost explicit empty map enrichment")
		}
	})
	t.Run("delete-map-preserves-concurrent-key", func(t *testing.T) {
		before := &Auth{Metadata: map[string]any{"old": true}}
		after := before.Clone()
		after.Metadata = nil
		target := before.Clone()
		target.Metadata["concurrent"] = true
		mergeAuthSaveDelta(target, before, after, true)
		if len(target.Metadata) != 1 || target.Metadata["concurrent"] != true {
			t.Fatalf("map deletion lost: %v", target.Metadata)
		}
	})
	t.Run("conflicts", func(t *testing.T) {
		before := &Auth{Label: "old", Metadata: map[string]any{"same": "old", "other": "old"}}
		after := before.Clone()
		after.Label = "store"
		after.Metadata["same"] = "store"
		after.Metadata["other"] = "store"
		target := before.Clone()
		target.Label = "runtime"
		target.Metadata["same"] = "runtime"
		mergeAuthSaveDelta(target, before, after, true)
		if target.Label != "runtime" || target.Metadata["same"] != "runtime" || target.Metadata["other"] != "store" {
			t.Fatalf("incorrect merge: %+v", target)
		}
	})
}

func TestManagerSavedFieldsMetaMintPublication(t *testing.T) {
	for _, operation := range []string{"Prepare", "Refresh"} {
		for _, fail := range []bool{false, true} {
			name := operation + "/success"
			if fail {
				name = operation + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx := context.Background()
					store := &blockingEnrichingAuthStore{entered: make(chan struct{}), release: make(chan struct{})}
					if fail {
						store.err = errors.New("mint save failed")
					}
					hook := &savedFieldsHook{}
					manager := NewManager(store, nil, hook)
					base, err := manager.Register(WithSkipPersist(ctx), &Auth{ID: "meta-mint", Provider: "meta", Status: StatusActive, Metadata: map[string]any{"access_token": "old"}})
					if err != nil {
						t.Fatal(err)
					}
					updated := base.Clone()
					updated.Metadata["access_token"] = "minted"
					done := make(chan struct{})
					var returned *Auth
					var errUpdate error
					go func() {
						defer close(done)
						if operation == "Prepare" {
							returned, errUpdate = manager.UpdatePreparedAuth(ctx, base, updated)
						} else {
							returned, errUpdate = manager.UpdateRefreshedAuth(ctx, base, updated)
						}
					}()
					<-store.entered
					pending, _ := manager.GetByID(base.ID)
					if !reflect.DeepEqual(base, pending) {
						t.Fatal("mint published before persistence completed")
					}
					close(store.release)
					<-done
					got, _ := manager.GetByID(base.ID)
					if fail {
						if errUpdate == nil || returned != nil || !reflect.DeepEqual(base, got) || !reflect.DeepEqual(base, hook.auth) {
							t.Fatal("failed Meta mint changed published state")
						}
					} else {
						if errUpdate != nil || got.Metadata["access_token"] != "minted" || got.FileName == "" || !reflect.DeepEqual(got, returned) || !reflect.DeepEqual(got, hook.auth) {
							t.Fatalf("successful mint snapshots differ: %v", errUpdate)
						}
					}
				})
			})
		}
	}
}
