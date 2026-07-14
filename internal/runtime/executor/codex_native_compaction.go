package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/observability"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"
)

const codexRemoteCompactionV2Feature = "remote_compaction_v2"

const (
	maxCodexNativeCompactionPasses  = 16
	maxCodexNativeCompactionReplans = 4
)

type codexNativeCompactionSettings struct {
	triggerTokens         int64
	contextWindow         int64
	preserveRecentTokens  int64
	retainedMessageTokens int64
	stateTTL              time.Duration
}

type codexNativeCompactionScope struct {
	ctx                         context.Context
	lane                        *helps.ClaudeCodeCompactionLane
	revision                    uint64
	clientInputTokens           int64
	envelopeHash                string
	sourcePrefixHashes          []string
	replacementItems            [][]byte
	rejectedEncryptedItemHashes []string
	replacementActive           bool
	replayScope                 codexReasoningReplayScope
	replayApplied               bool
	active                      bool
}

type codexNativeCompactionProgress struct {
	body                    []byte
	effectiveItems          [][]byte
	state                   helps.ClaudeCodeCompactionState
	sourceBoundary          int
	replayInsertion         codexInsertedItems
	estimatedUpstreamTokens int64
}

func (s codexNativeCompactionScope) observeTerminal(eventData []byte) {
	if !s.active || s.lane == nil || s.revision == 0 {
		return
	}
	eventType := gjson.GetBytes(eventData, "type").String()
	if eventType != "response.completed" && eventType != "response.incomplete" {
		return
	}
	inputTokens := gjson.GetBytes(eventData, "response.usage.input_tokens").Int()
	if inputTokens <= 0 {
		inputTokens = gjson.GetBytes(eventData, "response.usage.prompt_tokens").Int()
	}
	pendingContextTokens := gjson.GetBytes(eventData, "response.usage.output_tokens_details.reasoning_tokens").Int()
	if pendingContextTokens <= 0 {
		pendingContextTokens = gjson.GetBytes(eventData, "response.usage.reasoning_tokens").Int()
	}
	if err := s.lane.ObserveTerminal(s.revision, s.clientInputTokens, inputTokens, pendingContextTokens); err != nil {
		helps.LogWithRequestID(s.ctx).Warnf("codex native compaction: persist terminal usage: %v", err)
	}
}

func (s codexNativeCompactionScope) abandon() {
	if !s.active || s.lane == nil {
		return
	}
	s.lane.AbandonObservation()
}

func (s codexNativeCompactionScope) rejectEncryptedState(requestBody []byte, rejectReasoning bool) (bool, error) {
	if !s.active || s.lane == nil || s.revision == 0 {
		return false, nil
	}
	rejectedHashes := append([]string(nil), s.rejectedEncryptedItemHashes...)
	if rejectReasoning {
		items, _ := codexInputItems(requestBody)
		rejectedHashes = codexMergeUniqueHashes(rejectedHashes, codexRejectedEncryptedItemHashes(items))
	}
	expected := helps.ClaudeCodeCompactionState{
		EnvelopeHash:       s.envelopeHash,
		SourcePrefixHashes: append([]string(nil), s.sourcePrefixHashes...),
		ReplacementItems:   codexCloneItems(s.replacementItems),
	}
	next := helps.ClaudeCodeCompactionState{
		EnvelopeHash:                s.envelopeHash,
		RejectedEncryptedItemHashes: rejectedHashes,
	}
	return s.lane.ReplaceStateIfCurrentMatches(expected, next)
}

func (e *CodexExecutor) nativeCompactionSettings() (codexNativeCompactionSettings, bool) {
	if e == nil || e.cfg == nil || !e.cfg.Codex.NativeCompaction.Enabled {
		return codexNativeCompactionSettings{}, false
	}
	cfg := e.cfg.Codex.NativeCompaction
	settings := codexNativeCompactionSettings{
		triggerTokens:         cfg.TriggerTokens,
		contextWindow:         cfg.ContextWindow,
		preserveRecentTokens:  cfg.PreserveRecentTokens,
		retainedMessageTokens: cfg.RetainedMessageTokens,
		stateTTL:              7 * 24 * time.Hour,
	}
	if settings.triggerTokens <= 0 {
		settings.triggerTokens = 240_000
	}
	if settings.contextWindow <= 0 {
		settings.contextWindow = 272_000
	}
	if settings.triggerTokens >= settings.contextWindow {
		settings.triggerTokens = settings.contextWindow * 9 / 10
	}
	if settings.preserveRecentTokens <= 0 {
		settings.preserveRecentTokens = settings.contextWindow - settings.triggerTokens
		if settings.preserveRecentTokens <= 0 {
			settings.preserveRecentTokens = 32_000
		}
	}
	if settings.retainedMessageTokens <= 0 {
		settings.retainedMessageTokens = 64_000
	}
	if rawTTL := strings.TrimSpace(cfg.StateTTL); rawTTL != "" {
		if parsed, err := time.ParseDuration(rawTTL); err == nil && parsed > 0 {
			settings.stateTTL = parsed
		}
	}
	return settings, true
}

// prepareCodexNativeCompaction applies a previously installed compaction lane,
// and performs an inline native Responses compaction when the configured token
// threshold is reached. The returned scope observes terminal generation usage
// so the next decision can use exact upstream input-token telemetry.
func (e *CodexExecutor) prepareCodexNativeCompaction(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	from sdktranslator.Format,
	opts cliproxyexecutor.Options,
	originalPayload []byte,
	body []byte,
	baseModel string,
	apiKey string,
	baseURL string,
) ([]byte, codexNativeCompactionScope, bool, error) {
	settings, enabled := e.nativeCompactionSettings()
	if !enabled || !sourceFormatEqual(from, sdktranslator.FormatClaude) || auth == nil {
		return body, codexNativeCompactionScope{}, false, nil
	}

	sessionID := helps.ExtractClaudeCodeSessionID(ctx, req.Payload, opts.Headers)
	agentID := helps.ExtractClaudeCodeAgentID(ctx, opts.Headers)
	key, ok := helps.NewClaudeCodeCompactionLaneKey(sessionID, agentID, req.Model, auth.ID)
	if !ok {
		return body, codexNativeCompactionScope{}, false, nil
	}

	inputItems, ok := codexInputItems(body)
	if !ok || len(inputItems) == 0 {
		return body, codexNativeCompactionScope{}, false, nil
	}
	clientBody := append([]byte(nil), body...)
	clientHashes := codexHashItems(inputItems)
	envelopeHash := codexCompactionEnvelopeHash(body)
	enc, err := tokenizerForCodexModel(baseModel)
	if err != nil {
		return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: tokenizer: %w", err)
	}
	clientInputTokens, err := countCodexInputTokens(enc, body)
	if err != nil {
		return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: count input: %w", err)
	}

	authDir, err := util.ResolveAuthDir(e.cfg.AuthDir)
	if err != nil {
		return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: resolve state directory: %w", err)
	}
	stateDir := filepath.Join(authDir, "state", "codex-native-compaction")
	lane := helps.LockClaudeCodeCompactionLane(key, settings.stateTTL, stateDir)
	defer lane.Unlock()
	if persistErr := lane.PersistenceError(); persistErr != nil {
		return body, codexNativeCompactionScope{}, false, statusErr{
			code: http.StatusInternalServerError,
			msg:  fmt.Sprintf("codex native compaction state is unavailable: %v", persistErr),
		}
	}
	state := lane.State()
	if len(state.ReplacementItems) == 0 && state.EnvelopeHash != "" && state.EnvelopeHash != envelopeHash {
		resetState := helps.ClaudeCodeCompactionState{
			RejectedEncryptedItemHashes: append([]string(nil), state.RejectedEncryptedItemHashes...),
		}
		if _, commitErr := lane.Commit(resetState); commitErr != nil {
			return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: reset changed envelope state: %w", commitErr)
		}
		state = lane.State()
	}
	if len(state.ReplacementItems) > 0 && (state.EnvelopeHash != envelopeHash || !codexHashesHavePrefix(clientHashes, state.SourcePrefixHashes)) {
		reason := "source prefix changed"
		if state.EnvelopeHash != envelopeHash {
			reason = "request envelope changed"
		}
		helps.LogWithRequestID(ctx).Warnf("codex native compaction: resetting lane because %s", reason)
		resetState := helps.ClaudeCodeCompactionState{
			RejectedEncryptedItemHashes: append([]string(nil), state.RejectedEncryptedItemHashes...),
		}
		if _, commitErr := lane.Commit(resetState); commitErr != nil {
			return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: reset changed source state: %w", commitErr)
		}
		observability.RecordCompactionReset(observability.NewCompactionResetDiagnostic(
			"codex",
			baseModel,
			reason,
			strings.Join([]string{key.SessionID, key.AgentID, key.Model, key.AuthID}, "\x00"),
			key.AgentID,
			state.EnvelopeHash,
			envelopeHash,
		))
		state = lane.State()
	}

	effectiveItems := codexSanitizeRejectedEncryptedItems(inputItems, state.RejectedEncryptedItemHashes)
	body = codexSetInputItems(body, effectiveItems)
	sourceBoundary := 0
	if len(state.ReplacementItems) > 0 {
		sourceBoundary = len(state.SourcePrefixHashes)
		effectiveItems = append(codexCloneItems(state.ReplacementItems), codexCloneItems(effectiveItems[sourceBoundary:])...)
		body = codexSetInputItems(body, effectiveItems)
	}
	baseEffectiveItems := codexCloneItems(effectiveItems)
	body, replayApplication, errReplay := applyCodexReasoningReplayCacheRequiredWithSuppression(
		ctx, from, req, opts, body, state.AbsorbedReplayItemHashes, state.RejectedEncryptedItemHashes,
	)
	replayScope := replayApplication.scope
	if errReplay != nil {
		return body, codexNativeCompactionScope{}, len(state.ReplacementItems) > 0, errReplay
	}
	effectiveItems, ok = codexInputItems(body)
	if !ok {
		return body, codexNativeCompactionScope{}, len(state.ReplacementItems) > 0, fmt.Errorf("codex native compaction: reasoning replay produced an invalid input array")
	}
	replayInsertion, mapOK := codexInsertedItemSpan(baseEffectiveItems, effectiveItems)
	if !mapOK {
		return body, codexNativeCompactionScope{}, len(state.ReplacementItems) > 0, fmt.Errorf("codex native compaction: could not map reasoning replay onto the client history")
	}

	rewrittenTokens, err := countCodexInputTokens(enc, body)
	if err != nil {
		return body, codexNativeCompactionScope{}, len(state.ReplacementItems) > 0, fmt.Errorf("codex native compaction: count rewritten input: %w", err)
	}
	estimatedUpstreamTokens := rewrittenTokens + state.CompactionTokens
	if state.UpstreamInputTokens > 0 && state.ClientInputTokens > 0 {
		delta := clientInputTokens - state.ClientInputTokens
		if delta >= 0 && state.UpstreamInputTokens+delta+state.PendingContextTokens > estimatedUpstreamTokens {
			estimatedUpstreamTokens = state.UpstreamInputTokens + delta + state.PendingContextTokens
		}
	}

	if estimatedUpstreamTokens >= settings.triggerTokens {
		progress := codexNativeCompactionProgress{
			body:                    body,
			effectiveItems:          effectiveItems,
			state:                   state,
			sourceBoundary:          sourceBoundary,
			replayInsertion:         replayInsertion,
			estimatedUpstreamTokens: estimatedUpstreamTokens,
		}
		progress, failedPrefix, compactErr := e.runCodexNativeCompactionPasses(
			ctx, auth, req, from, opts.Headers, originalPayload, baseModel, apiKey, baseURL,
			settings, enc, envelopeHash, clientHashes, replayApplication.sourceItems, lane, progress,
		)
		recoveryAttempted := false
		if codexCompactionErrorIsInvalidReasoning(compactErr) {
			recoveryAttempted = true

			// An invalid encrypted reasoning item can originate in replay, client
			// history, or a previously installed compaction. Restart from the
			// authoritative client history with only the rejected encrypted items
			// suppressed; the shared replay cache remains available to other lanes.
			rejectedHashes := codexMergeUniqueHashes(
				progress.state.RejectedEncryptedItemHashes,
				codexRejectedEncryptedItemHashes(failedPrefix),
			)
			recoveryState := helps.ClaudeCodeCompactionState{
				EnvelopeHash:                envelopeHash,
				RejectedEncryptedItemHashes: rejectedHashes,
			}
			if _, commitErr := lane.Commit(recoveryState); commitErr != nil {
				return progress.body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: persist rejected encrypted reasoning recovery: %w", commitErr)
			}
			state = lane.State()
			effectiveItems = codexSanitizeRejectedEncryptedItems(inputItems, rejectedHashes)
			baseEffectiveItems = codexCloneItems(effectiveItems)
			body = codexSetInputItems(clientBody, effectiveItems)
			replaySuppressions := codexMergeUniqueHashes(
				state.AbsorbedReplayItemHashes,
				codexRejectedReplayItemHashes(replayApplication.sourceItems, rejectedHashes),
			)
			body, _ = applyCodexReasoningReplayItems(body, replayApplication.sourceItems, replaySuppressions)
			effectiveItems, ok = codexInputItems(body)
			if !ok {
				return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: recovery replay produced an invalid input array")
			}
			replayInsertion, mapOK = codexInsertedItemSpan(baseEffectiveItems, effectiveItems)
			if !mapOK {
				return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: could not map recovery replay onto the client history")
			}
			recoveredTokens, countErr := countCodexInputTokens(enc, body)
			if countErr != nil {
				return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: count recovered input: %w", countErr)
			}
			progress = codexNativeCompactionProgress{
				body:                    body,
				effectiveItems:          effectiveItems,
				state:                   state,
				replayInsertion:         replayInsertion,
				estimatedUpstreamTokens: recoveredTokens,
			}
			progress, failedPrefix, compactErr = e.runCodexNativeCompactionPasses(
				ctx, auth, req, from, opts.Headers, originalPayload, baseModel, apiKey, baseURL,
				settings, enc, envelopeHash, clientHashes, replayApplication.sourceItems, lane, progress,
			)
		}
		body = progress.body
		effectiveItems = progress.effectiveItems
		state = progress.state
		estimatedUpstreamTokens = progress.estimatedUpstreamTokens
		if compactErr != nil {
			if codexCompactionErrorIsInvalidReasoning(compactErr) && recoveryAttempted {
				rejectedHashes := codexMergeUniqueHashes(
					state.RejectedEncryptedItemHashes,
					codexRejectedEncryptedItemHashes(failedPrefix),
				)
				recoveryState := helps.ClaudeCodeCompactionState{
					EnvelopeHash:                envelopeHash,
					RejectedEncryptedItemHashes: rejectedHashes,
				}
				if _, commitErr := lane.Commit(recoveryState); commitErr != nil {
					return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: persist second rejected encrypted reasoning recovery: %w", commitErr)
				}
				state = lane.State()
				effectiveItems = codexSanitizeRejectedEncryptedItems(inputItems, rejectedHashes)
				body = codexSetInputItems(clientBody, effectiveItems)
				replaySuppressions := codexMergeUniqueHashes(
					state.AbsorbedReplayItemHashes,
					codexRejectedReplayItemHashes(replayApplication.sourceItems, rejectedHashes),
				)
				body, _ = applyCodexReasoningReplayItems(body, replayApplication.sourceItems, replaySuppressions)
				effectiveItems, ok = codexInputItems(body)
				if !ok {
					return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: second recovery replay produced an invalid input array")
				}
				secondRecoveredTokens, countErr := countCodexInputTokens(enc, body)
				if countErr != nil {
					return body, codexNativeCompactionScope{}, false, fmt.Errorf("codex native compaction: count second recovered input: %w", countErr)
				}
				estimatedUpstreamTokens = secondRecoveredTokens
			}
			if estimatedUpstreamTokens >= settings.contextWindow {
				return body, codexNativeCompactionScope{}, len(state.ReplacementItems) > 0, statusErr{
					code: http.StatusBadRequest,
					msg:  fmt.Sprintf("codex native compaction failed at the configured %d-token context boundary: %v", settings.contextWindow, compactErr),
				}
			}
			helps.LogWithRequestID(ctx).Warnf("codex native compaction failed below hard context boundary; continuing with current lane: %v", compactErr)
		}
	}

	state = lane.State()
	compactionActive := len(state.ReplacementItems) > 0
	if state.EnvelopeHash == "" {
		state.EnvelopeHash = envelopeHash
		if _, commitErr := lane.Commit(state); commitErr != nil {
			return body, codexNativeCompactionScope{}, compactionActive, fmt.Errorf("codex native compaction: persist initial lane state: %w", commitErr)
		}
	}
	revision := lane.BeginObservation()
	scope := codexNativeCompactionScope{
		ctx:                         ctx,
		lane:                        lane,
		revision:                    revision,
		clientInputTokens:           clientInputTokens,
		envelopeHash:                envelopeHash,
		sourcePrefixHashes:          append([]string(nil), state.SourcePrefixHashes...),
		replacementItems:            codexCloneItems(state.ReplacementItems),
		rejectedEncryptedItemHashes: append([]string(nil), state.RejectedEncryptedItemHashes...),
		replacementActive:           len(state.ReplacementItems) > 0,
		replayScope:                 replayScope,
		replayApplied:               true,
		active:                      true,
	}
	return body, scope, compactionActive, nil
}

func (e *CodexExecutor) runCodexNativeCompactionPasses(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	from sdktranslator.Format,
	requestHeaders http.Header,
	originalPayload []byte,
	baseModel string,
	apiKey string,
	baseURL string,
	settings codexNativeCompactionSettings,
	enc tokenizer.Codec,
	envelopeHash string,
	clientHashes []string,
	replaySourceItems [][]byte,
	lane *helps.ClaudeCodeCompactionLane,
	progress codexNativeCompactionProgress,
) (codexNativeCompactionProgress, [][]byte, error) {
	var lastPrefix [][]byte
	for pass := 1; progress.estimatedUpstreamTokens >= settings.triggerTokens; pass++ {
		if pass > maxCodexNativeCompactionPasses {
			return progress, lastPrefix, codexCompactionProtocolError{message: fmt.Sprintf(
				"codex native compaction still exceeds the %d-token trigger after %d bounded passes",
				settings.triggerTokens,
				maxCodexNativeCompactionPasses,
			)}
		}

		maximumCut := codexCompactionCut(enc, progress.effectiveItems, settings.preserveRecentTokens)
		minimumCut := len(progress.state.ReplacementItems)
		if progress.replayInsertion.count > 0 && progress.replayInsertion.start < minimumCut {
			minimumCut += progress.replayInsertion.count
		}
		if len(progress.state.ReplacementItems) > 0 && maximumCut < minimumCut {
			maximumCut = minimumCut
		}
		maximumCut = codexNormalizeCompactionCut(progress.effectiveItems, maximumCut, progress.replayInsertion)
		if maximumCut <= 0 || maximumCut > len(progress.effectiveItems) {
			return progress, lastPrefix, codexCompactionProtocolError{message: "codex native compaction could not preserve a safe recent tail"}
		}

		attemptBudget := codexNativeCompactionInputBudget(settings)
		attemptMaximumCut := maximumCut
		var result codexNativeCompactionResult
		var cut int
		var compactErr error
		for replan := 0; ; replan++ {
			var inputEstimate int64
			cut, inputEstimate, compactErr = codexBoundedCompactionCut(
				enc,
				progress.body,
				progress.effectiveItems,
				attemptMaximumCut,
				minimumCut,
				progress.replayInsertion,
				progress.state.CompactionTokens,
				attemptBudget,
			)
			if compactErr != nil {
				return progress, lastPrefix, fmt.Errorf("codex native compaction: choose bounded prefix: %w", compactErr)
			}
			if cut <= 0 {
				return progress, lastPrefix, codexCompactionProtocolError{message: fmt.Sprintf(
					"codex native compaction could not fit a complete prefix within the %d-token compaction budget",
					attemptBudget,
				)}
			}

			lastPrefix = codexCloneItems(progress.effectiveItems[:cut])
			compactionBody := codexSetInputItems(progress.body, lastPrefix)
			result, compactErr = e.requestCodexNativeCompaction(
				ctx, auth, req, from, originalPayload, requestHeaders, compactionBody, baseModel, apiKey, baseURL,
			)
			if compactErr == nil || !codexCompactionErrorIsContextLength(compactErr) || replan >= maxCodexNativeCompactionReplans {
				break
			}

			nextMaximumCut := cut - 1
			nextBudget := inputEstimate * 3 / 4
			if nextBudget <= 0 || nextBudget >= attemptBudget {
				nextBudget = attemptBudget * 3 / 4
			}
			if nextBudget >= attemptBudget {
				nextBudget = attemptBudget - 1
			}
			if nextMaximumCut <= 0 || nextBudget <= 0 {
				break
			}
			helps.LogWithRequestID(ctx).Warnf(
				"codex native compaction prefix was rejected for context length; replanning below %d estimated tokens (pass %d, replan %d)",
				nextBudget,
				pass,
				replan+1,
			)
			attemptMaximumCut = nextMaximumCut
			attemptBudget = nextBudget
		}
		if compactErr != nil {
			return progress, lastPrefix, compactErr
		}

		baseCut := codexBaseItemCut(cut, progress.replayInsertion)
		newSourceBoundary := progress.sourceBoundary
		previousReplacementLength := len(progress.state.ReplacementItems)
		if previousReplacementLength == 0 {
			newSourceBoundary = baseCut
		} else if baseCut > previousReplacementLength {
			newSourceBoundary += baseCut - previousReplacementLength
		}
		if newSourceBoundary > len(clientHashes) {
			newSourceBoundary = len(clientHashes)
		}

		var replacement [][]byte
		if result.legacy {
			// The legacy endpoint returns the authoritative replacement history,
			// including any retained messages and server-selected metadata.
			replacement = codexCloneItems(result.items)
		} else {
			replacement = codexRetainedMessages(enc, lastPrefix, settings.retainedMessageTokens)
			replacement = append(replacement, codexCloneItems(result.items)...)
		}
		tail := codexCloneItems(progress.effectiveItems[cut:])

		compactionTokens := result.outputTokens
		if compactionTokens <= 0 {
			for _, item := range result.items {
				if gjson.GetBytes(item, "type").String() == "compaction" {
					compactionTokens += codexApproxOpaqueCompactionTokens(item)
				}
			}
		}
		nextState := helps.ClaudeCodeCompactionState{
			SourcePrefixHashes: clientHashes[:newSourceBoundary],
			ReplacementItems:   replacement,
			EnvelopeHash:       envelopeHash,
			CompactionTokens:   compactionTokens,
			AbsorbedReplayItemHashes: codexAbsorbedReplayItemHashes(
				progress.state.AbsorbedReplayItemHashes,
				replaySourceItems,
				lastPrefix,
			),
			RejectedEncryptedItemHashes: append([]string(nil), progress.state.RejectedEncryptedItemHashes...),
		}
		if _, commitErr := lane.Commit(nextState); commitErr != nil {
			if progress.estimatedUpstreamTokens >= settings.contextWindow {
				return progress, lastPrefix, statusErr{
					code: http.StatusInternalServerError,
					msg: fmt.Sprintf(
						"codex native compaction completed but its durable state could not be installed at the configured %d-token context boundary: %v",
						settings.contextWindow,
						commitErr,
					),
				}
			}
			helps.LogWithRequestID(ctx).Warnf(
				"codex native compaction completed but durable state installation failed below the hard boundary; continuing with the prior lane: %v",
				commitErr,
			)
			return progress, lastPrefix, nil
		}

		progress.effectiveItems = append(codexCloneItems(replacement), tail...)
		progress.body = codexSetInputItems(progress.body, progress.effectiveItems)
		progress.replayInsertion = codexAdvanceInsertedItemSpan(progress.replayInsertion, cut, len(replacement))
		progress.sourceBoundary = newSourceBoundary
		progress.state = lane.State()
		rewrittenTokens, countErr := countCodexInputTokens(enc, progress.body)
		if countErr != nil {
			return progress, lastPrefix, fmt.Errorf("codex native compaction: count bounded pass output: %w", countErr)
		}
		progress.estimatedUpstreamTokens = rewrittenTokens + progress.state.CompactionTokens
		helps.LogWithRequestID(ctx).Infof(
			"codex native compaction completed bounded pass %d; estimated rewritten input is %d tokens",
			pass,
			progress.estimatedUpstreamTokens,
		)
		if cut >= maximumCut {
			break
		}
	}
	return progress, lastPrefix, nil
}

func codexNativeCompactionInputBudget(settings codexNativeCompactionSettings) int64 {
	// Tiny context windows are used by unit tests to force boundary branches;
	// no supported Codex model has a context window below 1K tokens.
	if settings.contextWindow < 1_024 {
		if settings.triggerTokens > 1_024 {
			return settings.triggerTokens
		}
		return 1_024
	}
	budget := settings.contextWindow - settings.preserveRecentTokens
	if budget <= 0 {
		budget = settings.triggerTokens
	}
	if budget > settings.contextWindow {
		budget = settings.contextWindow
	}
	return budget
}

type codexInsertedItems struct {
	start int
	count int
}

// codexInsertedItemSpan verifies that transformed consists of base plus one
// contiguous insertion. Reasoning replay has exactly that shape, which lets the
// compactor keep durable source-prefix hashes tied solely to client items.
func codexInsertedItemSpan(base, transformed [][]byte) (codexInsertedItems, bool) {
	delta := len(transformed) - len(base)
	if delta < 0 {
		return codexInsertedItems{}, false
	}
	start := 0
	for start < len(base) && start < len(transformed) && bytes.Equal(base[start], transformed[start]) {
		start++
	}
	if delta == 0 {
		return codexInsertedItems{start: len(base)}, start == len(base)
	}
	for i := start; i < len(base); i++ {
		if !bytes.Equal(base[i], transformed[i+delta]) {
			return codexInsertedItems{}, false
		}
	}
	return codexInsertedItems{start: start, count: delta}, true
}

func codexAdjustCompactionCutForInsertedItems(cut int, inserted codexInsertedItems) int {
	if inserted.count > 0 && cut > inserted.start && cut < inserted.start+inserted.count {
		return inserted.start
	}
	return cut
}

func codexBaseItemCut(cut int, inserted codexInsertedItems) int {
	if inserted.count == 0 || cut <= inserted.start {
		return cut
	}
	if cut < inserted.start+inserted.count {
		return inserted.start
	}
	return cut - inserted.count
}

func codexNormalizeCompactionCut(items [][]byte, cut int, inserted codexInsertedItems) int {
	cut = codexAdjustCompactionCutForInsertedItems(cut, inserted)
	cut = codexAdjustCompactionCutForToolPairs(items, cut)
	return codexAdjustCompactionCutForInsertedItems(cut, inserted)
}

func codexAdvanceInsertedItemSpan(inserted codexInsertedItems, cut, replacementLength int) codexInsertedItems {
	if inserted.count <= 0 {
		return codexInsertedItems{}
	}
	if cut <= inserted.start {
		return codexInsertedItems{
			start: replacementLength + inserted.start - cut,
			count: inserted.count,
		}
	}
	return codexInsertedItems{}
}

func codexBoundedCompactionCut(
	enc tokenizer.Codec,
	body []byte,
	items [][]byte,
	maximumCut int,
	minimumCut int,
	inserted codexInsertedItems,
	opaqueCompactionTokens int64,
	tokenLimit int64,
) (int, int64, error) {
	if maximumCut <= 0 || len(items) == 0 || tokenLimit <= 0 {
		return 0, 0, nil
	}
	if maximumCut > len(items) {
		maximumCut = len(items)
	}
	if minimumCut < 1 {
		minimumCut = 1
	}
	maximumCut = codexNormalizeCompactionCut(items, maximumCut, inserted)
	if maximumCut < minimumCut {
		return 0, 0, nil
	}

	countPrefix := func(cut int) (int64, error) {
		candidateBody := codexSetInputItems(body, items[:cut])
		tokens, err := countCodexInputTokens(enc, candidateBody)
		if err != nil {
			return 0, err
		}
		return tokens + opaqueCompactionTokens, nil
	}

	maximumTokens, err := countPrefix(maximumCut)
	if err != nil {
		return 0, 0, err
	}
	if maximumTokens <= tokenLimit {
		return maximumCut, maximumTokens, nil
	}

	bestCut := 0
	var bestTokens int64
	low, high := minimumCut, maximumCut-1
	for low <= high {
		rawCut := low + (high-low)/2
		candidateCut := codexNormalizeCompactionCut(items, rawCut, inserted)
		if candidateCut < minimumCut {
			low = rawCut + 1
			continue
		}
		candidateTokens, countErr := countPrefix(candidateCut)
		if countErr != nil {
			return 0, 0, countErr
		}
		if candidateTokens <= tokenLimit {
			if candidateCut > bestCut {
				bestCut = candidateCut
				bestTokens = candidateTokens
			}
			low = rawCut + 1
		} else {
			high = rawCut - 1
		}
	}
	return bestCut, bestTokens, nil
}

type codexNativeCompactionResult struct {
	items        [][]byte
	inputTokens  int64
	outputTokens int64
	legacy       bool
}

type codexCompactionProtocolError struct{ message string }

func (e codexCompactionProtocolError) Error() string { return e.message }

func (e *CodexExecutor) requestCodexNativeCompaction(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	from sdktranslator.Format,
	originalPayload []byte,
	requestHeaders http.Header,
	body []byte,
	baseModel string,
	apiKey string,
	baseURL string,
) (codexNativeCompactionResult, error) {
	result, err := e.requestCodexNativeCompactionTransport(ctx, auth, req, from, originalPayload, requestHeaders, body, baseModel, apiKey, baseURL, false)
	if err == nil {
		return result, nil
	}
	if codexShouldRetryV2Compaction(ctx, err) {
		helps.LogWithRequestID(ctx).Warnf("codex native compaction v2 attempt failed transiently; retrying once: %v", err)
		result, err = e.requestCodexNativeCompactionTransport(ctx, auth, req, from, originalPayload, requestHeaders, body, baseModel, apiKey, baseURL, false)
		if err == nil {
			return result, nil
		}
	}
	if !codexShouldFallbackToLegacyCompaction(err) {
		return codexNativeCompactionResult{}, err
	}
	helps.LogWithRequestID(ctx).Warnf("codex native compaction v2 explicitly unsupported; falling back to /responses/compact for this attempt: %v", err)
	return e.requestCodexNativeCompactionTransport(ctx, auth, req, from, originalPayload, requestHeaders, body, baseModel, apiKey, baseURL, true)
}

func codexShouldRetryV2Compaction(ctx context.Context, err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if codexCompactionErrorIsInvalidReasoning(err) || codexCompactionErrorIsContextLength(err) {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	var protocolErr codexCompactionProtocolError
	if errors.As(err, &protocolErr) {
		return true
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) {
		return true
	}
	code := status.StatusCode()
	return code == http.StatusRequestTimeout || code == http.StatusConflict || code == http.StatusTooEarly || code == http.StatusTooManyRequests || code >= 500
}

func codexCompactionErrorIsContextLength(err error) bool {
	if err == nil {
		return false
	}
	statusCode := http.StatusBadRequest
	var status interface{ StatusCode() int }
	if errors.As(err, &status) && status.StatusCode() > 0 {
		statusCode = status.StatusCode()
	}
	code, _, ok := codexStatusErrorClassification(statusCode, []byte(err.Error()))
	if ok && code == "context_too_large" {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "context_length_exceeded") ||
		strings.Contains(lower, "context_too_large") ||
		strings.Contains(lower, "context length") ||
		strings.Contains(lower, "maximum context") ||
		strings.Contains(lower, "too many tokens")
}

func codexCompactionErrorIsInvalidReasoning(err error) bool {
	if err == nil {
		return false
	}
	statusCode := http.StatusBadRequest
	var status interface{ StatusCode() int }
	if errors.As(err, &status) && status.StatusCode() > 0 {
		statusCode = status.StatusCode()
	}
	code, _, ok := codexStatusErrorClassification(statusCode, []byte(err.Error()))
	return ok && code == "thinking_signature_invalid"
}

func (e *CodexExecutor) requestCodexNativeCompactionTransport(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	from sdktranslator.Format,
	originalPayload []byte,
	requestHeaders http.Header,
	body []byte,
	baseModel string,
	apiKey string,
	baseURL string,
	legacy bool,
) (result codexNativeCompactionResult, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	reporter.SetOperation("compaction")
	defer reporter.TrackFailure(ctx, &err)

	if legacy {
		body, _ = sjson.DeleteBytes(body, "stream")
	} else {
		items, ok := codexInputItems(body)
		if !ok {
			return result, codexCompactionProtocolError{message: "codex native compaction: input is not an array"}
		}
		items = append(items, []byte(`{"type":"compaction_trigger"}`))
		body = codexSetInputItems(body, items)
		body, _ = sjson.SetBytes(body, "stream", true)
	}

	path := "/responses"
	if legacy {
		path = "/responses/compact"
	}
	url := strings.TrimSuffix(baseURL, "/") + path
	httpReq, upstreamBody, identityState, err := e.cacheHelper(ctx, from, url, auth, req, originalPayload, body, requestHeaders)
	if err != nil {
		return result, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, !legacy, e.cfg)
	if !legacy {
		appendCodexBetaFeature(httpReq.Header, codexRemoteCompactionV2Feature)
	}
	applyModelHeaderOverrides(httpReq.Header, baseModel)
	applyCodexIdentityConfuseHeaders(httpReq.Header, &identityState)
	recordCodexNativeCompactionRequest(ctx, e.cfg, auth, url, httpReq.Header, upstreamBody)

	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return result, err
	}
	defer func() {
		if closeErr := httpResp.Body.Close(); closeErr != nil {
			helps.LogWithRequestID(ctx).Warnf("codex native compaction: close response body: %v", closeErr)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return result, err
	}
	data = applyCodexIdentityConfuseResponsePayload(data, identityState)
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return result, newCodexStatusErr(httpResp.StatusCode, data)
	}

	result.legacy = legacy
	if legacy {
		result.items, err = parseCodexLegacyCompaction(data)
		if err != nil {
			return codexNativeCompactionResult{}, err
		}
		detail := helps.ParseOpenAIUsage(data)
		result.inputTokens = detail.InputTokens
		result.outputTokens = detail.OutputTokens
		reporter.Publish(ctx, detail)
	} else {
		var item []byte
		item, result.inputTokens, result.outputTokens, err = parseCodexRemoteCompactionV2(data)
		if err != nil {
			return codexNativeCompactionResult{}, err
		}
		result.items = [][]byte{item}
		if detail, ok := helps.ParseCodexUsage(codexCompletedEvent(data)); ok {
			reporter.Publish(ctx, detail)
		}
	}
	reporter.EnsurePublished(ctx)
	return result, nil
}

func recordCodexNativeCompactionRequest(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   headers.Clone(),
		Body:      body,
		Provider:  "codex",
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func codexShouldFallbackToLegacyCompaction(err error) bool {
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) {
		return false
	}
	switch status.StatusCode() {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		message := strings.ToLower(err.Error())
		mentionsFeature := strings.Contains(message, codexRemoteCompactionV2Feature) ||
			strings.Contains(message, "compaction trigger") ||
			strings.Contains(message, "compaction_trigger")
		explicitlyUnsupported := strings.Contains(message, "unsupported") ||
			strings.Contains(message, "not supported") ||
			strings.Contains(message, "unknown feature") ||
			strings.Contains(message, "unrecognized")
		return mentionsFeature && explicitlyUnsupported
	default:
		return false
	}
}

func appendCodexBetaFeature(headers http.Header, feature string) {
	if headers == nil || strings.TrimSpace(feature) == "" {
		return
	}
	current := headers.Get("X-Codex-Beta-Features")
	for _, existing := range strings.Split(current, ",") {
		if strings.EqualFold(strings.TrimSpace(existing), feature) {
			return
		}
	}
	if strings.TrimSpace(current) == "" {
		headers.Set("X-Codex-Beta-Features", feature)
		return
	}
	headers.Set("X-Codex-Beta-Features", current+","+feature)
}

func parseCodexRemoteCompactionV2(data []byte) ([]byte, int64, int64, error) {
	var compactionItems [][]byte
	var completed []byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		eventData := bytes.TrimSpace(line[len(dataTag):])
		switch gjson.GetBytes(eventData, "type").String() {
		case "response.output_item.done":
			item := gjson.GetBytes(eventData, "item")
			if item.Exists() && item.Type == gjson.JSON && item.Get("type").String() == "compaction" {
				compactionItems = append(compactionItems, []byte(item.Raw))
			}
		case "response.completed":
			completed = append([]byte(nil), eventData...)
		case "response.failed", "error":
			return nil, 0, 0, codexCompactionProtocolError{message: "codex native compaction failed: " + string(eventData)}
		}
	}
	if len(completed) == 0 {
		return nil, 0, 0, codexCompactionProtocolError{message: "codex native compaction stream closed before response.completed"}
	}
	if len(compactionItems) == 0 {
		for _, item := range gjson.GetBytes(completed, "response.output").Array() {
			if item.Get("type").String() == "compaction" {
				compactionItems = append(compactionItems, []byte(item.Raw))
			}
		}
	}
	if len(compactionItems) != 1 {
		return nil, 0, 0, codexCompactionProtocolError{message: fmt.Sprintf("codex native compaction expected exactly one compaction item, got %d", len(compactionItems))}
	}
	if strings.TrimSpace(gjson.GetBytes(compactionItems[0], "encrypted_content").String()) == "" {
		return nil, 0, 0, codexCompactionProtocolError{message: "codex native compaction returned an empty encrypted_content"}
	}
	usage := gjson.GetBytes(completed, "response.usage")
	return compactionItems[0], usage.Get("input_tokens").Int(), usage.Get("output_tokens").Int(), nil
}

func parseCodexLegacyCompaction(data []byte) ([][]byte, error) {
	output := gjson.GetBytes(data, "output")
	if !output.Exists() || !output.IsArray() {
		return nil, codexCompactionProtocolError{message: "legacy Codex compaction response is missing output"}
	}
	items := make([][]byte, 0, len(output.Array()))
	compactionItems := 0
	for _, item := range output.Array() {
		if item.Type != gjson.JSON {
			return nil, codexCompactionProtocolError{message: "legacy Codex compaction output contains a non-object item"}
		}
		items = append(items, []byte(item.Raw))
		if item.Get("type").String() == "compaction" {
			compactionItems++
			if strings.TrimSpace(item.Get("encrypted_content").String()) == "" {
				return nil, codexCompactionProtocolError{message: "legacy Codex compaction returned an empty encrypted_content"}
			}
		}
	}
	if compactionItems != 1 {
		return nil, codexCompactionProtocolError{message: fmt.Sprintf("legacy Codex compaction expected exactly one compaction item, got %d", compactionItems)}
	}
	return items, nil
}

func codexCompletedEvent(data []byte) []byte {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		eventData := bytes.TrimSpace(line[len(dataTag):])
		if gjson.GetBytes(eventData, "type").String() == "response.completed" {
			return eventData
		}
	}
	return nil
}

func codexInputItems(body []byte) ([][]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return nil, false
	}
	results := input.Array()
	items := make([][]byte, 0, len(results))
	for _, result := range results {
		if result.Type != gjson.JSON {
			return nil, false
		}
		items = append(items, []byte(result.Raw))
	}
	return items, true
}

func codexSetInputItems(body []byte, items [][]byte) []byte {
	array := codexJSONItems(items)
	updated, err := sjson.SetRawBytes(body, "input", array)
	if err != nil {
		return body
	}
	return updated
}

func codexJSONItems(items [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(item)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

func codexCloneItems(items [][]byte) [][]byte {
	cloned := make([][]byte, len(items))
	for i := range items {
		cloned[i] = append([]byte(nil), items[i]...)
	}
	return cloned
}

func codexHashItems(items [][]byte) []string {
	hashes := make([]string, len(items))
	for i, item := range items {
		sum := sha256.Sum256(item)
		hashes[i] = hex.EncodeToString(sum[:])
	}
	return hashes
}

func codexAbsorbedReplayItemHashes(existing []string, sourceItems, compactedPrefix [][]byte) []string {
	absorbed := make([]string, 0, len(existing)+len(sourceItems))
	existingSet := make(map[string]struct{}, len(existing)+len(sourceItems))
	for _, itemHash := range existing {
		itemHash = strings.TrimSpace(itemHash)
		if itemHash == "" {
			continue
		}
		if _, duplicate := existingSet[itemHash]; !duplicate {
			existingSet[itemHash] = struct{}{}
			absorbed = append(absorbed, itemHash)
		}
	}
	for _, sourceItem := range sourceItems {
		itemHash := codexReasoningReplayItemHash(sourceItem)
		if _, wasAlreadyAbsorbed := existingSet[itemHash]; wasAlreadyAbsorbed {
			continue
		}
		if !codexReplaySourceItemCoveredByPrefix(sourceItem, compactedPrefix) {
			continue
		}
		existingSet[itemHash] = struct{}{}
		absorbed = append(absorbed, itemHash)
	}
	return absorbed
}

func codexReplaySourceItemCoveredByPrefix(sourceItem []byte, prefix [][]byte) bool {
	source := gjson.ParseBytes(sourceItem)
	switch source.Get("type").String() {
	case "reasoning":
		encryptedContent := source.Get("encrypted_content").String()
		if encryptedContent == "" {
			return false
		}
		for _, item := range prefix {
			candidate := gjson.ParseBytes(item)
			if candidate.Get("type").String() == "reasoning" && candidate.Get("encrypted_content").String() == encryptedContent {
				return true
			}
		}
	case "function_call", "custom_tool_call":
		keys := codexReplayToolCallKeys(source)
		if len(keys) == 0 {
			return false
		}
		for _, item := range prefix {
			if codexReplayAnyToolCallKeyExists(codexReplayToolCallKeySet(gjson.ParseBytes(item)), keys) {
				return true
			}
		}
	}
	return false
}

func codexReplayToolCallKeySet(item gjson.Result) map[string]bool {
	keys := codexReplayToolCallKeys(item)
	if len(keys) == 0 {
		return nil
	}
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

func codexSanitizeRejectedEncryptedItems(items [][]byte, rejectedHashes []string) [][]byte {
	if len(rejectedHashes) == 0 {
		return codexCloneItems(items)
	}
	rejected := make(map[string]struct{}, len(rejectedHashes))
	for _, itemHash := range rejectedHashes {
		if strings.TrimSpace(itemHash) != "" {
			rejected[itemHash] = struct{}{}
		}
	}
	sanitized := codexCloneItems(items)
	for i, item := range sanitized {
		fingerprint := codexRejectedEncryptedItemFingerprint(item)
		if fingerprint == "" {
			continue
		}
		if _, rejectedItem := rejected[fingerprint]; !rejectedItem {
			continue
		}
		if updated, err := sjson.DeleteBytes(item, "encrypted_content"); err == nil {
			sanitized[i] = updated
		}
	}
	return sanitized
}

func codexRejectedEncryptedItemHashes(items [][]byte) []string {
	hashes := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range items {
		fingerprint := codexRejectedEncryptedItemFingerprint(item)
		if fingerprint == "" {
			continue
		}
		if _, exists := seen[fingerprint]; exists {
			continue
		}
		seen[fingerprint] = struct{}{}
		hashes = append(hashes, fingerprint)
	}
	return hashes
}

func codexRejectedReplayItemHashes(items [][]byte, rejectedEncryptedHashes []string) []string {
	rejected := make(map[string]struct{}, len(rejectedEncryptedHashes))
	for _, itemHash := range rejectedEncryptedHashes {
		if itemHash = strings.TrimSpace(itemHash); itemHash != "" {
			rejected[itemHash] = struct{}{}
		}
	}
	hashes := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range items {
		fingerprint := codexRejectedEncryptedItemFingerprint(item)
		if _, isRejected := rejected[fingerprint]; fingerprint == "" || !isRejected {
			continue
		}
		itemHash := codexReasoningReplayItemHash(item)
		if _, duplicate := seen[itemHash]; duplicate {
			continue
		}
		seen[itemHash] = struct{}{}
		hashes = append(hashes, itemHash)
	}
	return hashes
}

func codexRejectedEncryptedItemFingerprint(item []byte) string {
	parsed := gjson.ParseBytes(item)
	if parsed.Get("type").String() != "reasoning" {
		return ""
	}
	encryptedContent := parsed.Get("encrypted_content")
	if encryptedContent.Type != gjson.String || encryptedContent.String() == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("reasoning\x00" + encryptedContent.String()))
	return hex.EncodeToString(sum[:])
}

func codexMergeUniqueHashes(left, right []string) []string {
	merged := make([]string, 0, len(left)+len(right))
	seen := make(map[string]struct{}, len(left)+len(right))
	for _, group := range [][]string{left, right} {
		for _, itemHash := range group {
			itemHash = strings.TrimSpace(itemHash)
			if itemHash == "" {
				continue
			}
			if _, exists := seen[itemHash]; exists {
				continue
			}
			seen[itemHash] = struct{}{}
			merged = append(merged, itemHash)
		}
	}
	return merged
}

func codexHashesHavePrefix(items, prefix []string) bool {
	if len(prefix) > len(items) {
		return false
	}
	for i := range prefix {
		if items[i] != prefix[i] {
			return false
		}
	}
	return true
}

func codexCompactionEnvelopeHash(body []byte) string {
	envelope := append([]byte(nil), body...)
	for _, field := range []string{"input", "stream", "prompt_cache_key"} {
		envelope, _ = sjson.DeleteBytes(envelope, field)
	}
	sum := sha256.Sum256(envelope)
	return hex.EncodeToString(sum[:])
}

func codexCompactionCut(enc tokenizer.Codec, items [][]byte, preserveTokens int64) int {
	if len(items) <= 1 {
		return 0
	}
	if preserveTokens <= 0 {
		return len(items) - 1
	}
	remaining := preserveTokens
	cut := len(items)
	for i := len(items) - 1; i >= 0; i-- {
		cut = i
		remaining -= codexItemTokens(enc, items[i])
		if remaining <= 0 {
			break
		}
	}
	if cut <= 0 {
		return 0
	}
	return cut
}

func codexAdjustCompactionCutForToolPairs(items [][]byte, cut int) int {
	if cut <= 0 || cut >= len(items) {
		return cut
	}
	for {
		previousCut := cut
		for i := cut; i < len(items); i++ {
			outputType := gjson.GetBytes(items[i], "type").String()
			callType := ""
			switch outputType {
			case "function_call_output":
				callType = "function_call"
			case "custom_tool_call_output":
				callType = "custom_tool_call"
			default:
				continue
			}
			callID := gjson.GetBytes(items[i], "call_id").String()
			if callID == "" {
				continue
			}
			for j := cut - 1; j >= 0; j-- {
				if gjson.GetBytes(items[j], "type").String() == callType && gjson.GetBytes(items[j], "call_id").String() == callID {
					cut = j
					break
				}
			}
		}
		if cut == 0 || cut == previousCut {
			return cut
		}
	}
}

func codexRetainedMessages(enc tokenizer.Codec, items [][]byte, tokenBudget int64) [][]byte {
	if tokenBudget <= 0 {
		return nil
	}
	remaining := tokenBudget
	reversed := make([][]byte, 0)
	for i := len(items) - 1; i >= 0 && remaining > 0; i-- {
		item := items[i]
		if gjson.GetBytes(item, "type").String() != "message" {
			continue
		}
		switch gjson.GetBytes(item, "role").String() {
		case "user", "developer", "system":
		default:
			continue
		}
		tokens := codexItemTokens(enc, item)
		if tokens > remaining {
			remaining = 0
			continue
		}
		reversed = append(reversed, append([]byte(nil), item...))
		remaining -= tokens
	}
	retained := make([][]byte, len(reversed))
	for i := range reversed {
		retained[len(reversed)-1-i] = reversed[i]
	}
	return retained
}

func codexItemTokens(enc tokenizer.Codec, item []byte) int64 {
	body := codexSetInputItems([]byte(`{"input":[]}`), [][]byte{item})
	tokens, err := countCodexInputTokens(enc, body)
	if err != nil || tokens <= 0 {
		return 1
	}
	return tokens
}

func codexApproxOpaqueCompactionTokens(item []byte) int64 {
	encrypted := gjson.GetBytes(item, "encrypted_content").String()
	if encrypted == "" {
		return 1
	}
	// Opaque ciphertext cannot be tokenized accurately without server support.
	// Four bytes/token is deliberately conservative for cache-boundary planning.
	return int64(len(encrypted)+3) / 4
}
