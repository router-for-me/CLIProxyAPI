package executionregistry

import (
	"sync"
	"testing"
)

type recordingReleaseSink struct {
	mu        sync.Mutex
	sequences map[ReleaseGroup]int64
}

func (s *recordingReleaseSink) MarkDirty(group ReleaseGroup, sequence int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sequences == nil {
		s.sequences = make(map[ReleaseGroup]int64)
	}
	if sequence > s.sequences[group] {
		s.sequences[group] = sequence
	}
}

func (s *recordingReleaseSink) Sequence(credentialID, model string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sequences[ReleaseGroup{CredentialID: credentialID, Model: model}]
}

func installAccountedScope(t *testing.T, registry *Registry, credentialID, model string) *Scope {
	t.Helper()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, ScopeSpec{CredentialID: credentialID, Model: model, Accounted: true})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	return scope
}

func TestRegistryEndMarksOneDirtyGroup(t *testing.T) {
	sink := &recordingReleaseSink{}
	registry := New()
	registry.SetReleaseSink(sink.MarkDirty)

	scope := installAccountedScope(t, registry, "cred-1", "gpt")
	scope.End("complete")
	scope.End("duplicate")

	if got := sink.Sequence("cred-1", "gpt"); got != 1 {
		t.Fatalf("release sequence = %d, want 1", got)
	}
}

func TestUnaccountedScopeDoesNotRelease(t *testing.T) {
	sink := &recordingReleaseSink{}
	registry := New()
	registry.SetReleaseSink(sink.MarkDirty)
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, ScopeSpec{CredentialID: "cred-1", Model: "gpt", Accounted: false})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	scope.End("observation_complete")

	if got := sink.Sequence("cred-1", "gpt"); got != 0 {
		t.Fatalf("release sequence = %d, want 0", got)
	}
}

func TestSetReleaseSinkReplaysExistingSequences(t *testing.T) {
	registry := New()
	installAccountedScope(t, registry, "cred-1", "gpt").End("complete")

	sink := &recordingReleaseSink{}
	registry.SetReleaseSink(sink.MarkDirty)
	if got := sink.Sequence("cred-1", "gpt"); got != 1 {
		t.Fatalf("replayed release sequence = %d, want 1", got)
	}
}
