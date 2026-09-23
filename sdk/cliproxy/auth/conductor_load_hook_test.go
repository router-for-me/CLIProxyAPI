package auth

import (
	"context"
	"errors"
	"testing"
)

type loadHookStore struct {
	auths []*Auth
	err   error
}

func (s *loadHookStore) List(context.Context) ([]*Auth, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]*Auth, 0, len(s.auths))
	for _, auth := range s.auths {
		if auth == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, auth.Clone())
	}
	return out, nil
}

func (s *loadHookStore) Save(context.Context, *Auth) (string, error) { return "", nil }

func (s *loadHookStore) Delete(context.Context, string) error { return nil }

type loadHookCtxKey struct{}

type loadRegisterCall struct {
	id       string
	provider string
	epoch    uint64
	gen      uint64
	label    string
	ctxVal   string
	visible  bool
	cloned   bool
}

type loadRegisterHook struct {
	NoopHook
	mgr      *Manager
	calls    []loadRegisterCall
	lockHeld bool
}

func (h *loadRegisterHook) OnAuthRegistered(ctx context.Context, auth *Auth) {
	if h.mgr != nil && !h.mgr.mu.TryRLock() {
		h.lockHeld = true
		return
	}
	if h.mgr != nil {
		stored := h.mgr.auths[authIDOf(auth)]
		h.mgr.mu.RUnlock()
		call := loadRegisterCall{cloned: auth != nil && stored != auth}
		if auth != nil {
			call.id = auth.ID
			call.provider = auth.Provider
			call.epoch = auth.RegistrationEpoch
			call.gen = auth.Generation
			call.label = auth.Label
			if got, ok := h.mgr.GetByID(auth.ID); ok && got != nil && got.Label == auth.Label && got.RegistrationEpoch == auth.RegistrationEpoch {
				call.visible = true
			}
			auth.Label = "mutated-by-hook"
		}
		if ctx != nil {
			if v, _ := ctx.Value(loadHookCtxKey{}).(string); v != "" {
				call.ctxVal = v
			}
		}
		h.calls = append(h.calls, call)
		return
	}
	if auth == nil {
		h.calls = append(h.calls, loadRegisterCall{})
		return
	}
	h.calls = append(h.calls, loadRegisterCall{id: auth.ID, provider: auth.Provider})
}

func authIDOf(auth *Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

func TestManagerLoadFiresOnAuthRegisteredOutsideLock(t *testing.T) {
	store := &loadHookStore{auths: []*Auth{
		{ID: "keep", Provider: "gemini", Label: "kept", Status: StatusActive, RegistrationEpoch: 5, Generation: 10},
		{ID: "drop", Provider: "gemini", Label: "dropped", Status: StatusActive, RegistrationEpoch: 3, Generation: 20},
		nil,
		{ID: "", Provider: "gemini"},
		{ID: "bad-weight", Provider: "gemini", Metadata: map[string]any{AttributeWeight: "nonnumeric"}},
	}}
	hook := &loadRegisterHook{}
	mgr := NewManager(store, nil, hook)
	hook.mgr = mgr

	ctx := context.WithValue(context.Background(), loadHookCtxKey{}, "load-ctx")
	if errLoad := mgr.Load(ctx); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	if hook.lockHeld {
		t.Fatal("OnAuthRegistered ran while the manager lock was held")
	}

	got := make(map[string]loadRegisterCall, len(hook.calls))
	for _, call := range hook.calls {
		got[call.id] = call
	}
	if _, notified := got["drop"]; !notified {
		t.Fatal("installed auth drop was not registered")
	}
	if _, notified := got["bad-weight"]; notified {
		t.Fatal("invalid weight auth fired OnAuthRegistered")
	}
	if _, notified := got[""]; notified {
		t.Fatal("empty id fired OnAuthRegistered")
	}
	if len(got) != 2 {
		t.Fatalf("registered calls = %+v, want keep and drop", hook.calls)
	}
	keep := got["keep"]
	if keep.provider != "gemini" || keep.epoch != 6 || keep.gen != 1 || keep.label != "kept" || keep.ctxVal != "load-ctx" || !keep.visible || !keep.cloned {
		t.Fatalf("keep registration = %+v", keep)
	}
	drop := got["drop"]
	if drop.provider != "gemini" || drop.epoch != 4 || drop.gen != 1 || drop.label != "dropped" || !drop.visible || !drop.cloned {
		t.Fatalf("drop registration = %+v", drop)
	}
	stored, ok := mgr.GetByID("keep")
	if !ok || stored.Label != "kept" {
		t.Fatalf("hook mutated stored auth: %#v ok=%v", stored, ok)
	}
	if _, ok := mgr.GetByID("bad-weight"); ok {
		t.Fatal("invalid weight auth was installed")
	}

	hook.calls = nil
	hook.lockHeld = false
	store.auths = []*Auth{
		{ID: "keep", Provider: "gemini", Label: "kept", Status: StatusActive, RegistrationEpoch: 6, Generation: 1},
	}
	if errLoad := mgr.Load(ctx); errLoad != nil {
		t.Fatalf("reload error = %v", errLoad)
	}
	if hook.lockHeld {
		t.Fatal("reload OnAuthRegistered ran while the manager lock was held")
	}
	if len(hook.calls) != 1 || hook.calls[0].id != "keep" || hook.calls[0].epoch != 7 || !hook.calls[0].visible {
		t.Fatalf("reload registrations = %+v, want only reinstalled keep at epoch 7", hook.calls)
	}
	if _, ok := mgr.GetByID("drop"); ok {
		t.Fatal("removed auth remained installed")
	}
}

func TestManagerLoadDoesNotNotifyWhenNothingInstalled(t *testing.T) {
	hook := &loadRegisterHook{}
	nilStoreMgr := NewManager(nil, nil, hook)
	if errLoad := nilStoreMgr.Load(context.Background()); errLoad != nil {
		t.Fatalf("nil store Load() error = %v", errLoad)
	}
	if len(hook.calls) != 0 {
		t.Fatalf("nil store notifications = %+v", hook.calls)
	}

	errStore := &loadHookStore{err: errors.New("list failed")}
	errMgr := NewManager(errStore, nil, hook)
	if errLoad := errMgr.Load(context.Background()); errLoad == nil {
		t.Fatal("Load() with list error returned nil")
	}
	if len(hook.calls) != 0 {
		t.Fatalf("list error notifications = %+v", hook.calls)
	}

	empty := &loadHookStore{}
	emptyMgr := NewManager(empty, nil, hook)
	hook.mgr = emptyMgr
	if _, errRegister := emptyMgr.Register(context.Background(), &Auth{ID: "gone", Provider: "gemini", Metadata: map[string]any{"type": "test"}}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if len(hook.calls) != 1 || hook.calls[0].id != "gone" || !hook.calls[0].visible || hook.lockHeld {
		t.Fatalf("Register notification = %+v lockHeld=%v", hook.calls, hook.lockHeld)
	}
	hook.calls = nil
	hook.lockHeld = false
	if errLoad := emptyMgr.Load(context.Background()); errLoad != nil {
		t.Fatalf("empty Load() error = %v", errLoad)
	}
	if len(hook.calls) != 0 || hook.lockHeld {
		t.Fatalf("removal-only Load notifications = %+v lockHeld=%v, want none", hook.calls, hook.lockHeld)
	}
	if _, ok := emptyMgr.GetByID("gone"); ok {
		t.Fatal("removed auth still installed")
	}
}
