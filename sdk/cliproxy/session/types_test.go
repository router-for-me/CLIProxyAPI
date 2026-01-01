package session

import (
	"testing"
	"time"
)

func TestSession_IsExpired(t *testing.T) {
	t.Run("not expired when future", func(t *testing.T) {
		session := &Session{
			ExpiresAt: time.Now().Add(time.Hour),
		}
		if session.IsExpired() {
			t.Error("session should not be expired")
		}
	})

	t.Run("expired when past", func(t *testing.T) {
		session := &Session{
			ExpiresAt: time.Now().Add(-time.Hour),
		}
		if !session.IsExpired() {
			t.Error("session should be expired")
		}
	})

	t.Run("expired when exactly now (edge case)", func(t *testing.T) {
		// Set expiry to a moment in the past to avoid race
		session := &Session{
			ExpiresAt: time.Now().Add(-time.Millisecond),
		}
		if !session.IsExpired() {
			t.Error("session at exact expiry time should be considered expired")
		}
	})
}

func TestSession_AddMessage(t *testing.T) {
	t.Run("appends message and updates metadata", func(t *testing.T) {
		session := &Session{
			Messages: []Message{},
			Metadata: SessionMetadata{},
		}

		msg := Message{
			Role:       "user",
			Content:    "Hello",
			TokensUsed: 5,
		}

		session.AddMessage(msg)

		if len(session.Messages) != 1 {
			t.Errorf("messages count = %d, want 1", len(session.Messages))
		}
		if session.Metadata.TotalMessages != 1 {
			t.Errorf("TotalMessages = %d, want 1", session.Metadata.TotalMessages)
		}
		if session.Metadata.TotalTokens != 5 {
			t.Errorf("TotalTokens = %d, want 5", session.Metadata.TotalTokens)
		}
	})

	t.Run("accumulates tokens", func(t *testing.T) {
		session := &Session{
			Messages: []Message{},
			Metadata: SessionMetadata{},
		}

		session.AddMessage(Message{Role: "user", Content: "Hi", TokensUsed: 10})
		session.AddMessage(Message{Role: "assistant", Content: "Hello!", TokensUsed: 20})
		session.AddMessage(Message{Role: "user", Content: "How are you?", TokensUsed: 15})

		if session.Metadata.TotalTokens != 45 {
			t.Errorf("TotalTokens = %d, want 45", session.Metadata.TotalTokens)
		}
		if session.Metadata.TotalMessages != 3 {
			t.Errorf("TotalMessages = %d, want 3", session.Metadata.TotalMessages)
		}
	})

	t.Run("updates PreferredProvider for assistant messages", func(t *testing.T) {
		session := &Session{
			Messages: []Message{},
			Metadata: SessionMetadata{},
		}

		// User message should not set provider
		session.AddMessage(Message{Role: "user", Content: "Hi", Provider: "openai"})
		if session.Metadata.PreferredProvider != "" {
			t.Error("PreferredProvider should not be set for user messages")
		}

		// Assistant message should set provider
		session.AddMessage(Message{Role: "assistant", Content: "Hello", Provider: "claude"})
		if session.Metadata.PreferredProvider != "claude" {
			t.Errorf("PreferredProvider = %v, want claude", session.Metadata.PreferredProvider)
		}
		if session.Metadata.LastProvider != "claude" {
			t.Errorf("LastProvider = %v, want claude", session.Metadata.LastProvider)
		}

		// New assistant message should update provider
		session.AddMessage(Message{Role: "assistant", Content: "Yes", Provider: "gemini"})
		if session.Metadata.PreferredProvider != "gemini" {
			t.Errorf("PreferredProvider = %v, want gemini", session.Metadata.PreferredProvider)
		}
	})

	t.Run("updates UpdatedAt timestamp", func(t *testing.T) {
		session := &Session{
			UpdatedAt: time.Now().Add(-time.Hour),
			Messages:  []Message{},
			Metadata:  SessionMetadata{},
		}
		oldUpdatedAt := session.UpdatedAt

		session.AddMessage(Message{Role: "user", Content: "test"})

		if !session.UpdatedAt.After(oldUpdatedAt) {
			t.Error("UpdatedAt should be updated")
		}
	})
}

func TestSession_GetHistory(t *testing.T) {
	t.Run("returns all messages", func(t *testing.T) {
		session := &Session{
			Messages: []Message{
				{Role: "user", Content: "First"},
				{Role: "assistant", Content: "Second"},
				{Role: "user", Content: "Third"},
			},
		}

		history := session.GetHistory()

		if len(history) != 3 {
			t.Errorf("history count = %d, want 3", len(history))
		}
		if history[0].Content != "First" {
			t.Error("first message mismatch")
		}
		if history[2].Content != "Third" {
			t.Error("last message mismatch")
		}
	})

	t.Run("returns empty slice for no messages", func(t *testing.T) {
		session := &Session{
			Messages: []Message{},
		}

		history := session.GetHistory()

		if history == nil {
			t.Error("should return empty slice, not nil")
		}
		if len(history) != 0 {
			t.Errorf("history count = %d, want 0", len(history))
		}
	})
}

func TestSession_Touch(t *testing.T) {
	t.Run("updates UpdatedAt without changing messages", func(t *testing.T) {
		session := &Session{
			UpdatedAt: time.Now().Add(-time.Hour),
			Messages: []Message{
				{Role: "user", Content: "test"},
			},
			Metadata: SessionMetadata{TotalMessages: 1},
		}
		oldUpdatedAt := session.UpdatedAt
		oldMessageCount := len(session.Messages)

		session.Touch()

		if !session.UpdatedAt.After(oldUpdatedAt) {
			t.Error("UpdatedAt should be updated")
		}
		if len(session.Messages) != oldMessageCount {
			t.Error("messages should not change")
		}
	})
}

func TestMessage_Serialization(t *testing.T) {
	t.Run("content can be string", func(t *testing.T) {
		msg := Message{
			Role:    "user",
			Content: "Hello, world!",
		}

		content, ok := msg.Content.(string)
		if !ok {
			t.Error("content should be string type")
		}
		if content != "Hello, world!" {
			t.Errorf("content = %v, want 'Hello, world!'", content)
		}
	})

	t.Run("content can be structured (Claude format)", func(t *testing.T) {
		contentBlocks := []interface{}{
			map[string]interface{}{"type": "text", "text": "Hello"},
			map[string]interface{}{"type": "image", "source": "..."},
		}

		msg := Message{
			Role:    "user",
			Content: contentBlocks,
		}

		blocks, ok := msg.Content.([]interface{})
		if !ok {
			t.Error("content should be slice type")
		}
		if len(blocks) != 2 {
			t.Errorf("content blocks = %d, want 2", len(blocks))
		}
	})
}

func TestSessionMetadata(t *testing.T) {
	t.Run("stores user context", func(t *testing.T) {
		metadata := SessionMetadata{
			UserContext: map[string]interface{}{
				"user_id":    "12345",
				"tenant":     "acme",
				"is_premium": true,
			},
		}

		if metadata.UserContext["user_id"] != "12345" {
			t.Error("user_id mismatch")
		}
		if metadata.UserContext["is_premium"] != true {
			t.Error("is_premium mismatch")
		}
	})
}
