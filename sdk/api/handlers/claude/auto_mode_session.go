package claude

import (
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type autoModeSessionModel struct {
	model string
	seen  time.Time
}
type autoModeSessionModels struct {
	mu      sync.Mutex
	entries map[[32]byte]autoModeSessionModel
}

// Routing remembers only model names, never transcripts or raw credentials.
// Unrecognized requests and sessions without a main request retain native routing.
func (s *autoModeSessionModels) route(headers http.Header, body []byte, now time.Time) []byte {
	session := headers.Get("X-Claude-Code-Session-Id")
	if session == "" {
		metadata := gjson.GetBytes(body, "metadata.user_id").String()
		session = gjson.Get(metadata, "session_id").String()
	}
	if session == "" || len(session) > 256 {
		return body
	}
	key := sha256.Sum256([]byte(headers.Get("Authorization") + "\x00" + headers.Get("X-Api-Key") + "\x00" + session))
	class := headers.Get("X-Claude-Code-Request-Class")
	model := gjson.GetBytes(body, "model").String()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[[32]byte]autoModeSessionModel)
	}
	if class == "main" && model != "" {
		if len(s.entries) >= 4096 {
			var oldest [32]byte
			oldestTime := now
			for k, v := range s.entries {
				if now.Sub(v.seen) > 24*time.Hour {
					delete(s.entries, k)
				} else if v.seen.Before(oldestTime) {
					oldest, oldestTime = k, v.seen
				}
			}
			if len(s.entries) >= 4096 {
				delete(s.entries, oldest)
			}
		}
		s.entries[key] = autoModeSessionModel{model, now}
		return body
	}
	// Never reinterpret ordinary auxiliary work as a permission classifier.
	if class != "auxiliary" || gjson.GetBytes(body, "tools.#").Int() != 0 || gjson.GetBytes(body, "messages.#").Int() != 1 || gjson.GetBytes(body, "messages.0.role").String() != "user" {
		return body
	}
	first := gjson.GetBytes(body, "messages.0.content.0.text").String()
	if !strings.HasPrefix(first, "<transcript>\n") {
		return body
	}
	entry, ok := s.entries[key]
	if !ok || now.Sub(entry.seen) > 24*time.Hour {
		delete(s.entries, key)
		return body
	}
	updated, err := sjson.SetBytes(body, "model", entry.model)
	if err != nil {
		return body
	}
	return updated
}
