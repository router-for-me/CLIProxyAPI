package executor

import (
	"context"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
	if interactionID == "" && envID == "" {
		return
	}
	sessionKey := antigravityExecSessionKey(opts, originalRequest)
	if sessionKey == "" {
		return
	}
	internalcache.CacheAntigravityInteractionsStateBestEffort(ctx, modelName, sessionKey, internalcache.AntigravityInteractionsState{
		InteractionID: interactionID,
		EnvironmentID: envID,
	})
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
		return body
	}
	state, ok := internalcache.GetAntigravityInteractionsState(modelName, sessionKey)
	if !ok {
		return body
	}

	// Rebuild the body keeping only function_result input items so the
	// continuation is a clean "turn" on the prior interaction.
	kept := make([][]byte, 0, 1)
	if root.Get("input").IsArray() {
		root.Get("input").ForEach(func(_, item gjson.Result) bool {
			if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "function_result") {
				kept = append(kept, []byte(item.Raw))
			}
			return true
		})
	}
	out := []byte(`{"input":[]}`)
	out, _ = sjson.SetBytes(out, "agent", modelName)
	if state.InteractionID != "" {
		out, _ = sjson.SetBytes(out, "previous_interaction_id", state.InteractionID)
	}
	if state.EnvironmentID != "" {
		out, _ = sjson.SetBytes(out, "environment_id", state.EnvironmentID)
	}
	// Preserve stream / payload config the translator may have set.
	if v := root.Get("stream"); v.Exists() {
		out, _ = sjson.SetBytes(out, "stream", v.Bool())
	}
	out, _ = sjson.SetRawBytes(out, "input", translatorcommon.JoinRawArray(kept))
	return out
}