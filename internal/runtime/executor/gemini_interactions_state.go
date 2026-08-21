package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// goframeCacheAntigravityInteractionsState records the upstream
// interaction_id + environment_id observed on an antigravity Agents
// (Interactions API) stream, keyed by conversation session. It is a
// best-effort, out-of-band capture: it never blocks the stream read loop and
// never rewrites the response.
//
// Only antigravity model names are considered. The reasoning-replay cache used
// by the antigravity OAuth / Code Assist path is never touched (distinct KV
// prefix in internal/cache).
func goframeCacheAntigravityInteractionsState(ctx context.Context, modelName string, originalRequest []byte, opts cliproxyexecutor.Options, payload []byte) {
	modelName = strings.TrimSpace(modelName)
	if !strings.Contains(strings.ToLower(modelName), "antigravity") {
		return
	}
	interactionID := strings.TrimSpace(firstNonEmptyString(
		gjson.GetBytes(payload, "interaction.id").String(),
		gjson.GetBytes(payload, "id").String(),
	))
	envID := strings.TrimSpace(firstNonEmptyString(
		gjson.GetBytes(payload, "interaction.environment_id").String(),
		gjson.GetBytes(payload, "environment_id").String(),
		gjson.GetBytes(payload, "interaction.environment.id").String(),
		gjson.GetBytes(payload, "environment.id").String(),
	))
	// Streamed tool calls arrive as step.start frames carrying step.id +
	// step.name but no interaction coordinates. Capture those names even when
	// this frame has none — a later Responses-style continuation (call_id +
	// output only) needs them to fill function_result.name.
	stepNames := map[string]string{}
	if step := gjson.GetBytes(payload, "step"); step.Exists() {
		if strings.EqualFold(strings.TrimSpace(step.Get("type").String()), "function_call") {
			callID := strings.TrimSpace(step.Get("id").String())
			name := strings.TrimSpace(step.Get("name").String())
			if callID != "" && name != "" {
				stepNames[callID] = name
			}
		}
	}
	if interactionID == "" && envID == "" && len(stepNames) == 0 {
		return
	}
	sessionKey := antigravityExecSessionKey(opts, originalRequest)
	if sessionKey == "" {
		log.Debugf("antigravity interactions state: no session key for model=%s", modelName)
		return
	}
	// Merge with any previously cached coordinates: stream events may carry
	// only one of the two (e.g. a completion event repeating just the ID), and
	// overwriting the whole entry would blank the missing coordinate, leaving
	// the next tool-result turn unable to resume.
	state := internalcache.AntigravityInteractionsState{
		InteractionID: interactionID,
		EnvironmentID: envID,
	}
	if existing, ok := internalcache.GetAntigravityInteractionsState(modelName, sessionKey); ok {
		if state.InteractionID == "" {
			state.InteractionID = existing.InteractionID
		}
		if state.EnvironmentID == "" {
			state.EnvironmentID = existing.EnvironmentID
		}
		state.ToolNames = existing.ToolNames
	}
	// Record call_id -> function name from any function_call steps in this
	// payload so a later Responses-style tool-result turn (call_id + output
	// only) can still be given the required function_result.name. Non-stream
	// responses may nest the array under "interaction.steps".
	names := map[string]string{}
	for k, v := range state.ToolNames {
		names[k] = v
	}
	for k, v := range stepNames {
		names[k] = v
	}
	stepsArrays := [][]gjson.Result{}
	for _, path := range []string{"steps", "interaction.steps"} {
		if arr := gjson.GetBytes(payload, path); arr.IsArray() {
			stepsArrays = append(stepsArrays, []gjson.Result{})
			arr.ForEach(func(_, step gjson.Result) bool {
				stepsArrays[len(stepsArrays)-1] = append(stepsArrays[len(stepsArrays)-1], step)
				return true
			})
		}
	}
	for _, steps := range stepsArrays {
		for _, step := range steps {
			if !strings.EqualFold(strings.TrimSpace(step.Get("type").String()), "function_call") {
				continue
			}
			callID := strings.TrimSpace(step.Get("id").String())
			name := strings.TrimSpace(step.Get("name").String())
			if callID != "" && name != "" {
				names[callID] = name
			}
		}
	}
	if len(names) > 0 {
		state.ToolNames = names
	}
	internalcache.CacheAntigravityInteractionsStateBestEffort(ctx, modelName, sessionKey, state)
	log.Debugf("antigravity interactions state: cached model=%s session=%s interaction=%s env=%s", modelName, sessionKey, state.InteractionID, state.EnvironmentID)
}

// antigravityConversationPrefixKey derives a continuity key from an OpenAI
// chat/completions body that carries no explicit session identifier. It hashes
// the stable conversation prefix: every message up to and including the LAST
// user message. The initial tool-calling turn ([system, user]) and its
// tool-result continuation ([system, user, assistant(tool_calls), tool])
// share this prefix, so both map to the same key while a new user turn starts
// a fresh one.
func antigravityConversationPrefixKey(raw []byte) string {
	msgs := gjson.GetBytes(raw, "messages")
	if !msgs.IsArray() {
		return ""
	}
	// Canonical JSON per message (sorted keys, no whitespace) so the hash is
	// stable across clients that re-serialize the history differently.
	canonical := func(msg gjson.Result) string {
		v := msg.Value()
		b, err := json.Marshal(v)
		if err != nil {
			return msg.Raw
		}
		return string(b)
	}
	var prefix []string
	lastUser := -1
	msgs.ForEach(func(_, msg gjson.Result) bool {
		prefix = append(prefix, canonical(msg))
		if strings.EqualFold(strings.TrimSpace(msg.Get("role").String()), "user") {
			lastUser = len(prefix) - 1
		}
		return true
	})
	if lastUser < 0 {
		return ""
	}
	hasher := sha256.New()
	for i := 0; i <= lastUser; i++ {
		hasher.Write([]byte(prefix[i]))
		hasher.Write([]byte{0})
	}
	return "chat:" + hex.EncodeToString(hasher.Sum(nil))[:16]
}

// antigravityExecSessionKey derives a stable continuity key for the agent
// conversation. It mirrors the reasoning-replay scope derivation but stays
// independent so the interactions-agent cache never collides with the OAuth /
// Code Assist replay cache.
func antigravityExecSessionKey(opts cliproxyexecutor.Options, originalRequest []byte) string {
	if value := strings.TrimSpace(opts.Headers.Get("Session-Id")); value != "" {
		return "responses:" + value
	}
	for _, raw := range [][]byte{opts.OriginalRequest, originalRequest} {
		if len(raw) == 0 {
			continue
		}
		for _, path := range []string{"session_id", "metadata.session_id"} {
			if value := strings.TrimSpace(gjson.GetBytes(raw, path).String()); value != "" {
				return "responses:" + value
			}
		}
	}
	if value := metadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return "execution:" + value
	}
	// Context-derived identity (conductor runs session.Enrich before exec):
	// stable per downstream caller+conversation even when the client sends no
	// explicit marker. This isolates identical conversation prefixes coming
	// from different callers so they never share one upstream interaction.
	if value := helps.DerivedSessionID(opts.Metadata); value != "" {
		return "derived:" + value
	}
	// Fallback for chat/completions clients (e.g. Hermes delegation) that send
	// no explicit session marker: derive a stable key from the conversation
	// prefix. Antigravity function calling is stateful-only upstream, so
	// without this key the proxy could never attach previous_interaction_id to
	// the tool-result turn.
	for _, raw := range [][]byte{opts.OriginalRequest, originalRequest} {
		if len(raw) == 0 {
			continue
		}
		if key := antigravityConversationPrefixKey(raw); key != "" {
			return key
		}
	}
	return ""
}

// firstNonEmptyString returns the first non-empty string argument.
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// originalRequestRawJSON resolves the client-facing request body the same way
// the executor does elsewhere (opts.OriginalRequest falls back to Payload).
func originalRequestRawJSON(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	if len(opts.OriginalRequest) > 0 {
		return opts.OriginalRequest
	}
	return req.Payload
}

// goframeApplyAntigravityInteractionsContinuation rewrites a translated
// interactions body so a tool-calling continuation turn resumes the upstream
// antigravity agent interaction instead of replaying the full assistant
// tool-call history.
//
// When the incoming conversation carries a function_result (i.e. the client is
// responding to a prior function_call) and we have a cached continuation state
// for this session, this function:
//   - attaches previous_interaction_id + environment_id from the cache, and
//   - keeps ONLY the function_result step(s) in input (dropping the replayed
//     user_input / model_output / function_call history that Google rejects).
//
// When no continuation state is present, the body is returned unchanged.
func goframeApplyAntigravityInteractionsContinuation(ctx context.Context, modelName string, originalRequest []byte, opts cliproxyexecutor.Options, body []byte) []byte {
	modelName = strings.TrimSpace(modelName)
	if !strings.Contains(strings.ToLower(modelName), "antigravity") {
		return body
	}
	// Only act when this turn actually carries a tool result to feed back.
	hasToolResult := false
	root := gjson.ParseBytes(body)
	if root.Get("input").IsArray() {
		root.Get("input").ForEach(func(_, item gjson.Result) bool {
			if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "function_result") {
				hasToolResult = true
				return false
			}
			return true
		})
	}
	if !hasToolResult {
		return body
	}
	sessionKey := antigravityExecSessionKey(opts, originalRequest)
	if sessionKey == "" {
		log.Debugf("antigravity continuation: no session key model=%s", modelName)
		return body
	}
	state, ok := internalcache.GetAntigravityInteractionsState(modelName, sessionKey)
	if !ok {
		log.Debugf("antigravity continuation: no cached state model=%s session=%s", modelName, sessionKey)
		return body
	}
	log.Debugf("antigravity continuation: resuming interaction=%s env=%s session=%s", state.InteractionID, state.EnvironmentID, sessionKey)

	// Rebuild the body keeping only function_result input items so the
	// continuation is a clean "turn" on the prior interaction.
	//
	// Chat Completions tool loops send role:"tool" messages with only
	// tool_call_id + content, so their function_result steps may lack "name"
	// — but the Interactions function-result schema requires it. Collect the
	// call_id -> name mapping from the replayed assistant function_call steps
	// first, then fill any missing names before dropping the history.
	namesByCallID := map[string]string{}
	if root.Get("input").IsArray() {
		root.Get("input").ForEach(func(_, item gjson.Result) bool {
			if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "function_call") {
				callID := strings.TrimSpace(item.Get("call_id").String())
				name := strings.TrimSpace(item.Get("name").String())
				if callID != "" && name != "" {
					namesByCallID[callID] = name
				}
			}
			return true
		})
	}
	kept := make([][]byte, 0, 1)
	if root.Get("input").IsArray() {
		root.Get("input").ForEach(func(_, item gjson.Result) bool {
			if !strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "function_result") {
				return true
			}
			result := []byte(item.Raw)
			if strings.TrimSpace(item.Get("name").String()) == "" {
				callID := strings.TrimSpace(item.Get("call_id").String())
				name := namesByCallID[callID]
				if name == "" {
					// Responses tool loops do not replay the function_call,
					// so fall back to the cached call_id -> name mapping
					// captured from the prior upstream interaction.
					name = state.ToolNames[callID]
				}
				if name != "" {
					result, _ = sjson.SetBytes(result, "name", name)
				}
			}
			kept = append(kept, result)
			return true
		})
	}
	out := []byte(`{"input":[]}`)
	out, _ = sjson.SetBytes(out, "agent", modelName)
	if state.InteractionID != "" {
		out, _ = sjson.SetBytes(out, "previous_interaction_id", state.InteractionID)
	}
	// The continuation requires the field literally named "environment"
	// (carrying the environment_id captured from the prior interaction).
	// Google returns 400 "Missing required field 'environment'" if it is
	// sent as "environment_id" instead.
	if state.EnvironmentID != "" {
		out, _ = sjson.SetBytes(out, "environment", state.EnvironmentID)
	}
	// Preserve stream / payload config the translator may have set.
	if v := root.Get("stream"); v.Exists() {
		out, _ = sjson.SetBytes(out, "stream", v.Bool())
	}
	out, _ = sjson.SetRawBytes(out, "input", translatorcommon.JoinRawArray(kept))
	log.Debugf("antigravity continuation: final upstream body %s", string(out))
	return out
}