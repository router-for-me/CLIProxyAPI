package auth

import (
	"context"
	"sync/atomic"
	"testing"
)

type nestedUpdateHook struct {
	NoopHook
	manager *Manager
	fired   atomic.Bool
}

func (h *nestedUpdateHook) OnAuthUpdated(ctx context.Context, auth *Auth) {
	if auth == nil || !h.fired.CompareAndSwap(false, true) {
		return
	}
	updated := auth.Clone()
	if updated.Attributes == nil {
		updated.Attributes = make(map[string]string)
	}
	updated.Attributes["api_key"] = "key-b"
	_, _ = h.manager.Update(ctx, updated)
}

func TestUpdateReturnsLatestCommittedMetaAuthAfterHook(t *testing.T) {
	hook := &nestedUpdateHook{}
	manager := NewManager(nil, nil, hook)
	hook.manager = manager

	auth := &Auth{
		ID:       "meta-hook-return",
		Provider: "meta",
		Status:   StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "key-a",
		},
		Metadata: map[string]any{"type": "meta", "auth_kind": "oauth"},
	}
	registered, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	candidate := registered.Clone()
	candidate.Metadata["last_refresh"] = "2026-10-13T00:00:00Z"
	returned, errUpdate := manager.Update(context.Background(), candidate)
	if errUpdate != nil {
		t.Fatalf("Update() error = %v", errUpdate)
	}
	if returned == nil || returned.Attributes["api_key"] != "key-b" {
		t.Fatalf("Update() returned api_key = %v, want key-b from hook-committed update", returned)
	}
	latest, ok := manager.GetByID(auth.ID)
	if !ok || latest.Attributes["api_key"] != "key-b" {
		t.Fatalf("manager auth api_key = %v, want key-b", latest)
	}
	if returned.Generation != latest.Generation {
		t.Fatalf("returned generation = %d, want latest %d", returned.Generation, latest.Generation)
	}
}

type nestedRegisterHook struct {
	NoopHook
	manager *Manager
	fired   atomic.Bool
}

func (h *nestedRegisterHook) OnAuthRegistered(ctx context.Context, auth *Auth) {
	if auth == nil || !h.fired.CompareAndSwap(false, true) {
		return
	}
	updated := auth.Clone()
	if updated.Attributes == nil {
		updated.Attributes = make(map[string]string)
	}
	updated.Attributes["api_key"] = "key-b"
	_, _ = h.manager.Update(ctx, updated)
}

func TestRegisterReturnsLatestCommittedMetaAuthAfterHook(t *testing.T) {
	hook := &nestedRegisterHook{}
	manager := NewManager(nil, nil, hook)
	hook.manager = manager

	auth := &Auth{
		ID:       "meta-hook-register-return",
		Provider: "meta",
		Status:   StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "key-a",
		},
		Metadata: map[string]any{"type": "meta", "auth_kind": "oauth"},
	}
	returned, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if returned == nil || returned.Attributes["api_key"] != "key-b" {
		t.Fatalf("Register() returned api_key = %v, want key-b from hook-committed update", returned)
	}
	latest, ok := manager.GetByID(auth.ID)
	if !ok || latest.Attributes["api_key"] != "key-b" {
		t.Fatalf("manager auth api_key = %v, want key-b", latest)
	}
	if returned.Generation != latest.Generation {
		t.Fatalf("returned generation = %d, want latest %d", returned.Generation, latest.Generation)
	}
}
