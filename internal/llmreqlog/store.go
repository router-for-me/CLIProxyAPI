package llmreqlog

import (
	"sync"
	"time"
)

const defaultCapacity = 2000

// Entry is one LLM request log row for the independent log page.
type Entry struct {
	ID              string    `json:"id"`
	Time            time.Time `json:"time"`
	Token           string    `json:"token"`
	Group           string    `json:"group"`
	Type            string    `json:"type"`
	Model           string    `json:"model"`
	LatencyMs       int64     `json:"latency_ms"`
	TTFTMs          int64     `json:"ttft_ms"`
	PromptTokens    int64     `json:"prompt_tokens"`
	CompletionTokens int64    `json:"completion_tokens"`
	Cost            float64   `json:"cost"`
	ExitIP          string    `json:"exit_ip"`
	ExitNode        string    `json:"exit_node"`
	ThinkingLevel   string    `json:"thinking_level"`
	HasThinking     bool      `json:"has_thinking"`
	ThinkingLen     int64     `json:"thinking_len"`
	Failed          bool      `json:"failed"`
	StatusCode      int       `json:"status_code"`
	Provider        string    `json:"provider"`
	Endpoint        string    `json:"endpoint"`
	RequestID       string    `json:"request_id"`
	AuthID          string    `json:"auth_id"`
	Source          string    `json:"source"`
	Detail          any       `json:"detail,omitempty"`
}

type store struct {
	mu       sync.RWMutex
	capacity int
	items    []Entry
}

var defaultStore = &store{capacity: defaultCapacity, items: make([]Entry, 0, defaultCapacity)}

func (s *store) add(entry Entry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.capacity <= 0 {
		s.capacity = defaultCapacity
	}
	s.items = append(s.items, entry)
	if len(s.items) > s.capacity {
		overflow := len(s.items) - s.capacity
		s.items = append([]Entry(nil), s.items[overflow:]...)
	}
}

func (s *store) updateExit(id, exitIP, exitNode string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.items) - 1; i >= 0; i-- {
		if s.items[i].ID == id {
			if exitIP != "" {
				s.items[i].ExitIP = exitIP
			}
			if exitNode != "" {
				s.items[i].ExitNode = exitNode
			}
			return
		}
	}
}

func (s *store) updateThinkByRequestID(requestID string, hasThinking bool, thinkingLen int64) {
	if s == nil || requestID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.items) - 1; i >= 0; i-- {
		if s.items[i].RequestID == requestID {
			s.items[i].HasThinking = hasThinking
			s.items[i].ThinkingLen = thinkingLen
			if detail, ok := s.items[i].Detail.(map[string]any); ok && detail != nil {
				detail["think_chars"] = thinkingLen
			}
			return
		}
	}
}

// UpdateThinkByRequestID backfills think field stats after the response is fully written.
func UpdateThinkByRequestID(requestID string, hasThinking bool, thinkingLen int64) {
	defaultStore.updateThinkByRequestID(requestID, hasThinking, thinkingLen)
}

func (s *store) list(limit, offset int) (items []Entry, total int) {
	if s == nil {
		return nil, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	total = len(s.items)
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []Entry{}, total
	}
	// newest first
	end := total - offset
	start := end - limit
	if start < 0 {
		start = 0
	}
	chunk := s.items[start:end]
	out := make([]Entry, len(chunk))
	for i := range chunk {
		out[len(chunk)-1-i] = chunk[i]
	}
	return out, total
}

// List returns newest-first entries.
func List(limit, offset int) ([]Entry, int) {
	return defaultStore.list(limit, offset)
}
