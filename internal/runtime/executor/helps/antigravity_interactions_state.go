package helps

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

var antigravityInteractionsSessions = cache.NewInteractionsSessionCache()

// AntigravityInteractionsState tracks a single request, including streamed steps.
// The cache namespace is computed before continuation rewriting and payload rules.
type AntigravityInteractionsState struct {
	key         string
	id          string
	environment string
	calls       []string
}

func interactionsDigest(value any) string {
	encoded, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// PrepareAntigravityInteractions restores only a matching tool-result suffix.
// Explicit continuation always wins, including an explicitly empty field.
func PrepareAntigravityInteractions(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, model string, body []byte) (*AntigravityInteractionsState, []byte) {
	doc := ojson.NewDoc(body)
	state, mutated := prepareAntigravityInteractionsDoc(auth, req, opts, model, doc)
	if !mutated {
		return state, body
	}
	return state, doc.Bytes()
}

// PrepareAntigravityInteractionsDoc restores only a matching tool-result suffix in place on doc.
func PrepareAntigravityInteractionsDoc(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, model string, doc *ojson.Doc) *AntigravityInteractionsState {
	state, _ := prepareAntigravityInteractionsDoc(auth, req, opts, model, doc)
	return state
}

func prepareAntigravityInteractionsDoc(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, model string, doc *ojson.Doc) (*AntigravityInteractionsState, bool) {
	state := &AntigravityInteractionsState{}
	if doc == nil || doc.Len() == 0 || !strings.HasPrefix(strings.ToLower(model), "antigravity") || auth == nil {
		return state, false
	}
	caller, _ := opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
	if caller == "" {
		caller, _ = req.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
	}
	if caller == "" {
		caller = opts.Headers.Get("Authorization")
	}
	original := opts.OriginalRequest
	if len(original) == 0 {
		original = req.Payload
	}
	identity := ""
	if info, ok := session.ExtractSessionInfo(opts.Headers, original, opts.Metadata); ok {
		identity = info.SessionID
	}
	input := doc.Get("input").Array()
	if identity == "" {
		lastUser := -1
		for i, step := range input {
			if step.Get("type").String() == "user_input" {
				lastUser = i
			}
		}
		if lastUser < 0 {
			return state, false
		}
		// Decode JSON objects before hashing, so whitespace and key order do not
		// split a conversation. Tool rounds after the last user are excluded.
		prefix := make([]json.RawMessage, 0, lastUser+1)
		for _, step := range input[:lastUser+1] {
			prefix = append(prefix, json.RawMessage(step.Raw))
		}
		raw, errMarshal := json.Marshal(map[string]any{"input": prefix, "system_instruction": doc.Get("system_instruction").Value()})
		if errMarshal != nil {
			return state, false
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var canonical any
		if errDecode := decoder.Decode(&canonical); errDecode != nil {
			return state, false
		}
		identity = interactionsDigest(canonical)
	}
	state.key = interactionsDigest([]string{caller, auth.ID, auth.Provider, auth.Attributes["api_key"], auth.Attributes["base_url"], model, identity})
	if doc.Get("previous_interaction_id").Exists() {
		return state, false
	}
	start := len(input)
	var calls []string
	for start > 0 && input[start-1].Get("type").String() == "function_result" {
		start--
		calls = append(calls, input[start].Get("call_id").String())
	}
	callKey := cache.InteractionsCallKey(calls)
	if callKey == "" {
		return state, false
	}
	cached, ok := antigravityInteractionsSessions.Get(state.key + ":" + callKey)
	if !ok {
		return state, false
	}
	_ = doc.Set("previous_interaction_id", cached.ID)
	if !doc.Get("environment_id").Exists() && cached.Environment != "" {
		_ = doc.Set("environment_id", cached.Environment)
	}
	results := make([]json.RawMessage, 0, len(input)-start)
	for _, step := range input[start:] {
		results = append(results, json.RawMessage(step.Raw))
	}
	_ = doc.Set("input", results)
	return state, true
}

// Observe records only completed requires_action interactions, never failed or
// truncated streams. Stream events may carry steps outside the final snapshot.
func (s *AntigravityInteractionsState) Observe(payload []byte) {
	if s == nil || s.key == "" {
		return
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	event := root.Get("event_type").String()
	interaction := root
	if event != "" {
		interaction = root.Get("interaction")
	}
	if id := interaction.Get("id").String(); id != "" {
		s.id = strings.Clone(id)
	}
	if environment := interaction.Get("environment_id").String(); environment != "" {
		s.environment = strings.Clone(environment)
	}
	if event == "step.start" && root.Get("step.type").String() == "function_call" {
		s.calls = append(s.calls, strings.Clone(root.Get("step.id").String()))
	}
	if event != "" && event != "interaction.completed" {
		return
	}
	if interaction.Get("status").String() != "requires_action" || s.id == "" {
		return
	}
	if steps := interaction.Get("steps"); steps.IsArray() {
		s.calls = nil
		for _, step := range steps.Array() {
			if step.Get("type").String() == "function_call" {
				s.calls = append(s.calls, strings.Clone(step.Get("id").String()))
			}
		}
	}
	callKey := cache.InteractionsCallKey(s.calls)
	if callKey == "" {
		return
	}
	antigravityInteractionsSessions.Put(s.key+":"+callKey, cache.InteractionsContinuation{ID: s.id, Environment: s.environment})
}
