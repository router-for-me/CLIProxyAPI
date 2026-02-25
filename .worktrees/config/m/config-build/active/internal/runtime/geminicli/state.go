package geminicli

import (
	"strings"
	"sync"
)

// SharedCredential keeps canonical OAuth metadata for a multi-project Gemini CLI login.
type SharedCredential struct {
	primaryID  string
	email      string
	metadata   map[string]any
	projectIDs []string
	mu         sync.RWMutex
}

// NewSharedCredential builds a shared credential container for the given primary entry.
func NewSharedCredential(primaryID, email string, metadata map[string]any, projectIDs []string) *SharedCredential {
	return &SharedCredential{
		primaryID:  strings.TrimSpace(primaryID),
		email:      strings.TrimSpace(email),
		metadata:   cloneMap(metadata),
		projectIDs: cloneStrings(projectIDs),
	}
}

// PrimaryID returns the owning credential identifier.
func (s *SharedCredential) PrimaryID() string {
	if s == nil {
		return ""
	}
	return s.primaryID
}

// Email returns the associated account email.
func (s *SharedCredential) Email() string {
	if s == nil {
		return ""
	}
	return s.email
}

// ProjectIDs returns a snapshot of the configured project identifiers.
func (s *SharedCredential) ProjectIDs() []string {
	if s == nil {
		return nil
	}
	return cloneStrings(s.projectIDs)
}

// MetadataSnapshot returns a deep copy of the stored OAuth metadata.
func (s *SharedCredential) MetadataSnapshot() map[string]any {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMap(s.metadata)
}

// MergeMetadata merges the provided fields into the shared metadata and returns an updated copy.
func (s *SharedCredential) MergeMetadata(values map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	if len(values) == 0 {
		return s.MetadataSnapshot()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metadata == nil {
		s.metadata = make(map[string]any, len(values))
	}
	for k, v := range values {
		if v == nil {
			delete(s.metadata, k)
			continue
		}
		s.metadata[k] = v
	}
	return cloneMap(s.metadata)
}

// SetProjectIDs updates the stored project identifiers.
func (s *SharedCredential) SetProjectIDs(ids []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.projectIDs = cloneStrings(ids)
	s.mu.Unlock()
}

// VirtualCredential tracks a per-project virtual auth entry that reuses a primary credential.
type VirtualCredential struct {
	ProjectID string
	Parent    *SharedCredential
}

// NewVirtualCredential creates a virtual credential descriptor bound to the shared parent.
func NewVirtualCredential(projectID string, parent *SharedCredential) *VirtualCredential {
	return &VirtualCredential{ProjectID: strings.TrimSpace(projectID), Parent: parent}
}

// ResolveSharedCredential returns the shared credential backing the provided runtime payload.
func ResolveSharedCredential(runtime any) *SharedCredential {
	switch typed := runtime.(type) {
	case *SharedCredential:
		return typed
	case *VirtualCredential:
		return typed.Parent
	default:
		return nil
	}
}

// IsVirtual reports whether the runtime payload represents a virtual credential.
func IsVirtual(runtime any) bool {
	if runtime == nil {
		return false
	}
	_, ok := runtime.(*VirtualCredential)
	return ok
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
