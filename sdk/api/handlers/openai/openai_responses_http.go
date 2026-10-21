package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coresession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const responsesHTTPContextKey = "openai.responses.http.continuation"

type responsesHTTPExecution struct {
	handler    *OpenAIResponsesAPIHandler
	scope      string
	previousID string
	input      []byte
	model      string
	store      bool
	pinnedID   string
	mu         sync.Mutex
	authID     string
}

func responsesHTTPCallerScope(c *gin.Context) string {
	ctx := context.WithValue(c.Request.Context(), "gin", c)
	key := usage.APIKeyFromContext(ctx)
	if key == "" {
		// No-auth servers intentionally share one anonymous namespace per handler.
		return "anonymous"
	}
	return coresession.CallerScope(usage.AccessProviderFromContext(ctx) + "\x00" + key)
}

func (h *OpenAIResponsesAPIHandler) prepareResponsesHTTP(c *gin.Context, raw []byte) ([]byte, bool) {
	fail := func(status int, message, code, param string) ([]byte, bool) {
		c.JSON(status, gin.H{"error": gin.H{"message": message, "type": "invalid_request_error", "code": code, "param": param}})
		return nil, false
	}
	if !json.Valid(raw) || !util.ParseGJSONBytesNoCopy(raw).IsObject() {
		return fail(400, "Invalid JSON request", "invalid_request", "input")
	}
	input := util.GetGJSONBytesNoCopy(raw, "input")
	normalized := []byte("[]")
	switch {
	case input.Type == gjson.String:
		const prefix = `[{"type":"message","role":"user","content":[{"type":"input_text","text":`
		normalized = make([]byte, 0, len(prefix)+len(input.Raw)+4)
		normalized = append(normalized, prefix...)
		normalized = append(normalized, input.Raw...)
		normalized = append(normalized, '}', ']', '}', ']')
	case input.IsArray():
		normalized = []byte(input.Raw)
	case input.Exists() && input.Type != gjson.Null:
		return fail(400, "input must be a string or array", "invalid_request", "input")
	}
	previous := gjson.GetBytes(raw, "previous_response_id")
	if previous.Exists() && previous.Type != gjson.Null && previous.Type != gjson.String {
		return fail(400, "previous_response_id must be a string", "invalid_request", "previous_response_id")
	}
	state := &responsesHTTPExecution{handler: h, scope: responsesHTTPCallerScope(c), previousID: strings.TrimSpace(previous.String()), model: gjson.GetBytes(raw, "model").String(), store: gjson.GetBytes(raw, "store").Type != gjson.False}
	if state.previousID != "" {
		saved, ok := h.httpContinuations.load(state.scope, state.previousID)
		if !ok {
			return fail(400, "Previous response was not found", "previous_response_not_found", "previous_response_id")
		}
		if !gjson.GetBytes(raw, "model").Exists() {
			state.model = saved.Model
			raw, _ = sjson.SetBytes(raw, "model", saved.Model)
		}
		merged, err := mergeResponsesInputArrays(util.ParseGJSONBytesNoCopy(saved.Input), saved.Output, string(normalized))
		if err != nil {
			return fail(400, "Invalid continuation input", "invalid_request", "input")
		}
		normalized = merged
		state.pinnedID = saved.AuthID
		raw, _ = sjson.SetRawBytes(raw, "input", normalized)
		raw, _ = sjson.DeleteBytes(raw, "previous_response_id")
		if limit := c.GetInt64(handlers.MaxRequestBodyBytesContextKey); limit > 0 && int64(len(raw)) > limit {
			return fail(http.StatusRequestEntityTooLarge, "Expanded request body exceeds configured limit", "request_too_large", "input")
		}
	}
	state.input = normalized
	c.Set(responsesHTTPContextKey, state)
	return raw, true
}

func responsesHTTPState(c *gin.Context) *responsesHTTPExecution {
	value, _ := c.Get(responsesHTTPContextKey)
	state, _ := value.(*responsesHTTPExecution)
	return state
}

func (s *responsesHTTPExecution) executionContext(ctx context.Context) context.Context {
	if s == nil {
		return ctx
	}
	ctx = handlers.WithPinnedAuthID(ctx, s.pinnedID)
	return handlers.WithSelectedAuthIDCallback(ctx, func(id string) { s.mu.Lock(); s.authID = id; s.mu.Unlock() })
}

// response is called on repaired terminal output before it becomes observable to
// the client, so an immediately issued next HTTP request sees the saved turn.
func (s *responsesHTTPExecution) response(raw []byte, completed bool) []byte {
	if s == nil || !json.Valid(raw) || !util.ParseGJSONBytesNoCopy(raw).IsObject() || gjson.GetBytes(raw, "id").String() == "" {
		return raw
	}
	retained := s.store
	if completed {
		retained = false
		if s.store && gjson.GetBytes(raw, "status").String() == "completed" && gjson.GetBytes(raw, "error").Type == gjson.Null {
			s.mu.Lock()
			authID := s.authID
			s.mu.Unlock()
			output := util.GetGJSONBytesNoCopy(raw, "output")
			if output.IsArray() {
				retained = s.handler.httpContinuations.save(s.scope, gjson.GetBytes(raw, "id").String(), responsesHTTPContinuation{Input: s.input, Output: []byte(output.Raw), AuthID: authID, Model: s.model})
			}
		}
	}
	raw, _ = sjson.SetBytes(raw, "store", retained)
	if s.previousID != "" {
		raw, _ = sjson.SetBytes(raw, "previous_response_id", s.previousID)
	} else {
		raw, _ = sjson.SetBytes(raw, "previous_response_id", nil)
	}
	return raw
}

func (s *responsesHTTPExecution) streamPayload(payload []byte, event string) []byte {
	if s == nil {
		return payload
	}
	response := util.GetGJSONBytesNoCopy(payload, "response")
	if !response.IsObject() {
		return payload
	}
	completed := event == "response.completed"
	repaired := s.response([]byte(response.Raw), completed)
	if event == "response.failed" || event == "response.incomplete" {
		repaired, _ = sjson.SetBytes(repaired, "store", false)
	}
	payload, _ = sjson.SetRawBytes(payload, "response", repaired)
	return payload
}
