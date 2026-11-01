package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// MetaReasoningNotIssuedMessage is the stable fragment of the 400 Meta returns for
// an envelope replayed through an account that did not issue it.
const MetaReasoningNotIssuedMessage = "not issued to this caller"

// MetaReasoningReplayStats counts what happened to the reasoning envelopes of
// one request. The executor logs it so the replay hit rate can be measured.
type MetaReasoningReplayStats struct {
	Kept            int
	DroppedForeign  int
	DroppedUntagged int
}

// MetaReasoningReplay is the per-request Muse reasoning replay policy. Muse
// binds each reasoning envelope to the account that issued it, so the proxy
// tags every envelope it hands to a client with the issuing account's key and,
// on the way back in, keeps an envelope only when its tag names the credential
// selected for that very request.
type MetaReasoningReplay struct {
	// Enabled is true when the feature is on for this model and the selected
	// credential can be identified.
	Enabled bool
	// AccountKey names the credential selected for this request.
	AccountKey string
	// BaseURL is the selected credential's upstream, which decides how strictly
	// the native sanitizer gates Meta envelopes.
	BaseURL string
	Stats   *MetaReasoningReplayStats
}

// MetaReasoningAccountKey derives a stable, pseudonymous key for the credential.
// It has to survive token re-minting and restarts, so it is derived from the
// credential's identity (auth.ID), never from the short-lived API key. A
// credential without an ID gets no key, which leaves replay off for it.
func MetaReasoningAccountKey(auth *cliproxyauth.Auth) string {
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

// MetaReasoningReplayDecision is what the account policy decides for one
// reasoning item's encrypted_content.
type MetaReasoningReplayDecision struct {
	// Replay is the encrypted_content to forward. Used only when Drop is false.
	Replay string
	// Drop removes the encrypted_content; Reason explains why.
	Drop   bool
	Reason string
}

// Decide applies the account policy to one string encrypted_content.
func (p MetaReasoningReplay) Decide(raw string) MetaReasoningReplayDecision {
	accountKey, envelope, tagged := signature.ParseMetaReasoningTag(raw)
	if !p.Enabled {
		if tagged {
			// The feature was on earlier and is off now: nothing may reach Meta
			// that did not reach it before the feature existed.
			p.Stats.DroppedForeign++
			return MetaReasoningReplayDecision{Drop: true, Reason: "Muse reasoning replay is disabled"}
		}
		// Without provenance, an untagged Muse envelope is forwarded as is (upstream
		// behaviour, still subject to the native-only shape check). It may belong to
		// another pool account, so it counts as kept: if Meta rejects it, the request
		// is retried once without reasoning.
		if signature.IsValidMetaReasoningSignature(raw) {
			p.Stats.Kept++
		}
		return MetaReasoningReplayDecision{Replay: raw}
	}
	switch {
	case !tagged:
		p.Stats.DroppedUntagged++
		return MetaReasoningReplayDecision{Drop: true, Reason: "untagged Muse reasoning envelope has no provable account"}
	case p.AccountKey == "" || accountKey != p.AccountKey:
		p.Stats.DroppedForeign++
		return MetaReasoningReplayDecision{Drop: true, Reason: "Muse reasoning envelope was issued by another account"}
	default:
		p.Stats.Kept++
		return MetaReasoningReplayDecision{Replay: envelope}
	}
}

// ApplyAccountPolicy applies the account policy to a Responses request body. A
// reasoning item that loses its envelope and has no summary or content of its
// own is removed entirely. The caller still runs the native-only Meta
// sanitizer afterwards, which strips anything that is not a recognized Meta
// envelope (fail-closed).
func (p MetaReasoningReplay) ApplyAccountPolicy(ctx context.Context, body []byte) []byte {
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
		decision := p.Decide(encrypted.String())
		if !decision.Drop {
			if decision.Replay == encrypted.String() {
				rebuilt = append(rebuilt, item.Raw)
				continue
			}
			next, errSet := sjson.Set(item.Raw, "encrypted_content", decision.Replay)
			if errSet == nil {
				rebuilt = append(rebuilt, next)
				changed = true
				continue
			}
			// Forwarding the tagged value would send Meta a string it never issued,
			// so fall back to dropping it like any other unusable envelope.
			decision = MetaReasoningReplayDecision{Drop: true, Reason: "failed to strip provenance tag: " + errSet.Error()}
		}
		changed = true
		LogWithRequestID(ctx).Debugf("meta executor: dropped reasoning encrypted_content at input[%d] reason=%s", index, decision.Reason)
		if metaReasoningReplayItemIsEmpty(item) {
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
		LogWithRequestID(ctx).Debugf("meta executor: failed to rebuild input after reasoning replay policy: %v", errSet)
		return body
	}
	return out
}

// metaReasoningReplayItemIsEmpty reports whether a reasoning item carries no summary
// and no content, so that nothing is left of it once encrypted_content is gone.
func metaReasoningReplayItemIsEmpty(item gjson.Result) bool {
	summary := item.Get("summary")
	if summary.Exists() && summary.Type != gjson.Null && !(summary.IsArray() && len(summary.Array()) == 0) {
		return false
	}
	content := item.Get("content")
	return !content.Exists() || content.Type == gjson.Null || (content.IsArray() && len(content.Array()) == 0)
}

// WithoutReplay is the policy for the single retry: it is enabled but names no
// account, so every envelope is dropped.
func (p MetaReasoningReplay) WithoutReplay() MetaReasoningReplay {
	return MetaReasoningReplay{Enabled: true, BaseURL: p.BaseURL, Stats: &MetaReasoningReplayStats{}}
}

// ShouldTag reports whether responses must carry the provenance tag.
func (p MetaReasoningReplay) ShouldTag() bool {
	return p.Enabled && p.AccountKey != ""
}

// TagResponseEvent wraps the reasoning envelopes in one upstream Responses event
// (or a bare non-streaming response object) with this account's provenance tag.
func (p MetaReasoningReplay) TagResponseEvent(eventData []byte) []byte {
	if !p.ShouldTag() || len(eventData) == 0 {
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

func (p MetaReasoningReplay) tagOutputItems(data []byte, outputPath string) []byte {
	output := gjson.GetBytes(data, outputPath)
	if !output.IsArray() {
		return data
	}
	for index := range output.Array() {
		data = p.tagReasoningItem(data, outputPath+"."+strconv.Itoa(index))
	}
	return data
}

func (p MetaReasoningReplay) tagReasoningItem(data []byte, itemPath string) []byte {
	item := gjson.GetBytes(data, itemPath)
	if item.Get("type").String() != "reasoning" {
		return data
	}
	encrypted := item.Get("encrypted_content")
	if encrypted.Type != gjson.String || encrypted.String() == "" {
		return data
	}
	tagged := signature.TagMetaReasoning(p.AccountKey, encrypted.String())
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

// RecordRequest logs what happened to the reasoning envelopes of one request, so the
// replay hit rate (kept / (kept + dropped_foreign)) can be measured from the logs.
func (p MetaReasoningReplay) RecordRequest(ctx context.Context, model, authID string) {
	if p.Stats == nil {
		return
	}
	s := p.Stats
	if s.Kept+s.DroppedForeign+s.DroppedUntagged == 0 {
		return
	}
	LogWithRequestID(ctx).Infof("meta reasoning replay: model=%s auth=%s kept=%d dropped_foreign=%d dropped_untagged=%d", model, authID, s.Kept, s.DroppedForeign, s.DroppedUntagged)
}

// IsMetaReasoningNotIssued reports whether an upstream error is Meta rejecting an
// envelope replayed through the wrong account.
func IsMetaReasoningNotIssued(statusCode int, body []byte) bool {
	// An error event inside a 200 stream carries no HTTP status of its own, so
	// callers report those with a 5xx stand-in unless the event names a code.
	if statusCode < 400 || statusCode >= 600 || len(body) == 0 {
		return false
	}
	return strings.Contains(strings.ToLower(string(body)), MetaReasoningNotIssuedMessage)
}
