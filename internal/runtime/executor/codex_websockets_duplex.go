package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// Serialize explicit creates against steering continuations. A response.created
	// alone does not identify whether upstream created it automatically.
	var unacknowledgedSteers []string
	acceptedSteers := make(map[string]string)
	current := initial
	responseID := ""
	responseSettings := make(map[string]*codexWebsocketPrepared)
	steeringSettings := make(map[string]*codexWebsocketPrepared)
	var responseOrder []string
	releaseSteeringSettings := func(parent string) {
		for _, target := range unacknowledgedSteers {
			if target == parent {
				return
			}
		}
		for _, target := range acceptedSteers {
			if target == parent {
				return
			}
		}
		delete(steeringSettings, parent)
	}
	waitingParent := ""
	automaticActive := false
	stateChanged := make(chan struct{}, 1)
	wakeWriter := func() {
		select {
		case stateChanged <- struct{}{}:
		default:
		}
	}
	waitFor := func(ready func() bool) bool {
		for {
			metadataMu.Lock()
			ok := ready()
			metadataMu.Unlock()
			if ok {
				return streamCtx.Err() == nil
			}
			select {
			case <-stateChanged:
			case <-streamCtx.Done():
				return false
			}
		}
	}
	// Do not consume follow-ups until bootstrap succeeds. A rejected initial
	// request may retry on another credential using the same input channel.
	inputReady := make(chan struct{})
	writerDone := make(chan struct{})
	var pendingCreates [][]byte
	readyForCreate := func() bool {
		metadataMu.Lock()
		defer metadataMu.Unlock()
		if len(unacknowledgedSteers) > 0 || automaticActive {
			return false
		}
		for _, parent := range acceptedSteers {
			if parent != waitingParent {
				return false
			}
		}
		return true
	}
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
		processCreatePayload := func(payload []byte) bool {
			metadataMu.Lock()
			isAppend := gjson.GetBytes(payload, "type").String() == "response.append"
			prevID := strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String())
			if isAppend && prevID == "" && responseID != "" {
				prevID = responseID
				payload, _ = sjson.SetBytes(payload, "previous_response_id", prevID)
			}
			wrongParent := len(acceptedSteers) > 0 && prevID != waitingParent
			metadataMu.Unlock()
			if wrongParent {
				if !reject("response.create must continue the response waiting for required input") {
					return false
				}
				return true
			}
			model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
			if model != "" && model != req.Model && model != gjson.GetBytes(initial.originalPayload, "model").String() {
				fail(cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError())
				return false
			}
			if model == "" {
				model = req.Model
				if model == "" {
					model = strings.TrimSpace(gjson.GetBytes(initial.originalPayload, "model").String())
				}
				payload, _ = sjson.SetBytes(payload, "model", model)
			}
			if isAppend && !gjson.GetBytes(payload, "instructions").Exists() {
				metadataMu.Lock()
				parentSettings := responseSettings[prevID]
				metadataMu.Unlock()
				targetSettings := parentSettings
				if targetSettings == nil {
					targetSettings = initial
				}
				var inst gjson.Result
				if targetSettings != nil {
					inst = gjson.GetBytes(targetSettings.clientBody, "instructions")
					if !inst.Exists() {
						inst = gjson.GetBytes(targetSettings.originalPayload, "instructions")
					}
				}
				if !inst.Exists() && initial != nil {
					inst = gjson.GetBytes(initial.clientBody, "instructions")
					if !inst.Exists() {
						inst = gjson.GetBytes(initial.originalPayload, "instructions")
					}
				}
				if inst.Exists() {
					payload, _ = sjson.SetRawBytes(payload, "instructions", []byte(inst.Raw))
				}
			}
			nextReq, nextOpts := req, opts
			nextReq.Payload = payload
			nextOpts.OriginalRequest = payload
			prepared, errPrepare := e.prepareCodexWebsocketStream(streamCtx, auth, nextReq, nextOpts)
			if errPrepare != nil {
				fail(errPrepare)
				return false
			}
			if prepared.wsURL != initial.wsURL {
				fail(cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError())
				return false
			}
			payload = buildCodexWebsocketRequestBody(prepared.upstreamBody)
			metadataMu.Lock()
			if len(pending) >= 16 {
				metadataMu.Unlock()
				fail(fmt.Errorf("too many outstanding response.create requests"))
				return false
			}
			pending = append(pending, prepared)
			metadataMu.Unlock()
			if prepared.optimizeMultiAgentV2 || prepared.multiAgentV2Conflict {
				sess.setMultiAgentV2Optimized(conn, prepared.optimizeMultiAgentV2 && !prepared.multiAgentV2Conflict)
			}
			if !cliproxyexecutor.WebsocketAuthEnabled(streamCtx, auth.ID) {
				fail(fmt.Errorf("websocket credential is no longer enabled"))
				return false
			}
			helps.RecordAPIWebsocketRequest(streamCtx, e.cfg, helps.UpstreamRequestLog{
				URL: initial.wsURL, Method: "WEBSOCKET", Body: payload, Provider: e.Identifier(), AuthID: auth.ID,
			})
			if errWrite := writeCodexWebsocketMessage(sess, conn, payload); errWrite != nil {
				fail(mapCodexWebsocketWriteError(sess, conn, errWrite))
				return false
			}
			log.Infof("codex websockets: request forwarded session=%s auth=%s url=%s event=%s", sess.sessionID, auth.ID, initial.wsURL, gjson.GetBytes(payload, "type").String())
			return true
		}
		flushPendingCreates := func() bool {
			for len(pendingCreates) > 0 && readyForCreate() {
				item := pendingCreates[0]
				pendingCreates = pendingCreates[1:]
				if !processCreatePayload(item) {
					return false
				}
			}
			return true
		}
		select {
		case <-streamCtx.Done():
			return
		case <-inputReady:
		}
		for {
			if !flushPendingCreates() {
				return
			}
			select {
			case <-streamCtx.Done():
				return
			case <-stateChanged:
				if !flushPendingCreates() {
					return
				}
				continue
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
					parent := gjson.GetBytes(payload, "previous_response_id").String()
					metadataMu.Lock()
					settings := responseSettings[parent]
					metadataMu.Unlock()
					if settings == nil {
						if !waitFor(func() bool { return len(pending) == 0 }) {
							return
						}
						metadataMu.Lock()
						settings = responseSettings[parent]
						metadataMu.Unlock()
					}
					metadataMu.Lock()
					unacknowledgedSteers = append(unacknowledgedSteers, parent)
					if settings != nil {
						steeringSettings[parent] = settings
					}
					metadataMu.Unlock()
					// Control frames bypass ALL response.create translations and defaults.
					// Unknown fields and unsupported input are left to upstream validation.
					if !cliproxyexecutor.WebsocketAuthEnabled(streamCtx, auth.ID) {
						fail(fmt.Errorf("websocket credential is no longer enabled"))
						return
					}
					helps.RecordAPIWebsocketRequest(streamCtx, e.cfg, helps.UpstreamRequestLog{
						URL: initial.wsURL, Method: "WEBSOCKET", Body: payload, Provider: e.Identifier(), AuthID: auth.ID,
					})
					if errWrite := writeCodexWebsocketMessage(sess, conn, payload); errWrite != nil {
						fail(mapCodexWebsocketWriteError(sess, conn, errWrite))
						return
					}
					log.Infof("codex websockets: request forwarded session=%s auth=%s url=%s event=response.steer", sess.sessionID, auth.ID, initial.wsURL)
					continue

				case "response.create", "response.append":
					if len(pendingCreates) == 0 && readyForCreate() {
						if !processCreatePayload(payload) {
							return
						}
					} else {
						if len(pendingCreates) >= 16 {
							fail(fmt.Errorf("too many outstanding response.create requests"))
							return
						}
						pendingCreates = append(pendingCreates, payload)
					}
					continue

				default:
					if !reject(fmt.Sprintf("unsupported websocket request type: %s", gjson.GetBytes(payload, "type").String())) {
						return
					}
					continue
				}
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
		reporter := initialReporter
		firstResponse := true
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
				parent := gjson.GetBytes(payload, "response.previous_response_id").String()
				if parent == "" {
					parent = responseID
				}
				if !firstResponse && len(pending) == 0 {
					settings := steeringSettings[parent]
					if settings == nil {
						settings = responseSettings[parent]
					}
					if settings == nil {
						metadataMu.Unlock()
						connectionErr := &codexDuplexConnectionError{cause: fmt.Errorf("automatic successor has no retained parent settings")}
						reporter.PublishFailure(ctx, connectionErr)
						send(cliproxyexecutor.StreamChunk{Err: connectionErr})
						return
					}
					current = settings
				}
				for id, target := range acceptedSteers {
					if target == parent {
						delete(acceptedSteers, id)
					}
				}
				waitingParent = ""
				automaticActive = !firstResponse && len(pending) == 0
				if len(pending) > 0 {
					current, pending = pending[0], pending[1:]
				}
				responseID = gjson.GetBytes(payload, "response.id").String()
				// Retain response settings, not request history or authorization headers.
				// In-flight steering pins its parent's settings independently of this window.
				snapshot := *current
				snapshot.originalPayload, snapshot.upstreamBody, snapshot.wsHeaders = nil, nil, nil
				snapshot.clientBody = []byte("{}")
				if reasoning := gjson.GetBytes(current.clientBody, "reasoning"); reasoning.Exists() {
					snapshot.clientBody, _ = sjson.SetRawBytes(snapshot.clientBody, "reasoning", []byte(reasoning.Raw))
				}
				if inst := gjson.GetBytes(current.clientBody, "instructions"); inst.Exists() {
					snapshot.clientBody, _ = sjson.SetRawBytes(snapshot.clientBody, "instructions", []byte(inst.Raw))
				}
				responseSettings[responseID] = &snapshot
				responseOrder = append(responseOrder, responseID)
				if len(responseOrder) > 16 {
					delete(responseSettings, responseOrder[0])
					responseOrder = responseOrder[1:]
				}
				releaseSteeringSettings(parent)
				metadataMu.Unlock()
				wakeWriter()
				if !firstResponse {
					reporter = helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
					reporter.SetTranslatedReasoningEffort(current.clientBody, current.to.String())
					reporter.StartResponseTTFT()
				}
				firstResponse = false
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
				id := gjson.GetBytes(payload, "steer.id").String()
				parent := gjson.GetBytes(payload, "steer.previous_response_id").String()
				if parent == "" {
					parent = responseID
				}
				metadataMu.Lock()
				consumeSubmission := func() {
					for i, target := range unacknowledgedSteers {
						if target == parent || target == "" {
							unacknowledgedSteers = append(unacknowledgedSteers[:i], unacknowledgedSteers[i+1:]...)
							break
						}
					}
				}
				switch eventType {
				case "response.steer.accepted":
					consumeSubmission()
					acceptedSteers[id] = parent
				case "response.steer.failed":
					if _, accepted := acceptedSteers[id]; accepted {
						delete(acceptedSteers, id)
					} else {
						consumeSubmission()
					}
					releaseSteeringSettings(parent)
				case "response.steer.pending":
					// Tool results may already be waiting in the writer. No automatic
					// successor can start until an explicit continuation supplies them.
					waitingParent = parent
				}
				metadataMu.Unlock()
				wakeWriter()
				if !send(cliproxyexecutor.StreamChunk{Payload: payload}) {
					return
				}
				continue
			}
			if !firstResponse && (eventType == "error" || eventType == "response.failed") {
				var credentialErr error
				if wsErr, ok := parseCodexWebsocketErrorWithCooling(payload, e.modelLevelCooling()); ok {
					credentialErr = wsErr
				} else if streamErr, _, ok := codexTerminalFailureErrWithCooling(payload, e.modelLevelCooling()); ok {
					credentialErr = streamErr
				}
				var status interface{ StatusCode() int }
				if errors.As(credentialErr, &status) && (status.StatusCode() == http.StatusUnauthorized || status.StatusCode() == http.StatusForbidden || status.StatusCode() == http.StatusTooManyRequests) {
					// Account health is independent of which queued request failed.
					// The conductor records the original classification without replaying
					// this already-started stream on another credential.
					reporter.PublishFailure(ctx, credentialErr)
					if send(cliproxyexecutor.StreamChunk{Payload: payload}) {
						send(cliproxyexecutor.StreamChunk{Err: credentialErr})
					}
					return
				}
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
				ambiguous := failedID == "" && ((len(pending) > 0 && responseActive) || len(unacknowledgedSteers) > 0)
				if len(pending) > 0 && !currentFailure && !ambiguous {
					eventPrepared, pending = pending[0], pending[1:]
					eventReporter = helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
					eventReporter.SetTranslatedReasoningEffort(eventPrepared.clientBody, eventPrepared.to.String())
				} else if !ambiguous {
					responseActive = false
					automaticActive = false
				}
				metadataMu.Unlock()
				wakeWriter()
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
				metadataMu.Lock()
				automaticActive = false
				metadataMu.Unlock()
				wakeWriter()
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
