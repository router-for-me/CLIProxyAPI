package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// codexDuplexConnectionError keeps a connection failure from cooling the shared
// credential. The socket still fails visibly; no reconnect or replay is attempted.
// Auth/quota failures from the initial handshake retain the normal policy.
type codexDuplexConnectionError struct{ cause error }

func (e *codexDuplexConnectionError) Error() string         { return e.cause.Error() }
func (e *codexDuplexConnectionError) Unwrap() error         { return e.cause }
func (e *codexDuplexConnectionError) IsRequestScoped() bool { return true }

// streamCodexDuplex owns the already authenticated socket until downstream
// disconnect. A response terminal event is not a connection terminal event:
// accepted steering may produce a successor or wait for client tool results.
// There is no redial, credential selection, local acknowledgement, or replay
// in this path. Only the upstream can take ownership of a steering submission.
func (e *CodexWebsocketsExecutor) streamCodexDuplex(
	ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options,
	sess *codexWebsocketSession, conn *websocket.Conn, readCh chan codexWebsocketRead,
	input <-chan cliproxyexecutor.WebsocketInput, initial *codexWebsocketPrepared,
	initialReporter *helps.UsageReporter, headers http.Header, unlock func(),
) *cliproxyexecutor.StreamResult {
	streamCtx, cancel := context.WithCancel(ctx)
	out := make(chan cliproxyexecutor.StreamChunk)
	// This first frame was successfully written by ExecuteStream before handoff.
	log.Infof("codex websockets: request forwarded session=%s auth=%s url=%s event=response.create", sess.sessionID, auth.ID, initial.wsURL)
	writeErrors := make(chan error, 1)
	// Explicit creates have their own request settings. Automatic successors
	// inherit the preceding response settings, just as they do upstream.
	var metadataMu sync.Mutex
	pending := []*codexWebsocketPrepared{initial}
	// Do not consume follow-ups until bootstrap succeeds. A rejected initial
	// request may retry on another credential using the same input channel.
	inputReady := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		fail := func(err error) {
			select {
			case writeErrors <- err:
			default:
			}
			cancel()
		}
		reject := func(message string) bool {
			payload, _ := json.Marshal(map[string]any{
				"type": "error", "status": http.StatusBadRequest,
				"error": map[string]string{"type": "invalid_request_error", "message": message},
			})
			// Reader cleanup joins this writer before closing out. Cancellation must
			// also release a local error blocked behind a disconnected downstream.
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: payload}:
				return true
			case <-streamCtx.Done():
				return false
			}
		}
		select {
		case <-streamCtx.Done():
			return
		case <-inputReady:
		}
		for {
			select {
			case <-streamCtx.Done():
				return
			case message, ok := <-input:
				if !ok {
					fail(context.Canceled)
					return
				}
				if message.Err != nil {
					fail(message.Err)
					return
				}
				if !cliproxyexecutor.WebsocketAuthEnabled(streamCtx, auth.ID) {
					// A fresh client connection can select an enabled credential. Do not send
					// this frame on a disabled account or replay it on a different socket.
					fail(fmt.Errorf("websocket credential is no longer enabled"))
					return
				}
				payload := message.Payload
				if !json.Valid(payload) {
					if !reject("invalid websocket request JSON") {
						return
					}
					continue
				}
				switch gjson.GetBytes(payload, "type").String() {
				case "response.steer":
					// Control frames bypass ALL response.create translations and defaults.
					// Unknown fields and unsupported input are left to upstream validation.
				case "response.create", "response.append":
					model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
					if model != "" && model != req.Model && model != gjson.GetBytes(initial.originalPayload, "model").String() {
						fail(cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError())
						return
					}
					nextReq, nextOpts := req, opts
					if model == "" {
						payload, _ = sjson.SetBytes(payload, "model", req.Model)
					}
					nextReq.Payload = payload
					nextOpts.OriginalRequest = payload
					prepared, errPrepare := e.prepareCodexWebsocketStream(streamCtx, auth, nextReq, nextOpts)
					if errPrepare != nil {
						fail(errPrepare)
						return
					}
					if prepared.wsURL != initial.wsURL {
						fail(cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError())
						return
					}
					payload = buildCodexWebsocketRequestBody(prepared.upstreamBody)
					metadataMu.Lock()
					if len(pending) >= 16 {
						metadataMu.Unlock()
						fail(fmt.Errorf("too many outstanding response.create requests"))
						return
					}
					pending = append(pending, prepared)
					metadataMu.Unlock()
					if prepared.optimizeMultiAgentV2 || prepared.multiAgentV2Conflict {
						sess.setMultiAgentV2Optimized(conn, prepared.optimizeMultiAgentV2 && !prepared.multiAgentV2Conflict)
					}
				default:
					if !reject(fmt.Sprintf("unsupported websocket request type: %s", gjson.GetBytes(payload, "type").String())) {
						return
					}
					continue
				}
				helps.RecordAPIWebsocketRequest(streamCtx, e.cfg, helps.UpstreamRequestLog{
					URL: initial.wsURL, Method: "WEBSOCKET", Body: payload, Provider: e.Identifier(), AuthID: auth.ID,
				})
				if errWrite := writeCodexWebsocketMessage(sess, conn, payload); errWrite != nil {
					fail(mapCodexWebsocketWriteError(sess, conn, errWrite))
					return
				}
				log.Infof("codex websockets: request forwarded session=%s auth=%s url=%s event=%s", sess.sessionID, auth.ID, initial.wsURL, gjson.GetBytes(payload, "type").String())
			}
		}
	}()
	go func() {
		defer close(out)
		defer func() {
			cancel()
			// Close releases a writer blocked in the network, then join it before
			// releasing the execution session. No goroutine outlives its socket.
			e.invalidateUpstreamConnWithoutDisconnectNotify(sess, conn, "duplex_closed", nil)
			<-writerDone
			sess.clearActive(conn, readCh)
			unlock()
		}()
		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		current := initial
		reporter := initialReporter
		firstResponse := true
		responseID := ""
		responseActive := false
		outputItems := make(map[int64][]byte)
		var outputFallback [][]byte
		for {
			kind, payload, errRead := readCodexWebsocketMessage(streamCtx, sess, conn, readCh)
			if errRead != nil {
				select {
				case errWrite := <-writeErrors:
					errRead = errWrite
				default:
				}
				if ctx.Err() == nil {
					connectionErr := &codexDuplexConnectionError{cause: errRead}
					reporter.PublishFailure(ctx, connectionErr)
					send(cliproxyexecutor.StreamChunk{Err: connectionErr})
				}
				return
			}
			if kind != websocket.TextMessage {
				continue
			}
			payload = bytes.TrimSpace(payload)
			eventType := gjson.GetBytes(payload, "type").String()
			establishing := firstResponse && eventType == "response.created"
			if eventType == "response.created" {
				metadataMu.Lock()
				if len(pending) > 0 {
					current, pending = pending[0], pending[1:]
				}
				metadataMu.Unlock()
				if !firstResponse {
					reporter = helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
					reporter.SetTranslatedReasoningEffort(current.clientBody, current.to.String())
					reporter.StartResponseTTFT()
				}
				firstResponse = false
				responseID = gjson.GetBytes(payload, "response.id").String()
				responseActive = true
				outputItems = make(map[int64][]byte)
				outputFallback = nil
			}
			observeCodexTokenEvent(reporter, payload)
			helps.AppendCodexAPIWebsocketResponse(ctx, e.cfg, payload)
			helps.EmitWebSocketResponseEvent(ctx, opts, auth, e.Identifier(), req.Model, payload)
			// Steering acknowledgements, pending notifications and failures are opaque:
			// preserve their IDs, input, sequence numbers and event types byte-for-byte.
			if strings.HasPrefix(eventType, "response.steer.") {
				if !send(cliproxyexecutor.StreamChunk{Payload: payload}) {
					return
				}
				continue
			}
			eventPrepared, eventReporter := current, reporter
			if !firstResponse && (eventType == "response.failed" || eventType == "error") {
				failedID := gjson.GetBytes(payload, "response.id").String()
				if failedID == "" {
					failedID = gjson.GetBytes(payload, "response_id").String()
				}
				metadataMu.Lock()
				// A failure for the running response must not consume a queued create.
				// A rejection before response.created instead owns the oldest pending
				// create, including its identity mapping and reasoning replay scope.
				currentFailure := failedID != "" && failedID == responseID
				ambiguous := len(pending) > 0 && responseActive && failedID == ""
				if len(pending) > 0 && !currentFailure && !ambiguous {
					eventPrepared, pending = pending[0], pending[1:]
					eventReporter = helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
					eventReporter.SetTranslatedReasoningEffort(eventPrepared.clientBody, eventPrepared.to.String())
				} else if !ambiguous {
					responseActive = false
				}
				metadataMu.Unlock()
				if ambiguous {
					// Without a response ID, assigning this failure could corrupt
					// either request. Preserve the event and fail the socket without
					// guessing a scope, replaying input, or cooling the credential.
					connectionErr := &codexDuplexConnectionError{cause: fmt.Errorf("cannot associate websocket failure with a response or pending create")}
					reporter.PublishFailure(ctx, connectionErr)
					if send(cliproxyexecutor.StreamChunk{Payload: payload}) {
						send(cliproxyexecutor.StreamChunk{Err: connectionErr})
					}
					return
				}
			}
			payload = applyCodexIdentityConfuseResponsePayload(payload, eventPrepared.identityState)
			restoreMultiAgent := !eventPrepared.multiAgentV2Conflict && (eventPrepared.optimizeMultiAgentV2 || sess.isMultiAgentV2Optimized(conn))
			payload = helps.RestoreCodexMultiAgentV2Response(payload, restoreMultiAgent)
			// Parse and invalidate replay for every rejected request, using the
			// metadata that belongs to this event. Only the first rejection can
			// enter conductor bootstrap retry; later failures stay on this socket.
			var terminalErr, replayErr error
			if wsErr, ok := parseCodexWebsocketErrorWithCooling(payload, e.modelLevelCooling()); ok {
				terminalErr = wsErr
				replayErr = clearCodexReasoningReplayOnWebsocketError(ctx, eventPrepared.replayScope, payload)
			} else if streamErr, body, ok := codexTerminalFailureErrWithCooling(payload, e.modelLevelCooling()); ok {
				terminalErr = streamErr
				replayErr = clearCodexReasoningReplayOnInvalidSignature(ctx, eventPrepared.replayScope, streamErr.StatusCode(), body)
			}
			if replayErr != nil {
				helps.RecordAPIWebsocketError(ctx, e.cfg, "replay_clear_error", replayErr)
				eventReporter.PublishFailure(ctx, replayErr)
				send(cliproxyexecutor.StreamChunk{Err: replayErr})
				return
			}
			if terminalErr != nil {
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", terminalErr)
				eventReporter.PublishFailure(ctx, terminalErr)
				if firstResponse {
					send(cliproxyexecutor.StreamChunk{Err: terminalErr})
					return
				}
			}
			if eventType == "response.output_item.done" {
				collectCodexOutputItemDone(payload, outputItems, &outputFallback)
			}
			if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
				responseActive = false
				payload = normalizeCodexWebsocketCompletion(payload)
				if !current.preserveNativeOutput {
					payload = patchCodexCompletedOutput(payload, outputItems, outputFallback)
				}
				if eventType != "response.incomplete" {
					cacheCodexReasoningReplayFromCompleted(current.replayScope, payload)
				}
				if detail, ok := helps.ParseCodexUsage(payload); ok {
					reporter.Publish(ctx, detail)
				} else {
					reporter.EnsurePublished(ctx)
				}
			}
			payload = applyCodexIdentityExposeResponsePayload(payload, eventPrepared.identityState)
			if !send(cliproxyexecutor.StreamChunk{Payload: helps.EnsureResponsesUsageDetails(payload)}) {
				return
			}
			if establishing {
				// Deliver response.created before any locally generated error so the
				// downstream handler also observes successful bootstrap first.
				close(inputReady)
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
}
