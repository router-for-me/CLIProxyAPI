package helps

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type antigravityAgentSession struct {
	cascade, trajectory string
	mu                  sync.Mutex
	lastMillis          int64
}

type antigravityAgentSessionKey struct {
	authID, sessionID, homeScope string
	epoch                        uint64
}

var antigravityAgentSessions = cache.NewBoundedLRU[antigravityAgentSessionKey, *antigravityAgentSession](4096, nil)

// ApplyAntigravityAgentEnvelope correlates request and label IDs within the active credential-scoped session.
func ApplyAntigravityAgentEnvelope(doc *ojson.Doc, auth *cliproxyauth.Auth, sessionID, modelName, modelEnum string) {
	key := antigravityAgentSessionKey{sessionID: sessionID}
	if auth != nil {
		key.authID = auth.ID
		key.homeScope = auth.Attributes[cliproxyauth.AntigravityModelEnumHomeScopeAttribute]
		if key.homeScope == "" {
			key.epoch = auth.RegistrationEpoch
		}
	}
	session := antigravityAgentSessions.GetOrAdd(key, func() *antigravityAgentSession {
		return &antigravityAgentSession{cascade: uuid.NewString(), trajectory: uuid.NewString()}
	})
	// Distinguish parallel requests for the same history even within one millisecond.
	session.mu.Lock()
	stamp := max(time.Now().UnixMilli(), session.lastMillis+1)
	session.lastMillis = stamp
	session.mu.Unlock()

	turns, results := antigravityAgentProgress(doc.Get("request.contents"))
	lastStep := turns + results
	_ = doc.Set("requestId", fmt.Sprintf("agent/%s/%d/%s/%d", session.cascade, stamp, session.trajectory, lastStep+1))
	_ = doc.Set("request.labels.cascade_id", session.cascade)
	_ = doc.Set("request.labels.root_cascade_id", session.cascade)
	_ = doc.Set("request.labels.trajectory_id", session.trajectory)
	_ = doc.Set("request.labels.request_id", session.trajectory+"-"+strconv.Itoa(turns))
	_ = doc.Set("request.labels.last_step_index", strconv.Itoa(lastStep))
	if modelEnum != "" {
		_ = doc.Set("request.labels.model_enum", modelEnum)
	} else {
		_ = doc.Delete("request.labels.model_enum")
	}
	model := strings.ToLower(modelName)
	_ = doc.Set("request.labels.used_claude", strconv.FormatBool(strings.Contains(model, "claude")))
	_ = doc.Set("request.labels.used_claude_conservative", "false")
	_ = doc.Set("request.labels.used_non_gemini_model", strconv.FormatBool(!strings.Contains(model, "gemini")))
}

func antigravityAgentProgress(contents ojson.Result) (turns, results int) {
	contents.ForEach(func(_, content ojson.Result) bool {
		isModel := content.Get("role").Str == "model"
		generated := false
		content.Get("parts").ForEach(func(_, part ojson.Result) bool {
			// Native tool results can carry the model role; they are steps, not additional model calls.
			if part.Get("functionResponse").Exists() || part.Get("function_response").Exists() {
				results++
			} else if isModel {
				generated = true
			}
			return true
		})
		if generated {
			turns++
		}
		return true
	})
	return turns, results
}
