package controller

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	taskjsplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type pluginProtocolBridgeDeps struct {
	submit             func(*gin.Context, *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError)
	loadTask           func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error)
	now                func() time.Time
	admissions         *pluginProtocolObservationLimiter
	protocolLimits     relay.PluginProtocolLimits
	artifactContentURL func(taskID, artifactKey string) (string, error)
	submissionTimeout  time.Duration
	observationTimeout time.Duration
	loadTimeout        time.Duration
	tickInterval       time.Duration
	tickJitter         time.Duration
	heartbeatInterval  time.Duration
	admissionTimeout   time.Duration
	getByTaskId        func(int, string) (*model.Task, bool, error)
	resolvePlugin      func(constant.TaskPlatform) (*pluginruntime.LoadedPlugin, *pluginruntime.RoutingGeneration, bool)
	// imagePollInterval, pollTask and downloadImage serve the synchronous
	// OpenAI Images protocol: an asynchronous upstream image task is polled
	// inside the request, and b64_json responses download each image URL.
	imagePollInterval time.Duration
	pollTask          func(context.Context, *model.Task) error
	downloadImage     func(url string) (mimeType string, base64Data string, err error)
}

// taskPluginImagePollInterval paces the in-request polling of asynchronous
// image tasks. Legacy DashScope text-to-image jobs finish within tens of
// seconds, so a short interval keeps the synchronous response prompt.
const taskPluginImagePollInterval = 3 * time.Second

func defaultPluginProtocolBridgeDeps() pluginProtocolBridgeDeps {
	timeout := time.Duration(constant.TaskPluginProtocolTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	tick := time.Duration(constant.TaskPluginProtocolTickMilliseconds) * time.Millisecond
	if tick <= 0 {
		tick = 2 * time.Second
	}
	jitter := max(time.Duration(constant.TaskPluginProtocolTickJitterMilliseconds)*time.Millisecond, 0)
	heartbeat := time.Duration(constant.TaskPluginProtocolHeartbeatSeconds) * time.Second
	if heartbeat <= 0 {
		heartbeat = 15 * time.Second
	}
	loadTimeout := 5 * time.Second
	if halfHeartbeat := heartbeat / 2; halfHeartbeat > 0 && halfHeartbeat < loadTimeout {
		loadTimeout = halfHeartbeat
	}
	return pluginProtocolBridgeDeps{
		submit:             executeTaskSubmission,
		loadTask:           model.GetTaskForProtocolObservation,
		now:                time.Now,
		admissions:         pluginProtocolObservationAdmissions,
		protocolLimits:     relay.DefaultPluginProtocolLimits(),
		artifactContentURL: service.BuildTaskArtifactContentURL,
		submissionTimeout:  timeout,
		observationTimeout: timeout,
		loadTimeout:        loadTimeout,
		tickInterval:       tick,
		tickJitter:         jitter,
		heartbeatInterval:  heartbeat,
		admissionTimeout:   pluginruntime.DefaultCallTimeout,
		getByTaskId:        model.GetByTaskId,
		resolvePlugin:      resolveTaskPluginForProtocolRetrieve,
		imagePollInterval:  taskPluginImagePollInterval,
		pollTask:           pollTaskPluginImageTask,
		downloadImage:      service.GetImageFromUrl,
	}
}

func (d pluginProtocolBridgeDeps) withDefaults() pluginProtocolBridgeDeps {
	defaults := defaultPluginProtocolBridgeDeps()
	if d.submit == nil {
		d.submit = defaults.submit
	}
	if d.loadTask == nil {
		d.loadTask = defaults.loadTask
	}
	if d.now == nil {
		d.now = defaults.now
	}
	if d.admissions == nil {
		d.admissions = defaults.admissions
	}
	if d.artifactContentURL == nil {
		d.artifactContentURL = defaults.artifactContentURL
	}
	if d.submissionTimeout <= 0 {
		d.submissionTimeout = defaults.submissionTimeout
	}
	if d.observationTimeout <= 0 {
		d.observationTimeout = defaults.observationTimeout
	}
	if d.loadTimeout <= 0 {
		d.loadTimeout = defaults.loadTimeout
	}
	if d.tickInterval <= 0 {
		d.tickInterval = defaults.tickInterval
	}
	if d.tickJitter < 0 {
		d.tickJitter = 0
	}
	if d.heartbeatInterval <= 0 {
		d.heartbeatInterval = defaults.heartbeatInterval
	}
	if halfHeartbeat := d.heartbeatInterval / 2; halfHeartbeat > 0 && d.loadTimeout > halfHeartbeat {
		d.loadTimeout = halfHeartbeat
	}
	if d.admissionTimeout <= 0 {
		d.admissionTimeout = defaults.admissionTimeout
	}
	if d.getByTaskId == nil {
		d.getByTaskId = defaults.getByTaskId
	}
	if d.resolvePlugin == nil {
		d.resolvePlugin = defaults.resolvePlugin
	}
	if d.imagePollInterval <= 0 {
		d.imagePollInterval = defaults.imagePollInterval
	}
	if d.pollTask == nil {
		d.pollTask = defaults.pollTask
	}
	if d.downloadImage == nil {
		d.downloadImage = defaults.downloadImage
	}
	return d
}

func resolveTaskPluginForProtocolRetrieve(platform constant.TaskPlatform) (*pluginruntime.LoadedPlugin, *pluginruntime.RoutingGeneration, bool) {
	generation := pluginruntime.DefaultRegistry.Generation()
	plugin, ok := relay.ResolveTaskPluginForPlatform(generation, platform)
	return plugin, generation, ok
}

func serveTaskPluginProtocol(
	c *gin.Context,
	pinned pluginruntime.PinnedEndpoint,
	deps pluginProtocolBridgeDeps,
) {
	deps = deps.withDefaults()
	generation := uint64(0)
	if pinned.Generation != nil {
		generation = pinned.Generation.Number
	}
	pluginKey := ""
	if pinned.Plugin != nil {
		pluginKey = pinned.Plugin.Meta.Key
	}
	logger.LogDebug(
		c,
		"task_plugin subsystem=protocol event=bridge_start generation=%d plugin=%q protocol=%q model=%q",
		generation,
		pluginKey,
		pinned.Protocol,
		c.GetString("resolved_task_model"),
	)
	if !pluginruntime.SupportsHostProtocol(pinned.Protocol) {
		logger.LogDebug(c, "task_plugin subsystem=protocol event=bridge_rejected generation=%d plugin=%q reason=unsupported_protocol", generation, pluginKey)
		respondPluginProtocolError(c, http.StatusNotImplemented, "task_protocol_not_available", "Task protocol bridge is not available")
		return
	}
	requestValue, exists := c.Get(pluginruntime.ContextKeyProtocolRequest)
	protocolRequest, ok := requestValue.(pluginruntime.ProtocolRequestContext)
	if !exists || !ok || protocolRequest.Protocol != pinned.Protocol {
		logger.LogDebug(c, "task_plugin subsystem=protocol event=bridge_rejected generation=%d plugin=%q reason=invalid_protocol_context", generation, pluginKey)
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	if definition, known := pluginruntime.HostProtocol(pinned.Protocol); known && len(definition.DefinedModes()) > 0 && pinned.Plugin != nil {
		background := false
		if body, ok := protocolRequest.Body.(map[string]any); ok && body["kind"] == string(pluginruntime.BodyJSON) {
			if requestBody, ok := body["value"].(map[string]any); ok {
				background, _ = requestBody["background"].(bool)
			}
		}
		missing := false
		if protocolRequest.Stream && !pinned.Plugin.Meta.ProtocolSupports(pinned.Protocol, "stream") {
			missing = true
		}
		if background && !pinned.Plugin.Meta.ProtocolSupports(pinned.Protocol, "background") {
			missing = true
		}
		if !protocolRequest.Stream && !background && !pinned.Plugin.Meta.ProtocolSupports(pinned.Protocol, "sync") {
			missing = true
		}
		if missing {
			logger.LogError(c, "pinned task plugin does not support the requested protocol form")
			respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
			return
		}
	}
	logger.LogDebug(
		c,
		"task_plugin subsystem=protocol event=request_ready generation=%d plugin=%q protocol=%q stream=%t",
		generation,
		pluginKey,
		protocolRequest.Protocol,
		protocolRequest.Stream,
	)

	release, admissionErr := deps.admissions.acquire(
		pinned.Plugin.Meta.Key,
		common.GetContextKeyInt(c, constant.ContextKeyUserId),
		common.GetContextKeyInt(c, constant.ContextKeyTokenId),
	)
	if admissionErr != nil {
		if errors.Is(admissionErr, errPluginProtocolObservationLimitExceeded) {
			logger.LogDebug(c, "task_plugin subsystem=protocol event=admission_rejected generation=%d plugin=%q reason=observation_limit", generation, pluginKey)
			respondPluginProtocolError(c, http.StatusTooManyRequests, "rate_limit_exceeded", "Too many active task observations")
			return
		}
		logger.LogDebug(c, "task_plugin subsystem=protocol event=admission_rejected generation=%d plugin=%q reason=invalid_identity", generation, pluginKey)
		respondPluginProtocolError(c, http.StatusUnauthorized, "authentication_error", "Authentication failed")
		return
	}
	defer release()
	logger.LogDebug(c, "task_plugin subsystem=protocol event=admission_acquired generation=%d plugin=%q", generation, pluginKey)

	clientRequest := c.Request
	var relayInfo *relaycommon.RelayInfo
	var outcome *taskSubmissionOutcome
	var taskErr *dto.TaskError
	var relayInfoErr error
	submissionStage := "relay_info"
	// A Responses client only observes an asynchronous task. Once admitted,
	// disconnecting that observer must not cancel submission, persistence, or
	// billing settlement; the submission keeps its own bounded lifetime.
	func() {
		submissionContext, cancelSubmission := context.WithTimeout(
			context.WithoutCancel(clientRequest.Context()),
			deps.submissionTimeout,
		)
		c.Request = clientRequest.Clone(submissionContext)
		defer func() {
			c.Request = clientRequest
			cancelSubmission()
		}()

		relayInfo, relayInfoErr = relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
		if relayInfoErr != nil {
			return
		}
		relayInfo.RelayMode = relayconstant.RelayModeVideoSubmit
		relayInfo.IsStream = false
		relayInfo.OriginModelName = c.GetString("resolved_task_model")
		if action := c.GetString("task_action"); action != "" {
			relayInfo.Action = action
		}
		submissionStage = "origin_task"
		if taskErr = relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
			return
		}
		if taskErr = relay.ApplyOriginTaskAffinity(c, relayInfo); taskErr != nil {
			return
		}

		submissionStage = "submission"
		logger.LogDebug(c, "task_plugin subsystem=protocol event=submission_start generation=%d plugin=%q protocol=%q stream=%t", generation, pluginKey, protocolRequest.Protocol, protocolRequest.Stream)
		outcome, taskErr = deps.submit(c, relayInfo)
	}()

	if clientRequest.Context().Err() != nil {
		logger.LogDebug(c, "task_plugin subsystem=protocol event=client_disconnected generation=%d plugin=%q stage=%s", generation, pluginKey, submissionStage)
		return
	}
	if relayInfoErr != nil {
		err := relayInfoErr
		logger.LogError(c, "build task protocol relay info failed: "+err.Error())
		logger.LogDebug(c, "task_plugin subsystem=protocol event=bridge_failed generation=%d plugin=%q stage=relay_info reason=invalid_context", generation, pluginKey)
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	if submissionStage == "origin_task" && taskErr != nil {
		logger.LogDebug(
			c,
			"task_plugin subsystem=protocol event=bridge_failed generation=%d plugin=%q stage=origin_task code=%q status=%d",
			generation,
			pluginKey,
			taskErr.Code,
			taskErr.StatusCode,
		)
		respondPluginProtocolSubmissionError(c, taskErr)
		return
	}

	if taskErr != nil {
		logger.LogDebug(
			c,
			"task_plugin subsystem=protocol event=submission_failed generation=%d plugin=%q code=%q status=%d local=%t",
			generation,
			pluginKey,
			taskErr.Code,
			taskErr.StatusCode,
			taskErr.LocalError,
		)
		respondPluginProtocolSubmissionError(c, taskErr)
		return
	}
	if outcome == nil || outcome.Task == nil || outcome.RelayInfo == nil ||
		outcome.Task.UserId != relayInfo.UserId ||
		outcome.Task.Platform != constant.TaskPlatform(pinned.Plugin.Meta.Key) {
		logger.LogError(c, "task protocol submission returned an invalid durable outcome")
		logger.LogDebug(c, "task_plugin subsystem=protocol event=submission_failed generation=%d plugin=%q reason=invalid_durable_outcome", generation, pluginKey)
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	logger.LogDebug(
		c,
		"task_plugin subsystem=protocol event=submission_durable generation=%d plugin=%q public_task_id=%q status=%q stream=%t",
		generation,
		pluginKey,
		outcome.Task.TaskID,
		taskPluginDebugStatus(string(outcome.Task.Status)),
		protocolRequest.Stream,
	)

	createdAt := outcome.Task.CreatedAt
	if createdAt == 0 {
		createdAt = outcome.Task.SubmitTime
	}
	if createdAt == 0 {
		createdAt = deps.now().Unix()
	}
	machine := relay.NewPluginResponsesMachine(
		outcome.Task.TaskID,
		outcome.RelayInfo.OriginModelName,
		createdAt,
		deps.protocolLimits,
	)
	background := false
	if body, ok := protocolRequest.Body.(map[string]any); ok && body["kind"] == string(pluginruntime.BodyJSON) {
		if requestBody, ok := body["value"].(map[string]any); ok {
			background, _ = requestBody["background"].(bool)
		}
	}
	if background {
		outcome.Task.PrivateData.ResponsesBackground = true
		if outcome.Task.ID != 0 {
			if err := model.DB.Model(outcome.Task).Update("private_data", outcome.Task.PrivateData).Error; err != nil {
				logger.LogError(c, "persist task background flag failed: "+err.Error())
			}
		}
		machine.SetBackground(true)
		if !protocolRequest.Stream {
			logger.LogDebug(
				c,
				"task_plugin subsystem=protocol event=background_return generation=%d plugin=%q public_task_id=%q status=%q",
				generation,
				pluginKey,
				outcome.Task.TaskID,
				taskPluginDebugStatus(string(outcome.Task.Status)),
			)
			c.JSON(http.StatusOK, machine.PendingResponse(string(outcome.Task.Status)))
			return
		}
		logger.LogDebug(c, "task_plugin subsystem=protocol event=background_stream generation=%d plugin=%q public_task_id=%q", generation, pluginKey, outcome.Task.TaskID)
	}
	if protocolRequest.Stream {
		logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_enter generation=%d plugin=%q mode=stream public_task_id=%q", generation, pluginKey, outcome.Task.TaskID)
		streamTaskPluginProtocol(c, pinned, protocolRequest, outcome.Task.TaskID, machine, deps)
		return
	}
	logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_enter generation=%d plugin=%q mode=nonstream public_task_id=%q", generation, pluginKey, outcome.Task.TaskID)
	waitTaskPluginProtocol(c, pinned, protocolRequest, outcome.Task.TaskID, machine, deps)
}

func streamTaskPluginProtocol(
	c *gin.Context,
	pinned pluginruntime.PinnedEndpoint,
	protocolRequest pluginruntime.ProtocolRequestContext,
	taskID string,
	machine *relay.PluginResponsesMachine,
	deps pluginProtocolBridgeDeps,
) {
	generation := pinned.Generation.Number
	pluginKey := pinned.Plugin.Meta.Key
	logger.LogDebug(
		c,
		"task_plugin subsystem=protocol event=observation_start generation=%d plugin=%q mode=stream public_task_id=%q timeout_ms=%d tick_ms=%d heartbeat_ms=%d",
		generation,
		pluginKey,
		taskID,
		deps.observationTimeout.Milliseconds(),
		deps.tickInterval.Milliseconds(),
		deps.heartbeatInterval.Milliseconds(),
	)
	created, err := machine.CreatedEvent()
	if err != nil {
		logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_failed generation=%d plugin=%q mode=stream stage=created_event reason=state_machine_error", generation, pluginKey)
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	helper.SetEventStreamHeaders(c)
	if err = writeTaskPluginProtocolEvent(c, created); err != nil {
		logger.LogDebug(c, "task_plugin subsystem=protocol event=client_write_failed generation=%d plugin=%q mode=stream stage=created_event", generation, pluginKey)
		return
	}

	observationContext, cancelObservation := context.WithTimeout(c.Request.Context(), deps.observationTimeout)
	defer cancelObservation()
	heartbeatTicker := time.NewTicker(deps.heartbeatInterval)
	defer heartbeatTicker.Stop()

	var previous relay.ProtocolState
	tickNumber := uint64(0)
	lastStatus := ""
	for {
		loadStarted := deps.now()
		loadContext, cancelLoad := context.WithTimeout(observationContext, deps.loadTimeout)
		task, exists, loadErr := deps.loadTask(
			loadContext,
			common.GetContextKeyInt(c, constant.ContextKeyUserId),
			constant.TaskPlatform(pinned.Plugin.Meta.Key),
			taskID,
		)
		loadContextErr := loadContext.Err()
		cancelLoad()
		loadElapsed := deps.now().Sub(loadStarted)
		if errors.Is(loadContextErr, context.DeadlineExceeded) &&
			observationContext.Err() == nil &&
			c.Request.Context().Err() == nil {
			logger.LogWarn(c, fmt.Sprintf(
				"task protocol database observation overloaded; plugin=%s task=%s",
				pinned.Plugin.Meta.Key,
				taskID,
			))
			logger.LogDebug(
				c,
				"task_plugin subsystem=protocol event=observation_tick generation=%d plugin=%q mode=stream tick=%d load_ms=%d overloaded=true",
				generation,
				pluginKey,
				tickNumber,
				loadElapsed.Milliseconds(),
			)
			delay := pluginProtocolTickDelay(taskID, tickNumber, deps.tickInterval, deps.tickJitter) + deps.tickInterval
			tickNumber++
			if !waitForTaskPluginProtocolTick(c, observationContext, heartbeatTicker, delay) {
				if errors.Is(observationContext.Err(), context.DeadlineExceeded) {
					logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_timeout generation=%d plugin=%q mode=stream last_status=%q", generation, pluginKey, taskPluginDebugStatus(lastStatus))
					writeTaskPluginProtocolTimeout(c, machine, lastStatus)
				} else if c.Request.Context().Err() != nil {
					logger.LogDebug(c, "task_plugin subsystem=protocol event=client_disconnected generation=%d plugin=%q mode=stream stage=backoff_wait", generation, pluginKey)
				} else {
					logger.LogDebug(c, "task_plugin subsystem=protocol event=client_write_failed generation=%d plugin=%q mode=stream stage=heartbeat", generation, pluginKey)
				}
				return
			}
			continue
		}
		if loadErr != nil || !exists || task == nil {
			if errors.Is(observationContext.Err(), context.DeadlineExceeded) {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_timeout generation=%d plugin=%q mode=stream last_status=%q", generation, pluginKey, taskPluginDebugStatus(lastStatus))
				writeTaskPluginProtocolTimeout(c, machine, lastStatus)
				return
			}
			if loadErr != nil && !errors.Is(loadErr, context.Canceled) {
				logger.LogError(c, "task protocol database observation failed")
			}
			if c.Request.Context().Err() == nil {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_failed generation=%d plugin=%q mode=stream stage=load reason=task_unavailable", generation, pluginKey)
				writeTaskPluginProtocolFailure(c, machine, lastStatus)
			} else {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=client_disconnected generation=%d plugin=%q mode=stream stage=load", generation, pluginKey)
			}
			return
		}
		previousStatus := lastStatus
		lastStatus = string(task.Status)
		if lastStatus != previousStatus {
			logger.LogDebug(
				c,
				"task_plugin subsystem=protocol event=status_transition generation=%d plugin=%q mode=stream tick=%d previous=%q status=%q load_ms=%d",
				generation,
				pluginKey,
				tickNumber,
				taskPluginDebugStatus(previousStatus),
				taskPluginDebugStatus(lastStatus),
				loadElapsed.Milliseconds(),
			)
		}
		view, viewErr := service.BuildTaskPluginView(task)
		if viewErr != nil {
			logger.LogError(c, "build task protocol view failed: "+viewErr.Error())
			writeTaskPluginProtocolFailure(c, machine, lastStatus)
			return
		}
		viewValue, viewErr := taskPluginProtocolJSONValue(view)
		if viewErr != nil {
			logger.LogError(c, "encode task protocol view failed: "+viewErr.Error())
			writeTaskPluginProtocolFailure(c, machine, lastStatus)
			return
		}
		hookStarted := deps.now()
		rendererContext, contextErr := taskPluginProtocolRendererContext(protocolRequest, pinned, task, deps.artifactContentURL)
		if contextErr != nil {
			logger.LogError(c, "build task protocol renderer context failed")
			writeTaskPluginProtocolFailure(c, machine, lastStatus)
			return
		}
		args := []any{rendererContext, viewValue}
		if previous.Present {
			previousValue, stateErr := previous.PluginValue()
			if stateErr != nil {
				logger.LogError(c, "decode task protocol state failed: "+stateErr.Error())
				writeTaskPluginProtocolFailure(c, machine, lastStatus)
				return
			}
			args = append(args, previousValue)
		}
		value, callErr := pinned.Plugin.Engine.CallPathWithAdmissionTimeout(observationContext, deps.admissionTimeout, "protocols", []string{pinned.Protocol, "renderEvents"}, args...)
		hookElapsed := deps.now().Sub(hookStarted)
		overloaded := false
		if callErr != nil {
			if errors.Is(observationContext.Err(), context.DeadlineExceeded) {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_timeout generation=%d plugin=%q mode=stream stage=render_events last_status=%q", generation, pluginKey, taskPluginDebugStatus(lastStatus))
				writeTaskPluginProtocolTimeout(c, machine, lastStatus)
				return
			}
			if c.Request.Context().Err() != nil {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=client_disconnected generation=%d plugin=%q mode=stream stage=render_events", generation, pluginKey)
				return
			}
			if errors.Is(callErr, pluginruntime.ErrCallAdmissionTimeout) {
				overloaded = true
				logger.LogWarn(c, fmt.Sprintf(
					"task protocol render hook overloaded; plugin=%s task=%s",
					pinned.Plugin.Meta.Key,
					taskID,
				))
			} else {
				logger.LogError(c, "task protocol render hook failed")
				logger.LogDebug(
					c,
					"task_plugin subsystem=protocol event=observation_failed generation=%d plugin=%q mode=stream stage=render_events reason=hook_failed elapsed_ms=%d",
					generation,
					pluginKey,
					hookElapsed.Milliseconds(),
				)
				writeTaskPluginProtocolFailure(c, machine, lastStatus)
				return
			}
		}
		if !overloaded {
			result, decodeErr := relay.DecodePluginProtocolEventResult(value, deps.protocolLimits)
			if decodeErr != nil {
				logger.LogError(c, "task protocol render result invalid: "+decodeErr.Error())
				writeTaskPluginProtocolFailure(c, machine, lastStatus)
				return
			}
			events, applyErr := machine.ApplyTick(result, lastStatus)
			if applyErr != nil {
				logger.LogError(c, "task protocol state transition failed: "+applyErr.Error())
				writeTaskPluginProtocolFailure(c, machine, lastStatus)
				return
			}
			logger.LogDebug(
				c,
				"task_plugin subsystem=protocol event=render_events generation=%d plugin=%q mode=stream tick=%d status=%q semantic_events=%d wire_events=%d done=%t state_present=%t elapsed_ms=%d",
				generation,
				pluginKey,
				tickNumber,
				taskPluginDebugStatus(lastStatus),
				len(result.Events),
				len(events),
				result.Done,
				result.State.Present,
				hookElapsed.Milliseconds(),
			)
			for _, event := range events {
				if err = writeTaskPluginProtocolEvent(c, event); err != nil {
					logger.LogDebug(c, "task_plugin subsystem=protocol event=client_write_failed generation=%d plugin=%q mode=stream stage=event event_type=%q sequence=%d", generation, pluginKey, event.Type, event.SequenceNumber)
					return
				}
			}
			if taskPluginProtocolEventsTerminal(events) {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_complete generation=%d plugin=%q mode=stream reason=terminal status=%q ticks=%d", generation, pluginKey, taskPluginDebugStatus(lastStatus), tickNumber+1)
				return
			}
			previous = result.State
		}

		delay := pluginProtocolTickDelay(taskID, tickNumber, deps.tickInterval, deps.tickJitter)
		tickNumber++
		if overloaded {
			delay += deps.tickInterval
		} else if hookElapsed > deps.tickInterval {
			delay += deps.tickInterval
			logger.LogWarn(c, fmt.Sprintf(
				"task protocol render hook slow; plugin=%s task=%s elapsed_ms=%d",
				pinned.Plugin.Meta.Key,
				taskID,
				hookElapsed.Milliseconds(),
			))
		}
		if !waitForTaskPluginProtocolTick(c, observationContext, heartbeatTicker, delay) {
			if errors.Is(observationContext.Err(), context.DeadlineExceeded) {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_timeout generation=%d plugin=%q mode=stream last_status=%q", generation, pluginKey, taskPluginDebugStatus(lastStatus))
				writeTaskPluginProtocolTimeout(c, machine, lastStatus)
			} else if c.Request.Context().Err() != nil {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=client_disconnected generation=%d plugin=%q mode=stream stage=tick_wait", generation, pluginKey)
			} else {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=client_write_failed generation=%d plugin=%q mode=stream stage=heartbeat", generation, pluginKey)
			}
			return
		}
	}
}

func waitForTaskPluginProtocolTick(
	c *gin.Context,
	observationContext context.Context,
	heartbeatTicker *time.Ticker,
	delay time.Duration,
) bool {
	tickTimer := time.NewTimer(delay)
	defer tickTimer.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return false
		case <-observationContext.Done():
			return false
		case <-heartbeatTicker.C:
			helper.ExtendWriteDeadline(c)
			if err := writeTaskPluginProtocolHeartbeat(c); err != nil {
				return false
			}
		case <-tickTimer.C:
			return true
		}
	}
}

func waitTaskPluginProtocol(
	c *gin.Context,
	pinned pluginruntime.PinnedEndpoint,
	protocolRequest pluginruntime.ProtocolRequestContext,
	taskID string,
	machine *relay.PluginResponsesMachine,
	deps pluginProtocolBridgeDeps,
) {
	generation := pinned.Generation.Number
	pluginKey := pinned.Plugin.Meta.Key
	logger.LogDebug(
		c,
		"task_plugin subsystem=protocol event=observation_start generation=%d plugin=%q mode=nonstream public_task_id=%q timeout_ms=%d tick_ms=%d",
		generation,
		pluginKey,
		taskID,
		deps.observationTimeout.Milliseconds(),
		deps.tickInterval.Milliseconds(),
	)
	observationContext, cancelObservation := context.WithTimeout(c.Request.Context(), deps.observationTimeout)
	defer cancelObservation()
	tickNumber := uint64(0)
	lastStatus := ""
	for {
		loadStarted := deps.now()
		loadContext, cancelLoad := context.WithTimeout(observationContext, deps.loadTimeout)
		task, exists, err := deps.loadTask(
			loadContext,
			common.GetContextKeyInt(c, constant.ContextKeyUserId),
			constant.TaskPlatform(pinned.Plugin.Meta.Key),
			taskID,
		)
		loadContextErr := loadContext.Err()
		cancelLoad()
		loadElapsed := deps.now().Sub(loadStarted)
		loadOverloaded := errors.Is(loadContextErr, context.DeadlineExceeded) &&
			observationContext.Err() == nil &&
			c.Request.Context().Err() == nil
		if loadOverloaded {
			logger.LogWarn(c, fmt.Sprintf(
				"task protocol database observation overloaded; plugin=%s task=%s",
				pinned.Plugin.Meta.Key,
				taskID,
			))
			logger.LogDebug(
				c,
				"task_plugin subsystem=protocol event=observation_tick generation=%d plugin=%q mode=nonstream tick=%d load_ms=%d overloaded=true",
				generation,
				pluginKey,
				tickNumber,
				loadElapsed.Milliseconds(),
			)
		} else if err != nil || !exists || task == nil {
			if errors.Is(observationContext.Err(), context.DeadlineExceeded) {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_timeout generation=%d plugin=%q mode=nonstream last_status=%q", generation, pluginKey, taskPluginDebugStatus(lastStatus))
				writeTaskPluginProtocolTimeoutResponse(c, machine, lastStatus)
				return
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.LogError(c, "task protocol database observation failed")
			}
			if c.Request.Context().Err() == nil {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_failed generation=%d plugin=%q mode=nonstream stage=load reason=task_unavailable", generation, pluginKey)
				writeTaskPluginProtocolFailureResponse(c, machine, lastStatus)
			} else {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=client_disconnected generation=%d plugin=%q mode=nonstream stage=load", generation, pluginKey)
			}
			return
		}
		overloaded := loadOverloaded
		if !loadOverloaded {
			previousStatus := lastStatus
			lastStatus = string(task.Status)
			if lastStatus != previousStatus {
				logger.LogDebug(
					c,
					"task_plugin subsystem=protocol event=status_transition generation=%d plugin=%q mode=nonstream tick=%d previous=%q status=%q load_ms=%d",
					generation,
					pluginKey,
					tickNumber,
					taskPluginDebugStatus(previousStatus),
					taskPluginDebugStatus(lastStatus),
					loadElapsed.Milliseconds(),
				)
			}
			logger.LogDebug(
				c,
				"task_plugin subsystem=protocol event=observation_tick generation=%d plugin=%q mode=nonstream tick=%d status=%q load_ms=%d overloaded=false",
				generation,
				pluginKey,
				tickNumber,
				taskPluginDebugStatus(lastStatus),
				loadElapsed.Milliseconds(),
			)
		}
		if !loadOverloaded && (task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure) {
			if task.Status == model.TaskStatusFailure {
				writeTaskPluginProtocolFailureResponse(c, machine, string(task.Status))
				return
			}
			response, hookElapsed, callErr := renderTaskPluginProtocolFinalResponse(
				observationContext,
				pinned,
				protocolRequest,
				task,
				machine,
				deps,
			)
			if callErr != nil {
				if errors.Is(observationContext.Err(), context.DeadlineExceeded) {
					logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_timeout generation=%d plugin=%q mode=nonstream stage=render_final last_status=%q", generation, pluginKey, taskPluginDebugStatus(lastStatus))
					writeTaskPluginProtocolTimeoutResponse(c, machine, lastStatus)
					return
				}
				if c.Request.Context().Err() != nil {
					logger.LogDebug(c, "task_plugin subsystem=protocol event=client_disconnected generation=%d plugin=%q mode=nonstream stage=render_final", generation, pluginKey)
					return
				}
				if errors.Is(callErr, pluginruntime.ErrCallAdmissionTimeout) {
					overloaded = true
					logger.LogWarn(c, fmt.Sprintf(
						"task protocol final hook overloaded; plugin=%s task=%s",
						pinned.Plugin.Meta.Key,
						taskID,
					))
				} else {
					logger.LogError(c, "task protocol final hook failed")
					logger.LogDebug(
						c,
						"task_plugin subsystem=protocol event=observation_failed generation=%d plugin=%q mode=nonstream stage=render_final reason=hook_failed elapsed_ms=%d",
						generation,
						pluginKey,
						hookElapsed.Milliseconds(),
					)
					writeTaskPluginProtocolFailureResponse(c, machine, lastStatus)
					return
				}
			} else {
				logger.LogDebug(
					c,
					"task_plugin subsystem=protocol event=render_final generation=%d plugin=%q mode=nonstream status=%q elapsed_ms=%d",
					generation,
					pluginKey,
					taskPluginDebugStatus(lastStatus),
					hookElapsed.Milliseconds(),
				)
				c.JSON(http.StatusOK, response)
				logger.LogDebug(
					c,
					"task_plugin subsystem=protocol event=observation_complete generation=%d plugin=%q mode=nonstream reason=terminal status=%q ticks=%d",
					generation,
					pluginKey,
					taskPluginDebugStatus(lastStatus),
					tickNumber+1,
				)
				return
			}
		}

		delay := pluginProtocolTickDelay(taskID, tickNumber, deps.tickInterval, deps.tickJitter)
		tickNumber++
		if overloaded {
			delay += deps.tickInterval
		}
		tickTimer := time.NewTimer(delay)
		select {
		case <-c.Request.Context().Done():
			if !tickTimer.Stop() {
				select {
				case <-tickTimer.C:
				default:
				}
			}
			logger.LogDebug(c, "task_plugin subsystem=protocol event=client_disconnected generation=%d plugin=%q mode=nonstream stage=tick_wait", generation, pluginKey)
			return
		case <-observationContext.Done():
			if !tickTimer.Stop() {
				select {
				case <-tickTimer.C:
				default:
				}
			}
			if errors.Is(observationContext.Err(), context.DeadlineExceeded) {
				logger.LogDebug(c, "task_plugin subsystem=protocol event=observation_timeout generation=%d plugin=%q mode=nonstream last_status=%q", generation, pluginKey, taskPluginDebugStatus(lastStatus))
				writeTaskPluginProtocolTimeoutResponse(c, machine, lastStatus)
			}
			return
		case <-tickTimer.C:
		}
	}
}

func renderTaskPluginProtocolFinalResponse(
	ctx context.Context,
	pinned pluginruntime.PinnedEndpoint,
	protocolRequest pluginruntime.ProtocolRequestContext,
	task *model.Task,
	machine *relay.PluginResponsesMachine,
	deps pluginProtocolBridgeDeps,
) (map[string]any, time.Duration, error) {
	view, err := service.BuildTaskPluginView(task)
	if err != nil {
		return nil, 0, err
	}
	viewValue, err := taskPluginProtocolJSONValue(view)
	if err != nil {
		return nil, 0, err
	}
	rendererContext, err := taskPluginProtocolRendererContext(
		protocolRequest,
		pinned,
		task,
		deps.artifactContentURL,
	)
	if err != nil {
		return nil, 0, err
	}
	hookStarted := deps.now()
	payload, err := pinned.Plugin.Engine.CallPathWithAdmissionTimeout(
		ctx,
		deps.admissionTimeout,
		"protocols",
		[]string{pinned.Protocol, "renderFinal"},
		rendererContext,
		viewValue,
	)
	hookElapsed := deps.now().Sub(hookStarted)
	if err != nil {
		return nil, hookElapsed, err
	}
	response, err := machine.FinalResponse(payload, string(task.Status))
	if err != nil {
		return nil, hookElapsed, err
	}
	return response, hookElapsed, nil
}

func renderTaskPluginProtocolEventsResponse(
	ctx context.Context,
	pinned pluginruntime.PinnedEndpoint,
	protocolRequest pluginruntime.ProtocolRequestContext,
	task *model.Task,
	machine *relay.PluginResponsesMachine,
	deps pluginProtocolBridgeDeps,
) (map[string]any, time.Duration, error) {
	view, err := service.BuildTaskPluginView(task)
	if err != nil {
		return nil, 0, err
	}
	viewValue, err := taskPluginProtocolJSONValue(view)
	if err != nil {
		return nil, 0, err
	}
	rendererContext, err := taskPluginProtocolRendererContext(
		protocolRequest,
		pinned,
		task,
		deps.artifactContentURL,
	)
	if err != nil {
		return nil, 0, err
	}
	hookStarted := deps.now()
	value, err := pinned.Plugin.Engine.CallPathWithAdmissionTimeout(
		ctx,
		deps.admissionTimeout,
		"protocols",
		[]string{pinned.Protocol, "renderEvents"},
		rendererContext,
		viewValue,
	)
	hookElapsed := deps.now().Sub(hookStarted)
	if err != nil {
		return nil, hookElapsed, err
	}
	result, err := relay.DecodePluginProtocolEventResult(value, deps.protocolLimits)
	if err != nil {
		return nil, hookElapsed, err
	}
	response, err := machine.FinalFromEvents(result, string(task.Status))
	if err != nil {
		return nil, hookElapsed, err
	}
	return response, hookElapsed, nil
}

func RetrieveTaskPluginResponse(c *gin.Context) {
	retrieveTaskPluginResponse(c, defaultPluginProtocolBridgeDeps())
}

func retrieveTaskPluginResponse(c *gin.Context, deps pluginProtocolBridgeDeps) {
	deps = deps.withDefaults()
	responseID := strings.TrimSpace(c.Param("response_id"))
	if !strings.HasPrefix(responseID, "resp_") {
		writeTaskPluginResponseNotFound(c, responseID, "bad_prefix")
		return
	}
	taskID := "task_" + strings.TrimPrefix(responseID, "resp_")
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	logger.LogDebug(c, "task_plugin subsystem=protocol event=retrieve_start response_id=%q public_task_id=%q", responseID, taskID)

	task, exists, err := deps.getByTaskId(userID, taskID)
	if err != nil {
		logger.LogError(c, "task protocol retrieve lookup failed")
		logger.LogDebug(c, "task_plugin subsystem=protocol event=retrieve_failed reason=lookup_error public_task_id=%q", taskID)
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	if !exists || task == nil {
		writeTaskPluginResponseNotFound(c, responseID, "missing")
		return
	}
	if !task.ResultRetrievable() {
		writeTaskPluginResponseNotFound(c, responseID, "result_discarded")
		return
	}

	plugin, generation, ok := deps.resolvePlugin(task.Platform)
	if !ok || plugin == nil {
		writeTaskPluginResponseNotFound(c, responseID, "no_plugin")
		return
	}
	claimsProtocol := false
	for _, claim := range plugin.Meta.Protocols {
		if claim.Name == "openai_responses" {
			claimsProtocol = true
			break
		}
	}
	if !claimsProtocol {
		writeTaskPluginResponseNotFound(c, responseID, "no_claim")
		return
	}

	generationNumber := uint64(0)
	if generation != nil {
		generationNumber = generation.Number
	}
	createdAt := task.CreatedAt
	if createdAt == 0 {
		createdAt = task.SubmitTime
	}
	if createdAt == 0 {
		createdAt = deps.now().Unix()
	}
	machine := relay.NewPluginResponsesMachine(
		task.TaskID,
		task.Properties.OriginModelName,
		createdAt,
		deps.protocolLimits,
	)
	machine.SetBackground(task.PrivateData.ResponsesBackground)
	pinned := pluginruntime.PinnedEndpoint{
		Generation: generation,
		Plugin:     plugin,
		Protocol:   "openai_responses",
		Model:      task.Properties.OriginModelName,
	}
	protocolRequest := pluginruntime.ProtocolRequestContext{
		RouteRequestContext: pluginruntime.RouteRequestContext{
			Path:   c.Request.URL.Path,
			Method: http.MethodGet,
			Params: map[string]string{"response_id": responseID},
			Query:  c.Request.URL.Query(),
			Body:   map[string]any{"kind": string(pluginruntime.BodyNone)},
		},
		Protocol:  "openai_responses",
		Operation: "retrieve",
		Model:     task.Properties.OriginModelName,
	}

	if task.Status == model.TaskStatusFailure {
		logger.LogDebug(c, "task_plugin subsystem=protocol event=retrieve_final generation=%d plugin=%q public_task_id=%q status=%q", generationNumber, plugin.Meta.Key, task.TaskID, taskPluginDebugStatus(string(task.Status)))
		writeTaskPluginProtocolFailureResponse(c, machine, string(task.Status))
		return
	}
	if task.Status != model.TaskStatusSuccess {
		logger.LogDebug(c, "task_plugin subsystem=protocol event=retrieve_pending generation=%d plugin=%q public_task_id=%q status=%q", generationNumber, plugin.Meta.Key, task.TaskID, taskPluginDebugStatus(string(task.Status)))
		c.JSON(http.StatusOK, machine.PendingResponse(string(task.Status)))
		return
	}

	var (
		response    map[string]any
		hookElapsed time.Duration
		renderErr   error
	)
	if plugin.Meta.ProtocolSupports("openai_responses", "sync") || plugin.Meta.ProtocolSupports("openai_responses", "background") {
		response, hookElapsed, renderErr = renderTaskPluginProtocolFinalResponse(
			c.Request.Context(),
			pinned,
			protocolRequest,
			task,
			machine,
			deps,
		)
	} else {
		response, hookElapsed, renderErr = renderTaskPluginProtocolEventsResponse(
			c.Request.Context(),
			pinned,
			protocolRequest,
			task,
			machine,
			deps,
		)
	}
	if renderErr != nil {
		logger.LogError(c, "task protocol retrieve render failed")
		logger.LogDebug(
			c,
			"task_plugin subsystem=protocol event=retrieve_failed generation=%d plugin=%q public_task_id=%q stage=render_final elapsed_ms=%d",
			generationNumber,
			plugin.Meta.Key,
			task.TaskID,
			hookElapsed.Milliseconds(),
		)
		writeTaskPluginProtocolFailureResponse(c, machine, string(task.Status))
		return
	}
	logger.LogDebug(
		c,
		"task_plugin subsystem=protocol event=retrieve_final generation=%d plugin=%q public_task_id=%q status=%q elapsed_ms=%d",
		generationNumber,
		plugin.Meta.Key,
		task.TaskID,
		taskPluginDebugStatus(string(task.Status)),
		hookElapsed.Milliseconds(),
	)
	c.JSON(http.StatusOK, response)
}

func writeTaskPluginResponseNotFound(c *gin.Context, responseID, reason string) {
	logger.LogDebug(c, "task_plugin subsystem=protocol event=retrieve_not_found reason=%s response_id=%q", reason, responseID)
	respondPluginProtocolError(c, http.StatusNotFound, "not_found", "No response found with id '"+responseID+"'.")
}

func writeTaskPluginProtocolHeartbeat(c *gin.Context) error {
	if _, err := c.Writer.Write([]byte(": PING\n")); err != nil {
		return err
	}
	return helper.FlushWriter(c)
}

func writeTaskPluginProtocolFailure(
	c *gin.Context,
	machine *relay.PluginResponsesMachine,
	taskStatus string,
) {
	failed, err := machine.FailureEvent(taskStatus)
	if err != nil {
		logger.LogError(c, "task protocol failure event failed: "+err.Error())
		return
	}
	_ = writeTaskPluginProtocolEvent(c, failed)
}

func writeTaskPluginProtocolTimeout(
	c *gin.Context,
	machine *relay.PluginResponsesMachine,
	taskStatus string,
) {
	incomplete, err := machine.TimeoutEvent(taskStatus)
	if err != nil {
		logger.LogError(c, "task protocol timeout event failed: "+err.Error())
		return
	}
	_ = writeTaskPluginProtocolEvent(c, incomplete)
}

func writeTaskPluginProtocolFailureResponse(
	c *gin.Context,
	machine *relay.PluginResponsesMachine,
	taskStatus string,
) {
	if taskStatus == string(model.TaskStatusFailure) {
		response, err := machine.FinalResponse(nil, taskStatus)
		if err != nil {
			logger.LogError(c, "task protocol terminal failure response failed: "+err.Error())
			respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
			return
		}
		c.JSON(http.StatusOK, response)
		return
	}
	response, err := machine.FailureResponse(taskStatus)
	if err != nil {
		logger.LogError(c, "task protocol failure response failed: "+err.Error())
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	c.JSON(http.StatusOK, response)
}

func writeTaskPluginProtocolTimeoutResponse(
	c *gin.Context,
	machine *relay.PluginResponsesMachine,
	lastStatus string,
) {
	response, err := machine.TimeoutResponse(lastStatus)
	if err != nil {
		logger.LogError(c, "task protocol timeout response failed: "+err.Error())
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	c.JSON(http.StatusOK, response)
}

func taskPluginProtocolJSONValue(value any) (any, error) {
	encoded, err := common.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err = common.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func taskPluginProtocolRendererContext(
	request pluginruntime.ProtocolRequestContext,
	pinned pluginruntime.PinnedEndpoint,
	task *model.Task,
	artifactContentURL func(taskID, artifactKey string) (string, error),
) (map[string]any, error) {
	rendererContext := request.JSValue()
	if task == nil || task.Status != model.TaskStatusSuccess {
		return rendererContext, nil
	}
	if pinned.Plugin == nil {
		return nil, errors.New("task artifact projection is unavailable")
	}

	artifacts, err := taskjsplugin.New(pinned.Plugin).ListArtifacts(task)
	if err != nil {
		return nil, fmt.Errorf("project task artifacts: %w", err)
	}
	artifacts, err = validateProjectedTaskArtifacts(artifacts)
	if err != nil {
		return nil, err
	}
	if len(artifacts) > 0 && artifactContentURL == nil {
		return nil, errors.New("task artifact projection is unavailable")
	}

	rendererArtifacts := make(map[string]any, len(artifacts))
	for _, artifact := range artifacts {
		contentURL, buildErr := artifactContentURL(task.TaskID, artifact.Key)
		if buildErr != nil {
			return nil, fmt.Errorf("build task artifact content URL: %w", buildErr)
		}
		item := map[string]any{
			"key":  artifact.Key,
			"type": artifact.Type,
			"url":  contentURL,
		}
		if artifact.MimeType != "" {
			item["mimeType"] = artifact.MimeType
		}
		rendererArtifacts[artifact.Key] = item
	}
	rendererContext["artifacts"] = rendererArtifacts
	return rendererContext, nil
}

func pluginProtocolTickDelay(taskID string, tick uint64, base, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return base
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(taskID))
	_, _ = hash.Write([]byte(":"))
	_, _ = hash.Write([]byte(strconv.FormatUint(tick, 10)))
	return base + time.Duration(hash.Sum64()%uint64(jitter+1))
}

func taskPluginProtocolEventsTerminal(events []dto.PluginResponsesStreamEvent) bool {
	for _, event := range events {
		switch event.Type {
		case "response.completed", "response.failed", "response.incomplete":
			return true
		}
	}
	return false
}

func writeTaskPluginProtocolEvent(c *gin.Context, event dto.PluginResponsesStreamEvent) error {
	encoded, err := common.Marshal(event)
	if err != nil {
		return err
	}
	helper.ExtendWriteDeadline(c)
	if _, err = c.Writer.Write([]byte("event: " + event.Type + "\n")); err != nil {
		return err
	}
	if _, err = c.Writer.Write([]byte("data: " + string(encoded) + "\n\n")); err != nil {
		return err
	}
	if err = helper.FlushWriter(c); err != nil {
		return err
	}
	logger.LogDebug(
		c,
		"task_plugin subsystem=protocol event=sse_event_sent event_type=%q sequence=%d",
		event.Type,
		event.SequenceNumber,
	)
	return nil
}

func respondPluginProtocolSubmissionError(c *gin.Context, taskErr *dto.TaskError) {
	status := http.StatusInternalServerError
	if taskErr != nil && taskErr.StatusCode >= 400 && taskErr.StatusCode <= 599 {
		status = taskErr.StatusCode
	}
	switch status {
	case http.StatusBadRequest:
		message := "Invalid task protocol request"
		if taskErr != nil && taskErr.Message != "" && (taskErr.Code == "invalid_request" || strings.HasPrefix(taskErr.Code, "invalid_request")) {
			message = taskErr.Message
		}
		respondPluginProtocolError(c, status, "invalid_request_error", message)
	case http.StatusUnauthorized:
		respondPluginProtocolError(c, status, "authentication_error", "Authentication failed")
	case http.StatusForbidden:
		respondPluginProtocolError(c, status, "permission_denied", "Task protocol request was denied")
	case http.StatusTooManyRequests:
		respondPluginProtocolError(c, status, "rate_limit_exceeded", "Too many requests")
	default:
		respondPluginProtocolError(c, status, "task_protocol_error", "Task protocol request failed")
	}
}

func respondPluginProtocolError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    "new_api_error",
			"code":    code,
		},
	})
}
