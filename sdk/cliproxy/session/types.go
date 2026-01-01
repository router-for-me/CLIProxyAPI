package session

import (
	"time"
)

// Session represents a conversation session with message history and metadata.
type Session struct {
	// ID is the unique session identifier (UUID v4).
	ID string `json:"id"`
	// CreatedAt is the timestamp when the session was created.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the timestamp when the session was last modified.
	UpdatedAt time.Time `json:"updated_at"`
	// ExpiresAt is the timestamp when the session should be purged.
	ExpiresAt time.Time `json:"expires_at"`
	// Messages contains the conversation history.
	Messages []Message `json:"messages"`
	// Metadata tracks session context and provider affinity.
	Metadata SessionMetadata `json:"metadata"`
}

// Message represents a single message in the conversation history.
type Message struct {
	// Role identifies the message author (user, assistant, system, tool).
	Role string `json:"role"`
	// Content holds the message payload - either string or structured content blocks.
	// Type depends on provider format (OpenAI uses string, Claude uses []ContentBlock).
	Content interface{} `json:"content"`
	// Provider identifies which AI provider generated this message (gemini, claude, openai, etc.).
	Provider string `json:"provider"`
	// Model is the specific model used for this message.
	Model string `json:"model"`
	// Timestamp records when this message was created.
	Timestamp time.Time `json:"timestamp"`
	// TokensUsed tracks token consumption for this message (0 for user messages).
	TokensUsed int `json:"tokens_used,omitempty"`
	// Metadata holds provider-specific or custom metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// SessionMetadata tracks session-level context and statistics.
type SessionMetadata struct {
	// PreferredProvider is the provider to prefer for session affinity.
	// When set, the system will attempt to route requests to this provider.
	PreferredProvider string `json:"preferred_provider,omitempty"`
	// LastProvider is the provider used in the most recent message.
	LastProvider string `json:"last_provider,omitempty"`
	// TotalMessages counts all messages in the session (user + assistant).
	TotalMessages int `json:"total_messages"`
	// TotalTokens sums token usage across all messages.
	TotalTokens int `json:"total_tokens"`
	// UserContext stores arbitrary user-defined metadata.
	UserContext map[string]interface{} `json:"user_context,omitempty"`
}

// IsExpired checks if the session has passed its expiration time.
func (s *Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// AddMessage appends a message to the session and updates metadata.
// Also updates PreferredProvider for session affinity when assistant messages are added.
func (s *Session) AddMessage(msg Message) {
	s.Messages = append(s.Messages, msg)
	s.UpdatedAt = time.Now()
	s.Metadata.TotalMessages = len(s.Messages)
	s.Metadata.TotalTokens += msg.TokensUsed
	if msg.Role == "assistant" && msg.Provider != "" {
		s.Metadata.LastProvider = msg.Provider
		// Update PreferredProvider for session affinity
		s.Metadata.PreferredProvider = msg.Provider
	}
}

// GetHistory returns all messages suitable for injection into provider requests.
// Excludes system metadata while preserving conversation context.
func (s *Session) GetHistory() []Message {
	// Return a copy to prevent external mutation
	history := make([]Message, len(s.Messages))
	copy(history, s.Messages)
	return history
}

// Touch updates the session's UpdatedAt timestamp without modifying messages.
func (s *Session) Touch() {
	s.UpdatedAt = time.Now()
}
