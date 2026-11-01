package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Muse returns reasoning as an opaque encrypted_content envelope, and Meta binds
// each envelope to the account that issued it: replaying one through any other
// account is rejected with 400 "reasoning encrypted_content was not issued to
// this caller". A client such as Claude Code resends the whole history on every
// turn, and that history mixes envelopes from every pool account that served an
// earlier turn, so forwarding it verbatim fails in production even though a
// single-account test passes.
//
// The feature therefore carries provenance end to end:
//
//   - Responses leaving this executor have each reasoning envelope wrapped as
//     meta#<account-key>#<envelope> (see signature.TagMetaReasoning), whether
//     the client reads it as an Anthropic thinking signature or as a Responses
//     encrypted_content, streaming or not.
//   - Requests entering this executor keep an envelope only when its tag names
//     the credential selected for this very request, and strip the tag first.
//     Everything else (another account's tag, an untagged legacy envelope) is
//     dropped. With the feature off, untagged envelopes are forwarded as is.
//   - If Meta still rejects a replayed envelope, the request is retried once
//     with every envelope removed, so the caller never sees that 400.

// metaReasoningNotIssuedMessage is the stable fragment of the 400 Meta returns for
// an envelope replayed through an account that did not issue it.
const metaReasoningNotIssuedMessage = "not issued to this caller"

// metaReasoningReplayStats counts what happened to the reasoning envelopes of
// one request. The executor logs it so the replay hit rate can be measured.
type metaReasoningReplayStats struct {
	kept            int
	droppedForeign  int
	droppedUntagged int
}

// metaReasoningReplay is the per-request replay policy.
type metaReasoningReplay struct {
	// enabled is true when the feature is on for this model and the selected
	// credential can be identified.
	enabled bool
	// accountKey names the credential selected for this request.
	accountKey string
	// baseURL is the selected credential's upstream, which decides how strictly
	// the native sanitizer gates Meta envelopes.
	baseURL string
	stats   *metaReasoningReplayStats
}

func (e *MetaExecutor) newMetaReasoningReplay(opts cliproxyexecutor.Options, requestModel, baseModel string, auth *cliproxyauth.Auth) metaReasoningReplay {
	replay := metaReasoningReplay{stats: &metaReasoningReplayStats{}}
	replay.baseURL, _ = metaCreds(auth)
	if e == nil || e.cfg == nil {
		return replay
	}
	requested := helps.PayloadRequestedModel(opts, requestModel)
	if !e.cfg.Meta.ReasoningReplay.AppliesToModel(requested, requestModel, baseModel) {
		return replay
	}
	replay.accountKey = metaReasoningAccountKey(auth)
	replay.enabled = replay.accountKey != ""
	return replay
}

// metaReasoningAccountKey derives a stable, pseudonymous key for the credential.
// It has to survive token re-minting and restarts, so it is derived from the
// credential's identity (auth.ID), never from the short-lived API key. A
// credential without an ID gets no key, which leaves replay off for it.
func metaReasoningAccountKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	id := strings.TrimSpace(auth.ID)
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("cliproxy/meta-reasoning-account/v1\x00" + id))
	return hex.EncodeToString(sum[:8])
}

// metaReasoningReplayDecision is what the account policy decides for one
// reasoning item's encrypted_content.
type metaReasoningReplayDecision struct {
	// replay is the encrypted_content to forward. Used only when drop is false.
	replay string
	// drop removes the encrypted_content; reason explains why.
	drop   bool
	reason string
}

// decide applies the account policy to one string encrypted_content.
func (p metaReasoningReplay) decide(raw string) metaReasoningReplayDecision {
	accountKey, envelope, tagged := signature.ParseMetaReasoningTag(raw)
	if !p.enabled {
		if tagged {
			// The feature was on earlier and is off now: nothing may reach Meta
			// that did not reach it before the feature existed.
			p.stats.droppedForeign++
			return metaReasoningReplayDecision{drop: true, reason: "Muse reasoning replay is disabled"}
		}
		// Without provenance, an untagged Muse envelope is forwarded as is (upstream
		// behaviour, still subject to the native-only shape check). It may belong to
		// another pool account, so it counts as kept: if Meta rejects it, the request
		// is retried once without reasoning.
		if signature.IsValidMetaReasoningSignature(raw) {
			p.stats.kept++
		}
		return metaReasoningReplayDecision{replay: raw}
	}
	switch {
	case !tagged:
		p.stats.droppedUntagged++
		return metaReasoningReplayDecision{drop: true, reason: "untagged Muse reasoning envelope has no provable account"}
	case p.accountKey == "" || accountKey != p.accountKey:
		p.stats.droppedForeign++
		return metaReasoningReplayDecision{drop: true, reason: "Muse reasoning envelope was issued by another account"}
	default:
		p.stats.kept++
		return metaReasoningReplayDecision{replay: envelope}
	}
}

// sanitizeRequest applies the account policy to a Responses request body and
// then the native-only Meta sanitizer, which still strips anything that is not
// a recognized Meta envelope (fail-closed). A reasoning item that loses its
// envelope and has no summary or content of its own is removed entirely.
func (p metaReasoningReplay) sanitizeRequest(ctx context.Context, body []byte) []byte {
	body = p.applyAccountPolicy(ctx, body)
	return sanitizeOpenAIResponsesReasoningEncryptedContentForMetaBaseURL(ctx, "meta executor", body, p.baseURL)
}

func (p metaReasoningReplay) applyAccountPolicy(ctx context.Context, body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	rebuilt := make([]string, 0, len(items))
	changed := false
	for index, item := range items {
		encrypted := item.Get("encrypted_content")
		if item.Get("type").String() != "reasoning" || encrypted.Type != gjson.String {
			rebuilt = append(rebuilt, item.Raw)
			continue
		}
		decision := p.decide(encrypted.String())
		if !decision.drop {
			if decision.replay == encrypted.String() {
				rebuilt = append(rebuilt, item.Raw)
				continue
			}
			next, errSet := sjson.Set(item.Raw, "encrypted_content", decision.replay)
			if errSet == nil {
				rebuilt = append(rebuilt, next)
				changed = true
				continue
			}
			// Forwarding the tagged value would send Meta a string it never issued,
			// so fall back to dropping it like any other unusable envelope.
			decision = metaReasoningReplayDecision{drop: true, reason: "failed to strip provenance tag: " + errSet.Error()}
		}
		changed = true
		helps.LogWithRequestID(ctx).Debugf("meta executor: dropped reasoning encrypted_content at input[%d] reason=%s", index, decision.reason)
		if metaReasoningItemIsEmpty(item) {
			continue
		}
		next, errDelete := sjson.Delete(item.Raw, "encrypted_content")
		if errDelete != nil {
			continue
		}
		rebuilt = append(rebuilt, next)
	}
	if !changed {
		return body
	}
	out, errSet := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if errSet != nil {
		helps.LogWithRequestID(ctx).Debugf("meta executor: failed to rebuild input after reasoning replay policy: %v", errSet)
		return body
	}
	return out
}

// metaReasoningItemIsEmpty reports whether a reasoning item carries no summary
// and no content, so that nothing is left of it once encrypted_content is gone.
func metaReasoningItemIsEmpty(item gjson.Result) bool {
	if !openaiResponsesReasoningSummaryIsEmpty(item.Get("summary")) {
		return false
	}
	content := item.Get("content")
	return !content.Exists() || content.Type == gjson.Null || (content.IsArray() && len(content.Array()) == 0)
}

// withoutReplay is the policy for the single retry: it is enabled but names no
// account, so every envelope is dropped.
func (p metaReasoningReplay) withoutReplay() metaReasoningReplay {
	return metaReasoningReplay{enabled: true, baseURL: p.baseURL, stats: &metaReasoningReplayStats{}}
}

// shouldTag reports whether responses must carry the provenance tag.
func (p metaReasoningReplay) shouldTag() bool {
	return p.enabled && p.accountKey != ""
}

// tagResponseEvent wraps the reasoning envelopes in one upstream Responses event
// (or a bare non-streaming response object) with this account's provenance tag.
func (p metaReasoningReplay) tagResponseEvent(eventData []byte) []byte {
	if !p.shouldTag() || len(eventData) == 0 {
		return eventData
	}
	switch gjson.GetBytes(eventData, "type").String() {
	case "response.output_item.added", "response.output_item.done":
		return p.tagReasoningItem(eventData, "item")
	case "response.completed", "response.incomplete", "response.failed":
		return p.tagOutputItems(eventData, "response.output")
	case "":
		return p.tagOutputItems(eventData, "output")
	}
	return eventData
}

func (p metaReasoningReplay) tagOutputItems(data []byte, outputPath string) []byte {
	output := gjson.GetBytes(data, outputPath)
	if !output.IsArray() {
		return data
	}
	for index := range output.Array() {
		data = p.tagReasoningItem(data, outputPath+"."+strconv.Itoa(index))
	}
	return data
}

func (p metaReasoningReplay) tagReasoningItem(data []byte, itemPath string) []byte {
	item := gjson.GetBytes(data, itemPath)
	if item.Get("type").String() != "reasoning" {
		return data
	}
	encrypted := item.Get("encrypted_content")
	if encrypted.Type != gjson.String || encrypted.String() == "" {
		return data
	}
	tagged := signature.TagMetaReasoning(p.accountKey, encrypted.String())
	if tagged == encrypted.String() {
		return data
	}
	out, errSet := sjson.SetBytes(data, itemPath+".encrypted_content", tagged)
	if errSet != nil {
		// An untagged envelope is dropped on replay, so failing to tag only costs the
		// replay; it must never fail the response.
		return data
	}
	return out
}

// recordRequest logs what happened to the reasoning envelopes of one request, so the
// replay hit rate (kept / (kept + dropped_foreign)) can be measured from the logs.
func (p metaReasoningReplay) recordRequest(ctx context.Context, model, authID string) {
	if p.stats == nil {
		return
	}
	s := p.stats
	if s.kept+s.droppedForeign+s.droppedUntagged == 0 {
		return
	}
	helps.LogWithRequestID(ctx).Infof("meta reasoning replay: model=%s auth=%s kept=%d dropped_foreign=%d dropped_untagged=%d", model, authID, s.kept, s.droppedForeign, s.droppedUntagged)
}

// isMetaReasoningNotIssued reports whether an upstream error is Meta rejecting an
// envelope replayed through the wrong account.
func isMetaReasoningNotIssued(statusCode int, body []byte) bool {
	// An error event inside a 200 stream carries no HTTP status of its own, and
	// wrapMetaUpstreamError reports those as 502 unless the event names a code.
	if statusCode < 400 || statusCode >= 600 || len(body) == 0 {
		return false
	}
	return strings.Contains(strings.ToLower(string(body)), metaReasoningNotIssuedMessage)
}
