package claude

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
)

const (
	autoModeSessionCapacity = 4096
	autoModeSessionTTL      = 24 * time.Hour
	autoModeMaxModelBytes   = 256
)

type autoModeSessionModel struct {
	model string
	seen  time.Time
}

type autoModeSessionModels struct {
	mu      sync.Mutex
	entries map[[32]byte]autoModeSessionModel
}

func (s *autoModeSessionModels) route(headers http.Header, provider, principal string, body []byte, now time.Time) []byte {
	if provider == "" || principal == "" {
		return body
	}
	session := headers.Get("X-Claude-Code-Session-Id")
	if session == "" {
		metadata := ojson.GetBytesNoCopy(body, "metadata.user_id").String()
		session = ojson.Get(metadata, "session_id").String()
	}
	if session == "" || len(session) > 256 {
		return body
	}
	key := sha256.Sum256(fmt.Appendf(nil, "%q:%q:%q", provider, principal, session))
	switch headers.Get("X-Claude-Code-Request-Class") {
	case "main":
		model := ojson.GetBytesNoCopy(body, "model")
		if name := model.String(); model.Type == ojson.String && name != "" && len(name) <= autoModeMaxModelBytes {
			s.remember(key, name, now)
		}
	case "auxiliary":
		if ojson.GetBytesNoCopy(body, "tools.#").Int() != 0 ||
			ojson.GetBytesNoCopy(body, "messages.#").Int() != 1 ||
			ojson.GetBytesNoCopy(body, "messages.0.role").String() != "user" ||
			!strings.HasPrefix(ojson.GetBytesNoCopy(body, "messages.0.content.0.text").String(), "<transcript>\n") {
			return body
		}
		if model := s.lookup(key, now); model != "" {
			doc := ojson.NewDoc(body)
			if err := doc.Set("model", model); err == nil {
				return doc.Bytes()
			}
		}
	}
	return body
}

func (s *autoModeSessionModels) remember(key [32]byte, model string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.entries == nil {
		s.entries = make(map[[32]byte]autoModeSessionModel)
	}
	if _, exists := s.entries[key]; !exists && len(s.entries) >= autoModeSessionCapacity {
		var oldest [32]byte
		var oldestTime time.Time
		var foundOldest bool
		for k, entry := range s.entries {
			if now.Sub(entry.seen) > autoModeSessionTTL {
				delete(s.entries, k)
			} else if !foundOldest || entry.seen.Before(oldestTime) {
				oldest, oldestTime, foundOldest = k, entry.seen, true
			}
		}
		if len(s.entries) >= autoModeSessionCapacity {
			delete(s.entries, oldest)
		}
	}
	// Avoid retaining the request backing buffer through a no-copy JSON result.
	s.entries[key] = autoModeSessionModel{model: strings.Clone(model), seen: now}
}

func (s *autoModeSessionModels) lookup(key [32]byte, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		return ""
	}
	if now.Sub(entry.seen) > autoModeSessionTTL {
		delete(s.entries, key)
		return ""
	}
	return entry.model
}
