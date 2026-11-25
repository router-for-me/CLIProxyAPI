package auth

import (
	"context"
	"testing"
)

func TestConductorLifecycleExecutorRegistrationNormalizesCase(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	exec := &replaceAwareExecutor{id: "  OpenAI-Compat  "}

	rev := m.RegisterExecutorWithRevision(exec)
	if rev != 1 {
		t.Fatalf("expected initial revision 1, got %d", rev)
	}
	if got := m.ExecutorRevision("OpenAI-Compat"); got != 1 {
		t.Fatalf("expected ExecutorRevision(mixed-case) == 1, got %d", got)
	}
	if got := m.ExecutorRevision("openai-compat"); got != 1 {
		t.Fatalf("expected ExecutorRevision(lowercase) == 1, got %d", got)
	}

	m.mu.RLock()
	storedLower := m.executors["openai-compat"]
	storedRaw := m.executors["OpenAI-Compat"]
	m.mu.RUnlock()
	if storedLower != exec {
		t.Fatalf("expected executor stored under lowercase key, got %v", storedLower)
	}
	if storedRaw != nil {
		t.Fatalf("expected no executor stored under mixed-case key, got %v", storedRaw)
	}

	m.UnregisterExecutor("OpenAI-Compat")
	if got := m.ExecutorRevision("openai-compat"); got != 2 {
		t.Fatalf("expected ExecutorRevision after unregister == 2, got %d", got)
	}

	m.mu.RLock()
	remaining := len(m.executors)
	m.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("expected executors map to be empty after unregister, got %d entries", remaining)
	}
}

func TestConductorLifecycleRemoveIfCleansModelPoolOffsets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m := NewManager(nil, nil, nil)

	auth1 := &Auth{ID: "Auth-1", Provider: "openai-compat"}
	auth10 := &Auth{ID: "Auth-10", Provider: "openai-compat"}
	if _, err := m.Register(ctx, auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(ctx, auth10); err != nil {
		t.Fatalf("register auth10: %v", err)
	}

	key1a := openAICompatModelPoolKey(auth1, "gpt-4o")
	key1b := openAICompatModelPoolKey(auth1, "gpt-4o-mini")
	key10 := openAICompatModelPoolKey(auth10, "gpt-4o")

	_ = m.nextModelPoolOffset(key1a, 2)
	_ = m.nextModelPoolOffset(key1b, 2)
	_ = m.nextModelPoolOffset(key10, 2)

	if removed := m.RemoveIf(ctx, auth1.ID, nil); !removed {
		t.Fatalf("expected RemoveIf(%q) to return true", auth1.ID)
	}

	m.mu.RLock()
	_, has1a := m.modelPoolOffsets[key1a]
	_, has1b := m.modelPoolOffsets[key1b]
	_, has10 := m.modelPoolOffsets[key10]
	m.mu.RUnlock()

	if has1a || has1b {
		t.Fatalf("expected modelPoolOffsets entries for %q to be deleted, got has1a=%v has1b=%v", auth1.ID, has1a, has1b)
	}
	if !has10 {
		t.Fatalf("expected unrelated modelPoolOffsets entry %q to remain", key10)
	}
}
