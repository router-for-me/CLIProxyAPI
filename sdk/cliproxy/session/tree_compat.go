package session

import (
	"sync"
	"time"
)

// SessionTreeNode represents a node in the session hierarchy.
// Deprecated: Session tree management and multi-node lineage have moved to Home.
// This struct is maintained as a migration stub for SDK backward compatibility.
type SessionTreeNode struct {
	SessionID       string         `json:"session_id"`
	ParentSessionID string         `json:"parent_session_id,omitempty"`
	RootSessionID   string         `json:"root_session_id"`
	TreePath        string         `json:"tree_path"`
	TreeDepth       int            `json:"tree_depth"`
	AgentName       string         `json:"agent_name,omitempty"`
	ClientType      string         `json:"client_type,omitempty"`
	CallerScope     string         `json:"caller_scope,omitempty"`
	LastAuthID      string         `json:"last_auth_id,omitempty"`
	LastProvider    string         `json:"last_provider,omitempty"`
	LastModel       string         `json:"last_model,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// Clone returns a deep copy of the node.
// Deprecated: Session tree management has moved to Home.
func (n *SessionTreeNode) Clone() *SessionTreeNode {
	if n == nil {
		return nil
	}
	res := *n
	if n.Metadata != nil {
		res.Metadata = make(map[string]any, len(n.Metadata))
		for k, v := range n.Metadata {
			res.Metadata[k] = v
		}
	}
	return &res
}

// SessionTreeStore defines the interface for recording and querying session trees.
// Deprecated: Session tree management has moved to Home.
type SessionTreeStore interface {
	RecordNode(info SessionTreeInfo) *SessionTreeNode
	GetNode(sessionID string) (*SessionTreeNode, bool)
	GetTree(rootSessionID string) []*SessionTreeNode
	GetSubtree(sessionID string) []*SessionTreeNode
	Ancestors(sessionID string) []string
	UpdateAffinity(sessionID, authID, provider, model string) bool
	Len() int
	Clear()
}

// InMemorySessionTreeStore is a backward-compatible in-memory session tree stub.
// Deprecated: Standalone CPA nodes use flat KV session cache (SessionCache).
// Full hierarchical session tree management and global aggregation are handled by Home.
type InMemorySessionTreeStore struct {
	mu    sync.RWMutex
	nodes map[string]*SessionTreeNode
}

// NewInMemorySessionTreeStore creates a new in-memory session tree store stub.
// Deprecated: Standalone CPA nodes use flat KV session cache.
func NewInMemorySessionTreeStore(maxNodes int, ttl time.Duration) *InMemorySessionTreeStore {
	return &InMemorySessionTreeStore{
		nodes: make(map[string]*SessionTreeNode),
	}
}

// RecordNode records a session tree node stub.
// Deprecated: Session tree management has moved to Home.
func (s *InMemorySessionTreeStore) RecordNode(info SessionTreeInfo) *SessionTreeNode {
	if s == nil || info.SessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	node := &SessionTreeNode{
		SessionID:       info.SessionID,
		ParentSessionID: info.ParentSessionID,
		RootSessionID:   info.SessionID,
		TreePath:        info.SessionID,
		TreeDepth:       0,
		AgentName:       info.AgentName,
		ClientType:      info.ClientType,
		CallerScope:     info.CallerScope,
		LastAuthID:      info.AuthID,
		LastProvider:    info.Provider,
		LastModel:       info.Model,
		CreatedAt:       now,
		UpdatedAt:       now,
		Metadata:        info.Metadata,
	}
	if info.ParentSessionID != "" {
		node.RootSessionID = info.ParentSessionID
		node.TreePath = info.ParentSessionID + "/" + info.SessionID
		node.TreeDepth = 1
	}
	s.nodes[info.SessionID] = node
	return node.Clone()
}

// GetNode returns a recorded node.
// Deprecated: Session tree management has moved to Home.
func (s *InMemorySessionTreeStore) GetNode(sessionID string) (*SessionTreeNode, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.nodes[sessionID]
	if !ok {
		return nil, false
	}
	return node.Clone(), true
}

// GetTree returns nodes under rootSessionID.
// Deprecated: Session tree management has moved to Home.
func (s *InMemorySessionTreeStore) GetTree(rootSessionID string) []*SessionTreeNode {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var res []*SessionTreeNode
	for _, n := range s.nodes {
		if n.RootSessionID == rootSessionID || n.SessionID == rootSessionID {
			res = append(res, n.Clone())
		}
	}
	return res
}

// GetSubtree returns subtree rooted at sessionID.
// Deprecated: Session tree management has moved to Home.
func (s *InMemorySessionTreeStore) GetSubtree(sessionID string) []*SessionTreeNode {
	return s.GetTree(sessionID)
}

// Ancestors returns ancestor session IDs.
// Deprecated: Session tree management has moved to Home.
func (s *InMemorySessionTreeStore) Ancestors(sessionID string) []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if node, ok := s.nodes[sessionID]; ok && node.ParentSessionID != "" {
		return []string{node.ParentSessionID}
	}
	return nil
}

// UpdateAffinity updates the latest successful upstream binding.
// Deprecated: Session tree management has moved to Home.
func (s *InMemorySessionTreeStore) UpdateAffinity(sessionID, authID, provider, model string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if node, ok := s.nodes[sessionID]; ok {
		node.UpdatedAt = time.Now()
		if authID != "" {
			node.LastAuthID = authID
		}
		if provider != "" {
			node.LastProvider = provider
		}
		if model != "" {
			node.LastModel = model
		}
		return true
	}
	return false
}

// Len returns the number of nodes.
// Deprecated: Session tree management has moved to Home.
func (s *InMemorySessionTreeStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.nodes)
}

// Clear flushes all nodes.
// Deprecated: Session tree management has moved to Home.
func (s *InMemorySessionTreeStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = make(map[string]*SessionTreeNode)
}
