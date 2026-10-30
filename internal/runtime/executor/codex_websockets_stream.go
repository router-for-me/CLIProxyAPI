package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (e *CodexWebsocketsExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	log.Debugf("Executing Codex Websockets stream request with auth ID: %s, model: %s", auth.ID, req.Model)
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	prepared, err := e.prepareCodexWebsocketStream(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	from := prepared.from
	responseFormat := prepared.responseFormat
	to := prepared.to
	preserveNativeOutput := prepared.preserveNativeOutput
	originalPayload := prepared.originalPayload
	clientBody := prepared.clientBody
	wsURL := prepared.wsURL
	wsHeaders := prepared.wsHeaders
	useSIWC := prepared.useSIWC
	replayScope := prepared.replayScope
	optimizeMultiAgentV2 := prepared.optimizeMultiAgentV2
	multiAgentV2Conflict := prepared.multiAgentV2Conflict
	reporter.SetTranslatedReasoningEffort(clientBody, to.String())

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()

	executionSessionID := executionSessionIDFromOptions(opts)
	var sess *codexWebsocketSession
	isEphemeralSession := false
	if executionSessionID != "" {
		sess = e.getOrCreateSession(executionSessionID)
		if sess != nil {
			sess.reqMu.Lock()
		}
	} else {
		isEphemeralSession = true
		sess = newEphemeralCodexWebsocketSession()
	}
	if sess != nil {
		sess.connMu.Lock()
		sess.recoverTransport = cliproxyexecutor.DownstreamWebsocket(ctx)
		sess.turnContext = ctx
		sess.connMu.Unlock()
	}
	streamSessionLocked := sess != nil && !isEphemeralSession
	unlockStreamSession := func() {
		if sess != nil && streamSessionLocked {
			sess.reqMu.Unlock()
			streamSessionLocked = false
		}
	}

	wsReqBody := frameCodexWebsocketRequestBody(clientBody)
	wsReqLog := helps.UpstreamRequestLog{
		URL:       wsURL,
		Method:    "WEBSOCKET",
		Headers:   wsHeaders.Clone(),
		Body:      wsReqBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	}
	helps.RecordAPIWebsocketRequest(ctx, e.cfg, wsReqLog)

	var conn *websocket.Conn
	var closer *websocketConnectionCloser
	var respHS *http.Response
	var errDial error
	dialCtx := ctx
	if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		conn, closer = existingWebsocketSessionConn(sess, authID, wsURL, executionProxyURL(ctx, sess.effectiveConfig(authID, e.cfg), auth), useSIWC)
		if conn == nil {
			unlockStreamSession()
			if cliproxyexecutor.DownstreamWebsocket(ctx) && sess != nil {
				sess.activeMu.Lock()
				lost := cliproxyexecutor.IsUpstreamWebsocketTurnError(sess.terminalErr)
				sess.activeMu.Unlock()
				if lost {
					// The socket this continuation refers to was lost, not replaced.
					return nil, &cliproxyexecutor.UpstreamWebsocketTurnError{FullContextRequired: true}
				}
			}
			return nil, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
		}
	} else {
		dialCtx = cliproxyexecutor.WithUpstreamAttemptTracker(ctx)
		conn, closer, respHS, errDial = e.ensureUpstreamConnWithSIWC(dialCtx, auth, sess, authID, wsURL, wsHeaders, useSIWC)
	}
	var upstreamHeaders http.Header
	if respHS != nil {
		upstreamHeaders = respHS.Header.Clone()
	}
	if errDial != nil {
		if cliproxyexecutor.IsUpstreamWebsocketTurnError(errDial) {
			unlockStreamSession()
			return nil, codexWebsocketTurnEventID(errDial, originalPayload)
		}
		bodyErr := websocketHandshakeBody(respHS)
		if respHS != nil {
			helps.RecordAPIWebsocketUpgradeRejection(ctx, e.cfg, websocketUpgradeRequestLog(wsReqLog), respHS.StatusCode, respHS.Header.Clone(), bodyErr)
		}
		if respHS != nil && respHS.StatusCode == http.StatusUpgradeRequired {
			unlockStreamSession()
			if opts.ExecutionLifecycle == nil && !cliproxyexecutor.DownstreamWebsocket(ctx) {
				return e.CodexExecutor.ExecuteStream(ctx, auth, req, opts)
			}
			if cliproxyexecutor.UpstreamAttempted(dialCtx) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
			}
			return nil, statusErr{code: respHS.StatusCode, msg: string(bodyErr)}
		}
		if cliproxyexecutor.UpstreamAttempted(dialCtx) {
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
		}
		if respHS != nil && respHS.StatusCode > 0 {
			unlockStreamSession()
			return nil, newCodexStatusErrWithHeadersAndCooling(respHS.StatusCode, respHS.Header, bodyErr, e.modelLevelCooling())
		}
		helps.RecordAPIWebsocketError(ctx, e.cfg, "dial", errDial)
		unlockStreamSession()
		return nil, errDial
	}
	if errBind := sess.bindExecutionLifecycle(opts, conn, closer, req.Model); errBind != nil {
		unlockStreamSession()
		closeWebsocketAfterBindFailure(sess, conn, closer)
		return nil, errBind
	}
	recordAPIWebsocketHandshake(ctx, e.cfg, respHS)
	reporter.StartResponseTTFT()

	if sess == nil {
		logCodexWebsocketConnected(executionSessionID, authID, wsURL)
	}

	var readCh chan codexWebsocketRead
	if sess != nil {
		readCh = sess.activate(conn)
	}
	restoreMultiAgentV2 := !multiAgentV2Conflict && (optimizeMultiAgentV2 || sess.isMultiAgentV2Optimized(conn))

	if sess != nil && !isEphemeralSession && cliproxyexecutor.DownstreamWebsocket(ctx) {
		if errLoss := cliproxyexecutor.WebsocketInputLossError(ctx); errLoss != nil {
			sess.clearActive(conn, readCh)
			unlockStreamSession()
			return nil, codexWebsocketTurnEventID(errLoss, originalPayload)
		}
	}
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if errSend := writeCodexWebsocketMessage(sess, conn, wsReqBody); errSend != nil {
		errSend = mapCodexWebsocketWriteError(sess, conn, errSend)
		helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
		if sess != nil && !isEphemeralSession && cliproxyexecutor.DownstreamWebsocket(ctx) {
			// A failed write may still have delivered the create: never send it again.
			errSend = codexWebsocketTurnEventID(codexWebsocketTurnTransportError(errSend), originalPayload)
			if cliproxyexecutor.IsUpstreamWebsocketTurnError(errSend) {
				// The reader may not see the failure before the invalidation: record the loss
				// so the next continuation answers 409 instead of requiring a replay.
				sess.markTerminalError(conn, errSend)
				e.invalidateUpstreamConn(sess, conn, "send_error", errSend)
				sess.clearActive(conn, readCh)
				sess.reqMu.Unlock()
				return nil, errSend
			}
		}
		if sess != nil && !isEphemeralSession {
			if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
				e.invalidateUpstreamConnWithoutDisconnectNotify(sess, conn, "send_error", errSend)
				sess.clearActive(conn, readCh)
				sess.reqMu.Unlock()
				if !shouldRetryCodexWebsocketSend(errSend) {
					return nil, errSend
				}
				return nil, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
			}
			e.invalidateUpstreamConn(sess, conn, "send_error", errSend)
			if !shouldRetryCodexWebsocketSend(errSend) {
				sess.clearActive(conn, readCh)
				sess.reqMu.Unlock()
				return nil, errSend
			}

			// Retry once with a new websocket connection for the same execution session.
			connRetry, closerRetry, respHSRetry, errDialRetry := e.ensureUpstreamConnWithSIWC(ctx, auth, sess, authID, wsURL, wsHeaders, useSIWC)
			if errDialRetry != nil || connRetry == nil {
				closeHTTPResponseBody(respHSRetry, "codex websockets executor: close handshake response body error")
				helps.RecordAPIWebsocketError(ctx, e.cfg, "dial_retry", errDialRetry)
				sess.clearActive(conn, readCh)
				sess.reqMu.Unlock()
				return nil, errDialRetry
			}
			previousConn, previousReadCh := conn, readCh
			conn = connRetry
			closer = closerRetry
			if errBind := sess.bindExecutionLifecycle(opts, conn, closer, req.Model); errBind != nil {
				clearRetryActiveState(sess, previousConn, previousReadCh)
				sess.reqMu.Unlock()
				closeWebsocketAfterBindFailure(sess, conn, closer)
				return nil, errBind
			}
			readCh = sess.activate(conn)
			restoreMultiAgentV2 = !multiAgentV2Conflict && (optimizeMultiAgentV2 || sess.isMultiAgentV2Optimized(conn))
			wsReqBodyRetry := frameCodexWebsocketRequestBody(clientBody)
			helps.RecordAPIWebsocketRequest(ctx, e.cfg, helps.UpstreamRequestLog{
				URL:       wsURL,
				Method:    "WEBSOCKET",
				Headers:   wsHeaders.Clone(),
				Body:      wsReqBodyRetry,
				Provider:  e.Identifier(),
				AuthID:    authID,
				AuthLabel: authLabel,
				AuthType:  authType,
				AuthValue: authValue,
			})
			recordAPIWebsocketHandshake(ctx, e.cfg, respHSRetry)
			reporter.StartResponseTTFT()
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
			if errSendRetry := writeCodexWebsocketMessage(sess, conn, wsReqBodyRetry); errSendRetry != nil {
				errSendRetry = mapCodexWebsocketWriteError(sess, conn, errSendRetry)
				helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry", errSendRetry)
				e.invalidateUpstreamConn(sess, conn, "send_error", errSendRetry)
				sess.clearActive(conn, readCh)
				sess.reqMu.Unlock()
				return nil, errSendRetry
			}
			wsReqBody = wsReqBodyRetry
		} else {
			if sess != nil {
				e.invalidateUpstreamConn(sess, conn, "send_error", errSend)
				sess.clearActive(conn, readCh)
				if isEphemeralSession {
					closeCodexWebsocketSession(sess, "send_error")
				}
			} else {
				logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, "send_error", errSend)
				if closer != nil {
					_ = closer.Close()
				}
			}
			return nil, errSend
		}
	}

	if optimizeMultiAgentV2 || multiAgentV2Conflict {
		sess.setMultiAgentV2Optimized(conn, optimizeMultiAgentV2 && !multiAgentV2Conflict)
	}

	if input := cliproxyexecutor.WebsocketInputFromContext(ctx); input != nil && e.cfg != nil && (e.cfg.Codex.ResponseSteering || e.cfg.CodexResponseSteering) {
		return e.streamCodexDuplex(ctx, auth, req, opts, sess, conn, readCh, input, prepared, reporter, upstreamHeaders, unlockStreamSession), nil
	}

	buffering := e.cfg != nil && e.cfg.Codex.StreamBootstrapBuffering
	var bootstrapTimeout time.Duration
	var bootstrapStart time.Time
	var exhaustionLogged bool
	if buffering {
		bootstrapTimeout = e.cfg.Codex.StreamBootstrapTimeoutDuration()
		bootstrapStart = nowCodexBootstrap()
	}

	claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
	var param any
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte

	var bufferedChunks [][]byte
	// bufferedFrames counts every websocket message read during bootstrap, including the ones the
	// loop skips, so a peer that only sends frames the loop ignores cannot keep the window open.
	bufferedFrames := 0
	bufferedBytes := 0
	var initialChunks [][]byte
	immediateTerminal := false
	immediateIncomplete := false
	// bootstrapTerminalErr holds a non-overload terminal failure seen while buffering. It is
	// delivered as an in-stream chunk after the buffered handshake so downstream behaviour stays
	// identical to the unbuffered path instead of silently turning into a credential failover.
	var bootstrapTerminalErr error
	sawOutputDelta := false

	if buffering {
		for {
			if ctx != nil && ctx.Err() != nil {
				if sess != nil {
					sess.clearActive(conn, readCh)
					unlockStreamSession()
					if isEphemeralSession {
						closeCodexWebsocketSession(sess, "context_done")
					}
				} else {
					_ = closer.Close()
				}
				return nil, ctx.Err()
			}
			msgType, payload, errRead := readCodexWebsocketMessage(ctx, sess, conn, readCh)
			if errRead != nil {
				mappedErr := codexWebsocketTurnEventID(mapCodexWebsocketReadError(errRead), originalPayload)
				if sess != nil {
					e.invalidateUpstreamConn(sess, conn, "read_error", mappedErr)
					sess.clearActive(conn, readCh)
					unlockStreamSession()
					if isEphemeralSession {
						closeCodexWebsocketSession(sess, "read_error")
					}
				} else {
					logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, "read_error", mappedErr)
					_ = closer.Close()
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "read", mappedErr)
				reporter.PublishFailure(ctx, mappedErr)
				return nil, mappedErr
			}
			// Count every message ReadMessage returns, including the ones this loop goes on to skip,
			// so a peer sending only skippable text frames still closes the window. Control frames
			// are not counted: the websocket library answers ping and pong inside ReadMessage and
			// never returns them, so only the read deadline bounds a peer that sends nothing else.
			// windowOpen is carried into the skip branches below rather than breaking here, because
			// this message has not been processed yet and dropping it would lose a token, or a
			// terminal event, from the turn.
			bufferedFrames++
			timeSinceStart := nowCodexBootstrap().Sub(bootstrapStart)
			timeoutReached := bootstrapTimeout > 0 && timeSinceStart >= bootstrapTimeout
			windowOpen := bufferedFrames <= codexBootstrapMaxBufferedFrames && !timeoutReached
			if !windowOpen && !exhaustionLogged {
				exhaustionLogged = true
				exhausted := "frame budget"
				if timeoutReached {
					exhausted = "time budget"
				}
				helps.LogWithRequestID(ctx).Debugf("codex websockets executor: bootstrap %s exhausted after %d messages read / %v; this message will be released", exhausted, bufferedFrames, timeSinceStart)
			}
			if msgType != websocket.TextMessage {
				if msgType == websocket.BinaryMessage {
					errBinary := fmt.Errorf("codex websockets executor: unexpected binary message")
					if sess != nil {
						e.invalidateUpstreamConn(sess, conn, "unexpected_binary", errBinary)
						sess.clearActive(conn, readCh)
						unlockStreamSession()
						if isEphemeralSession {
							closeCodexWebsocketSession(sess, "unexpected_binary")
						}
					} else {
						logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, "unexpected_binary", errBinary)
						_ = closer.Close()
					}
					helps.RecordAPIWebsocketError(ctx, e.cfg, "unexpected_binary", errBinary)
					reporter.PublishFailure(ctx, errBinary)
					return nil, errBinary
				}
				// No window check here: ReadMessage only ever returns text or binary, and binary
				// returned just above, so nothing reaches this line. The empty-payload skip below
				// is the reachable one and does consult the window.
				continue
			}

			payload = bytes.TrimSpace(payload)
			if len(payload) == 0 {
				if !windowOpen {
					break
				}
				continue
			}
			observeCodexTokenEvent(reporter, payload)
			helps.AppendCodexAPIWebsocketResponse(ctx, e.cfg, payload)
			helps.EmitWebSocketResponseEvent(ctx, opts, auth, e.Identifier(), req.Model, payload)
			payload = helps.RestoreCodexMultiAgentV2Response(payload, restoreMultiAgentV2)

			if wsErr, ok := parseCodexWebsocketErrorWithCooling(payload, e.modelLevelCooling()); ok {
				if sess != nil {
					if timeoutReached && len(bufferedChunks) > 0 {
						e.invalidateUpstreamConn(sess, conn, "upstream_error", wsErr)
					} else {
						e.invalidateUpstreamConnWithoutDisconnectNotify(sess, conn, "upstream_error", wsErr)
					}
					sess.clearActive(conn, readCh)
					unlockStreamSession()
				} else {
					logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, "upstream_error", wsErr)
					_ = closer.Close()
				}
				if errClearReplay := clearCodexReasoningReplayOnWebsocketError(ctx, replayScope, payload); errClearReplay != nil {
					if isEphemeralSession {
						closeCodexWebsocketSession(sess, "replay_clear_error")
					}
					helps.RecordAPIWebsocketError(ctx, e.cfg, "replay_clear_error", errClearReplay)
					reporter.PublishFailure(ctx, errClearReplay)
					return nil, errClearReplay
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", wsErr)
				reporter.PublishFailure(ctx, wsErr)
				if timeoutReached {
					helps.LogWithRequestID(ctx).Debugf("codex websockets executor: bootstrap error after %d messages read / %v, time budget exhausted; delivering in-stream", bufferedFrames, timeSinceStart)
					bootstrapTerminalErr = wsErr
					break
				}
				if isEphemeralSession {
					closeCodexWebsocketSession(sess, "upstream_error")
				}
				return nil, wsErr
			}
			if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(payload, e.modelLevelCooling()); ok {
				// A transient capacity or usage-limit rejection during bootstrap is retried on
				// another credential, so the downstream websocket session must survive this
				// upstream teardown. Notifying the disconnect here (or when no buffered payload
				// chunk precedes the error) would close the client connection before the retry
				// can deliver anything.
				overloadFailover := isCodexOverloadBootstrapFailure(terminalBody)
				usageLimitFailover := isCodexUsageLimitError(terminalBody)
				failoverPending := overloadFailover || usageLimitFailover
				if failoverPending && timeoutReached {
					failoverPending = false
					helps.LogWithRequestID(ctx).Debugf("codex websockets executor: bootstrap overload rejection after %d messages read / %v, time budget exhausted; delivering in-stream", bufferedFrames, timeSinceStart)
				}
				notifyDisconnect := !failoverPending && len(bufferedChunks) > 0
				if sess != nil {
					if notifyDisconnect {
						e.invalidateUpstreamConn(sess, conn, "terminal_failure", streamErr)
					} else {
						e.invalidateUpstreamConnWithoutDisconnectNotify(sess, conn, "terminal_failure", streamErr)
					}
					sess.clearActive(conn, readCh)
					unlockStreamSession()
				} else {
					logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, "terminal_failure", streamErr)
					_ = closer.Close()
				}
				if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
					if isEphemeralSession {
						closeCodexWebsocketSession(sess, "replay_clear_error")
					}
					helps.RecordAPIWebsocketError(ctx, e.cfg, "replay_clear_error", errClearReplay)
					reporter.PublishFailure(ctx, errClearReplay)
					return nil, errClearReplay
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", streamErr)
				reporter.PublishFailure(ctx, streamErr)
				if failoverPending {
					if isEphemeralSession {
						closeCodexWebsocketSession(sess, "bootstrap_overload")
					}
					// Fail the attempt before the downstream headers are committed so the
					// conductor can transparently retry on another credential, and report the
					// status the upstream refused to put on the wire.
					helps.LogWithRequestID(ctx).Debugf("codex websockets executor: bootstrap overload rejection after %d messages read, failing over", bufferedFrames)
					if overloadFailover {
						return nil, newCodexBootstrapOverloadErr(terminalBody)
					}
					return nil, streamErr
				}
				bootstrapTerminalErr = streamErr
				break
			}

			eventType := gjson.GetBytes(payload, "type").String()
			isTerminalEvent := eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" || eventType == "response.failed" || eventType == "error"
			if helps.HasMeaningfulCodexOutputDelta(payload) {
				sawOutputDelta = true
			}
			if helps.IsCodexTerminalEmptyIncomplete(payload, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
				streamErr := newCodexEmptyIncompleteStreamError()
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", streamErr)
				reporter.PublishFailure(ctx, streamErr)
				if sess != nil {
					e.invalidateUpstreamConn(sess, conn, "terminal_empty_incomplete", streamErr)
					sess.clearActive(conn, readCh)
					unlockStreamSession()
				} else if closer != nil {
					_ = closer.Close()
				}
				bootstrapTerminalErr = streamErr
				break
			}
			if eventType == "response.output_item.done" {
				collectCodexOutputItemDone(payload, outputItemsByIndex, &outputItemsFallback)
			}
			completedPayload := payload
			if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
				completedPayload = normalizeCodexWebsocketCompletion(completedPayload)
				if !preserveNativeOutput {
					completedPayload = patchCodexCompletedOutput(completedPayload, outputItemsByIndex, outputItemsFallback)
				}
				if eventType != "response.incomplete" {
					cacheCodexReasoningReplayFromCompleted(replayScope, completedPayload)
				}
				if detail, ok := helps.ParseCodexUsageWithOutputItems(completedPayload, outputItemsByIndex, outputItemsFallback); ok {
					reporter.Publish(ctx, detail)
				} else {
					reporter.EnsurePublished(ctx)
				}
				publishCodexImageToolUsageWithOutputItems(ctx, reporter, clientBody, completedPayload, outputItemsByIndex, outputItemsFallback)
			}

			var currentChunks [][]byte
			if cliproxyexecutor.DownstreamWebsocket(ctx) {
				if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
					payload = completedPayload
				}
				downstreamPayload := helps.EnsureResponsesUsageDetails(payload)
				currentChunks = [][]byte{downstreamPayload}
			} else {
				payload = normalizeCodexWebsocketCompletion(payload)
				if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
					payload = completedPayload
				}
				line := encodeCodexWebsocketAsSSE(payload)
				currentChunks = helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, clientBody, line, &param, claudeInputTokens)
			}

			// !isTerminalEvent is redundant against the closed allow-list, which admits no terminal
			// type, and the empty-payload rule cannot fire on a payload already known non-empty. It
			// stays as the guard a reader expects to find, and its SSE counterpart is !terminalSuccess.
			if windowOpen && isCodexBootstrapBufferableEvent(eventType, payload) && !isTerminalEvent {
				frameBytes := len(payload)
				for i := range currentChunks {
					frameBytes += len(currentChunks[i])
				}
				if bufferedBytes+frameBytes <= codexBootstrapMaxBufferedBytes {
					bufferedBytes += frameBytes
					bufferedChunks = append(bufferedChunks, currentChunks...)
					continue
				}
				helps.LogWithRequestID(ctx).Debugf("codex websockets executor: bootstrap byte limit reached after %d messages / %d bytes, releasing stream without overload probing", bufferedFrames, bufferedBytes)
			}

			initialChunks = currentChunks
			if isTerminalEvent {
				immediateTerminal = true
				immediateIncomplete = eventType == "response.incomplete"
			}
			break
		}
	}

	chanCapacity := len(bufferedChunks) + len(initialChunks)
	if bootstrapTerminalErr != nil || (immediateIncomplete && len(initialChunks) == 0) {
		chanCapacity++
	}
	out := make(chan cliproxyexecutor.StreamChunk, chanCapacity)
	for _, chunk := range bufferedChunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	for _, chunk := range initialChunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk, Incomplete: immediateIncomplete}
	}
	if immediateIncomplete && len(initialChunks) == 0 {
		out <- cliproxyexecutor.StreamChunk{Incomplete: true}
	}
	if bootstrapTerminalErr != nil {
		if isEphemeralSession {
			closeCodexWebsocketSession(sess, "bootstrap_terminal_error")
		}
		// The upstream connection was already invalidated and released in the terminal-failure
		// branch above, so only the buffered payloads plus the in-stream error remain to emit.
		out <- cliproxyexecutor.StreamChunk{Err: bootstrapTerminalErr}
		close(out)
		return &cliproxyexecutor.StreamResult{Headers: upstreamHeaders, Chunks: out}, nil
	}
	if immediateTerminal {
		if sess != nil {
			sess.clearActive(conn, readCh)
			unlockStreamSession()
			if isEphemeralSession {
				closeCodexWebsocketSession(sess, "completed")
			}
		} else {
			logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, "completed", nil)
			if errClose := closer.Close(); errClose != nil {
				log.Errorf("codex websockets executor: close websocket error: %v", errClose)
			}
		}
		close(out)
		return &cliproxyexecutor.StreamResult{Headers: upstreamHeaders, Chunks: out}, nil
	}

	emittedPayload := len(bufferedChunks) > 0 || len(initialChunks) > 0

	go func() {
		terminateReason := "completed"
		var terminateErr error

		defer close(out)
		defer func() {
			if sess != nil {
				sess.clearActive(conn, readCh)
				unlockStreamSession()
				if isEphemeralSession {
					closeCodexWebsocketSession(sess, terminateReason)
				}
				return
			}
			logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, terminateReason, terminateErr)
			if errClose := closer.Close(); errClose != nil {
				log.Errorf("codex websockets executor: close websocket error: %v", errClose)
			}
		}()

		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			if ctx == nil {
				out <- chunk
				return true
			}
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for {
			if ctx != nil && ctx.Err() != nil {
				terminateReason = "context_done"
				terminateErr = ctx.Err()
				_ = send(cliproxyexecutor.StreamChunk{Err: ctx.Err()})
				return
			}
			msgType, payload, errRead := readCodexWebsocketMessage(ctx, sess, conn, readCh)
			if errRead != nil {
				if sess != nil && ctx != nil && ctx.Err() != nil {
					terminateReason = "context_done"
					terminateErr = ctx.Err()
					_ = send(cliproxyexecutor.StreamChunk{Err: ctx.Err()})
					return
				}
				mappedErr := codexWebsocketTurnEventID(mapCodexWebsocketReadError(errRead), originalPayload)
				terminateReason = "read_error"
				terminateErr = mappedErr
				helps.RecordAPIWebsocketError(ctx, e.cfg, "read", mappedErr)
				reporter.PublishFailure(ctx, mappedErr)
				_ = send(cliproxyexecutor.StreamChunk{Err: mappedErr})
				return
			}
			if msgType != websocket.TextMessage {
				if msgType == websocket.BinaryMessage {
					unexpectedErr := fmt.Errorf("codex websockets executor: unexpected binary message")
					terminateReason = "unexpected_binary"
					terminateErr = unexpectedErr
					helps.RecordAPIWebsocketError(ctx, e.cfg, "unexpected_binary", unexpectedErr)
					reporter.PublishFailure(ctx, unexpectedErr)
					if sess != nil {
						e.invalidateUpstreamConn(sess, conn, "unexpected_binary", unexpectedErr)
					}
					_ = send(cliproxyexecutor.StreamChunk{Err: unexpectedErr})
					return
				}
				continue
			}

			payload = bytes.TrimSpace(payload)
			if len(payload) == 0 {
				continue
			}
			observeCodexTokenEvent(reporter, payload)
			helps.AppendCodexAPIWebsocketResponse(ctx, e.cfg, payload)
			helps.EmitWebSocketResponseEvent(ctx, opts, auth, e.Identifier(), req.Model, payload)
			payload = helps.RestoreCodexMultiAgentV2Response(payload, restoreMultiAgentV2)

			if wsErr, ok := parseCodexWebsocketErrorWithCooling(payload, e.modelLevelCooling()); ok {
				terminateReason = "upstream_error"
				terminateErr = wsErr
				// The disconnect notification makes the downstream handler close the client and
				// cancel the request context, so it must follow delivery of the error to the
				// conductor; otherwise the cancellation can drop the error and its cooldown.
				// When no payload chunk has been emitted yet, the conductor may still fail over
				// to another credential on the same downstream websocket, so do not notify disconnect.
				if sess != nil {
					e.invalidateUpstreamConnWithoutDisconnectNotify(sess, conn, "upstream_error", wsErr)
					sess.clearActive(conn, readCh)
					unlockStreamSession()
				}
				if errClearReplay := clearCodexReasoningReplayOnWebsocketError(ctx, replayScope, payload); errClearReplay != nil {
					terminateErr = errClearReplay
					helps.RecordAPIWebsocketError(ctx, e.cfg, "replay_clear_error", errClearReplay)
					reporter.PublishFailure(ctx, errClearReplay)
					_ = send(cliproxyexecutor.StreamChunk{Err: errClearReplay})
					if emittedPayload {
						sess.notifyUpstreamDisconnect(wsErr)
					}
					return
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", wsErr)
				reporter.PublishFailure(ctx, wsErr)
				_ = send(cliproxyexecutor.StreamChunk{Err: wsErr})
				if emittedPayload {
					sess.notifyUpstreamDisconnect(wsErr)
				}
				return
			}
			if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(payload, e.modelLevelCooling()); ok {
				terminateReason = "upstream_error"
				terminateErr = streamErr
				// Publish the downstream disconnect only after an in-stream error was delivered,
				// see the upstream error branch above.
				if sess != nil {
					e.invalidateUpstreamConnWithoutDisconnectNotify(sess, conn, "terminal_failure", streamErr)
					sess.clearActive(conn, readCh)
					unlockStreamSession()
				}
				if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
					terminateErr = errClearReplay
					helps.RecordAPIWebsocketError(ctx, e.cfg, "replay_clear_error", errClearReplay)
					reporter.PublishFailure(ctx, errClearReplay)
					_ = send(cliproxyexecutor.StreamChunk{Err: errClearReplay})
					if emittedPayload {
						sess.notifyUpstreamDisconnect(streamErr)
					}
					return
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", streamErr)
				reporter.PublishFailure(ctx, streamErr)
				_ = send(cliproxyexecutor.StreamChunk{Err: streamErr})
				if emittedPayload {
					sess.notifyUpstreamDisconnect(streamErr)
				}
				return
			}

			eventType := gjson.GetBytes(payload, "type").String()
			isTerminalEvent := eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" || eventType == "response.failed" || eventType == "error"
			if helps.HasMeaningfulCodexOutputDelta(payload) {
				sawOutputDelta = true
			}
			if helps.IsCodexTerminalEmptyIncomplete(payload, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
				streamErr := newCodexEmptyIncompleteStreamError()
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				reporter.PublishFailure(ctx, streamErr)
				if sess != nil {
					e.invalidateUpstreamConn(sess, conn, "terminal_empty_incomplete", streamErr)
					unlockStreamSession()
				}
				_ = send(cliproxyexecutor.StreamChunk{Err: streamErr})
				terminateReason = "terminal_empty_incomplete"
				terminateErr = streamErr
				return
			}
			if eventType == "response.output_item.done" {
				collectCodexOutputItemDone(payload, outputItemsByIndex, &outputItemsFallback)
			}
			completedPayload := payload
			if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
				completedPayload = normalizeCodexWebsocketCompletion(completedPayload)
				if !preserveNativeOutput {
					completedPayload = patchCodexCompletedOutput(completedPayload, outputItemsByIndex, outputItemsFallback)
				}
				if eventType != "response.incomplete" {
					cacheCodexReasoningReplayFromCompleted(replayScope, completedPayload)
				}
				if detail, ok := helps.ParseCodexUsageWithOutputItems(completedPayload, outputItemsByIndex, outputItemsFallback); ok {
					reporter.Publish(ctx, detail)
				} else {
					reporter.EnsurePublished(ctx)
				}
				publishCodexImageToolUsageWithOutputItems(ctx, reporter, clientBody, completedPayload, outputItemsByIndex, outputItemsFallback)
			}

			if cliproxyexecutor.DownstreamWebsocket(ctx) {
				if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
					payload = completedPayload
				}
				downstreamPayload := helps.EnsureResponsesUsageDetails(payload)
				if !send(cliproxyexecutor.StreamChunk{Payload: downstreamPayload, Incomplete: eventType == "response.incomplete"}) {
					terminateReason = "context_done"
					terminateErr = ctx.Err()
					return
				}
				emittedPayload = true
				if isTerminalEvent {
					return
				}
				continue
			}

			payload = normalizeCodexWebsocketCompletion(payload)
			if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
				payload = completedPayload
			}
			eventType = gjson.GetBytes(payload, "type").String()
			isIncomplete := eventType == "response.incomplete"
			line := encodeCodexWebsocketAsSSE(payload)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, clientBody, line, &param, claudeInputTokens)
			for i := range chunks {
				if !send(cliproxyexecutor.StreamChunk{Payload: chunks[i], Incomplete: isIncomplete}) {
					terminateReason = "context_done"
					terminateErr = ctx.Err()
					return
				}
				emittedPayload = true
			}
			if isIncomplete && len(chunks) == 0 {
				if !send(cliproxyexecutor.StreamChunk{Incomplete: true}) {
					terminateReason = "context_done"
					terminateErr = ctx.Err()
					return
				}
			}
			if isTerminalEvent || eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
				return
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: upstreamHeaders, Chunks: out}, nil
}

// codexWebsocketPrepared contains the request pipeline output shared by the
// ordinary per-response stream and subsequent creates on a duplex connection.
type codexWebsocketPrepared struct {
	from                 sdktranslator.Format
	responseFormat       sdktranslator.Format
	to                   sdktranslator.Format
	preserveNativeOutput bool
	originalPayload      []byte
	clientBody           []byte
	wsURL                string
	wsHeaders            http.Header
	useSIWC              bool
	replayScope          codexReasoningReplayScope
	optimizeMultiAgentV2 bool
	multiAgentV2Conflict bool
}

func (e *CodexWebsocketsExecutor) prepareCodexWebsocketStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*codexWebsocketPrepared, error) {
	if errExpand := helps.ExpandRecognizedResponsesCompactionCapsules(&req, &opts); errExpand != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	apiKey, baseURL := codexCredsForRequest(ctx, auth, opts.Headers)
	if errSIWC := ensureCodexSIWCCredentialsForRequest(ctx, auth, apiKey, opts.Headers); errSIWC != nil {
		return nil, errSIWC
	}
	useSIWC := codexUseSIWCForRequest(ctx, auth, opts.Headers)
	if baseURL == "" {
		if useSIWC {
			baseURL = defaultOpenAISIWCBaseURL
		} else {
			baseURL = "https://chatgpt.com/backend-api/codex"
		}
	}
	var err error
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	preserveNativeOutput := helps.IsNativeCodexRequest(req.Payload, opts)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := e.resolveCodexModelIsCompat(auth, req, baseModel)
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	originalTranslated, body, updatesChanged, err := translateCodexRequestPairWithUpdateIntent(from, to, targetModel, originalPayload, req.Payload, true, isCompat)
	if err != nil {
		return nil, err
	}
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)
	body = helps.SetStringIfDifferent(body, "model", baseModel)

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged)
	if err != nil {
		return nil, err
	}

	hasExplicitInstructions := gjson.GetBytes(req.Payload, "instructions").Exists() && strings.TrimSpace(gjson.GetBytes(req.Payload, "instructions").String()) != ""
	finalizePayload := helps.NewPayloadFinalizer(e.cfg, "codex-websockets", baseModel, to.String(), "", originalTranslated, req, opts)
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = ensureWebSearchToolForHistory(body, opts.Headers)
	if !useSIWC {
		body = normalizeCodexInstructions(body, preserveNativeOutput)
		if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
			body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
		}
	}
	body = normalizeCodexWebsocketParallelToolCalls(body, opts.Headers)
	body = helps.NormalizeCodexToolSchemas(body)
	multiAgentV2Conflict := helps.HasCodexMultiAgentV2NamespaceConflict(body)
	body, optimizeMultiAgentV2 := helps.OptimizeCodexMultiAgentV2RequestForAuth(ctx, opts.Headers, body, e.cfg, auth, isCompat)
	body, replayScope, errReplay := applyCodexReasoningReplayCacheRequired(ctx, from, req, opts, body)
	if errReplay != nil {
		return nil, errReplay
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(ctx, "codex websockets executor", body, isCompat, targetModel, baseURL)

	httpURL := strings.TrimSuffix(baseURL, "/") + "/responses"
	wsURL, err := buildCodexResponsesWebsocketURL(httpURL)
	if err != nil {
		return nil, err
	}

	body, wsHeaders, errPromptCache := applyCodexPromptCacheHeadersWithContext(ctx, from, req, body, opts.Headers)
	if errPromptCache != nil {
		return nil, errPromptCache
	}
	body = helps.SanitizeCodexInputItemIDs(body)
	if useSIWC {
		body = applyCodexSIWCWebsocketBodyRules(body, hasExplicitInstructions, cliproxyexecutor.Options{KeepTopLevelEffort: thinking.HasThinkingSuffix(req.Model)})
	}
	body = finalizePayload(body)
	wsHeaders = applyCodexWebsocketHeaders(ctx, wsHeaders, auth, apiKey, e.cfg, preserveNativeOutput, opts.Headers)
	applyCodexRoutingHint(ctx, wsHeaders, auth, baseModel, body, opts.Headers)
	applyModelHeaderOverrides(wsHeaders, baseModel)

	return &codexWebsocketPrepared{
		from:                 from,
		responseFormat:       responseFormat,
		to:                   to,
		preserveNativeOutput: preserveNativeOutput,
		originalPayload:      originalPayload,
		clientBody:           body,
		wsURL:                wsURL,
		wsHeaders:            wsHeaders,
		useSIWC:              useSIWC,
		replayScope:          replayScope,
		optimizeMultiAgentV2: optimizeMultiAgentV2,
		multiAgentV2Conflict: multiAgentV2Conflict,
	}, nil
}
