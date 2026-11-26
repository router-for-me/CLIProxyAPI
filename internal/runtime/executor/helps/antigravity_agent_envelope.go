package helps

import (
	"crypto/sha256"
	"encoding/hex"
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
	authID, sessionID, homeScope, grantScope string
	epoch                                    uint64
}

var antigravityAgentSessions = cache.NewBoundedLRU[antigravityAgentSessionKey, *antigravityAgentSession](4096, nil)

// ApplyAntigravityAgentEnvelope correlates request and label IDs within the active credential-scoped session.
func ApplyAntigravityAgentEnvelope(doc *ojson.Doc, auth *cliproxyauth.Auth, sessionID, modelName, modelEnum string) {
	key := antigravityAgentSessionKey{sessionID: sessionID}
	if auth != nil {
		key.authID = auth.ID
		key.homeScope = strings.TrimSpace(auth.Attributes[cliproxyauth.AntigravityModelEnumHomeScopeAttribute])
		if key.homeScope == "" {
			key.epoch = auth.RegistrationEpoch
			if key.epoch == 0 {
				key.grantScope = antigravityAgentGrantScope(auth)
			}
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
	if strings.TrimSpace(doc.Get("requestId").String()) == "" {
		_ = doc.Set("requestId", fmt.Sprintf("agent/%s/%d/%s/%d", session.cascade, stamp, session.trajectory, lastStep+1))
	}
	setIfEmptyString := func(path, value string) {
		if strings.TrimSpace(doc.Get(path).String()) == "" {
			_ = doc.Set(path, value)
		}
	}
	setIfEmptyString("request.labels.cascade_id", session.cascade)
	setIfEmptyString("request.labels.root_cascade_id", session.cascade)
	setIfEmptyString("request.labels.trajectory_id", session.trajectory)
	setIfEmptyString("request.labels.request_id", session.trajectory+"-"+strconv.Itoa(turns))
	setIfEmptyString("request.labels.last_step_index", strconv.Itoa(lastStep))
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

// antigravityAgentGrantScope derives a hashed, refresh-stable credential scope
// for Home-mode auths (RegistrationEpoch == 0) that lack antigravity_model_enum_scope.
func antigravityAgentGrantScope(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	refreshToken := antigravityAuthMetaString(auth, "refresh_token")
	if refreshToken == "" {
		refreshToken = antigravityAuthMetaString(auth, "refreshToken")
	}
	projectID := antigravityAuthMetaString(auth, "project_id")
	email := antigravityAuthMetaString(auth, "email")
	if email == "" {
		email = antigravityAuthAttrString(auth, "email")
	}
	email = strings.ToLower(email)
	accountID := antigravityAuthMetaString(auth, "account_id")
	if accountID == "" {
		accountID = antigravityAuthAttrString(auth, "account_id")
	}
	accountID = strings.ToLower(accountID)
	// Require an account-level identifier before omitting ephemeral tokens;
	// project_id alone can be shared across distinct Google accounts.
	if refreshToken != "" || email != "" || accountID != "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("stable:%q/%q/%q/%q", refreshToken, projectID, email, accountID)))
		return "stable:" + hex.EncodeToString(sum[:])
	}
	accessToken := antigravityAuthMetaString(auth, "access_token")
	if accessToken == "" {
		accessToken = antigravityAuthMetaString(auth, "accessToken")
	}
	apiKey := antigravityAuthAttrString(auth, cliproxyauth.AttributeAPIKey)
	if apiKey == "" {
		apiKey = antigravityAuthMetaString(auth, "api_key")
	}
	if accessToken != "" || apiKey != "" || projectID != "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("ephemeral:%q/%q/%q", projectID, accessToken, apiKey)))
		return "ephemeral:" + hex.EncodeToString(sum[:])
	}
	return ""
}

func antigravityAuthMetaString(auth *cliproxyauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}

func antigravityAuthAttrString(auth *cliproxyauth.Auth, key string) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[key])
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
