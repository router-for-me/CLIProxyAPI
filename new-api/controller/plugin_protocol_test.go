package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestServeTaskPluginProtocolWaitsForDurableSubmissionBeforeWriting(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "durable-barrier", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [], done: false}; },
			renderFinal: function() { return {}; }
		}};
	`)
	c, recorder := newPluginProtocolTestContext(true, false)
	submitStarted := make(chan struct{})
	releaseSubmit := make(chan struct{})
	done := make(chan struct{})

	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		close(submitStarted)
		<-releaseSubmit
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_durable", map[string]any{
			"must_not": "be_written",
		}), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		return nil, false, errors.New("observation failed after durable barrier")
	}

	go func() {
		defer close(done)
		serveTaskPluginProtocol(c, pinned, deps)
	}()
	select {
	case <-submitStarted:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "submission did not start")
	}

	assert.Empty(t, recorder.Header().Get("Content-Type"))
	assert.Empty(t, recorder.Body.String())
	assert.False(t, recorder.Flushed)

	close(releaseSubmit)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "protocol handler did not finish")
	}
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Contains(t, recorder.Body.String(), "event: response.created\n")
	assert.NotContains(t, recorder.Body.String(), "must_not")
}

func TestServeTaskPluginProtocolDisconnectDuringSubmissionFinishesDurableWithoutWriting(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "disconnect-during-submit", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [], done: false}; },
			renderFinal: function() { return {}; }
		}};
	`)
	c, recorder := newPluginProtocolTestContext(true, true)
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	submitStarted := make(chan struct{})
	checkSubmissionContext := make(chan struct{})
	submissionContextActive := make(chan struct{})
	releaseSubmit := make(chan struct{})
	observationStarted := make(chan struct{}, 1)
	done := make(chan struct{})
	deps := pluginProtocolTestDeps()
	deps.submit = func(c *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		close(submitStarted)
		<-checkSubmissionContext
		select {
		case <-c.Request.Context().Done():
			return nil, service.TaskErrorWrapperLocal(c.Request.Context().Err(), "request_cancelled", http.StatusRequestTimeout)
		default:
			close(submissionContextActive)
		}
		<-releaseSubmit
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_disconnect_durable", nil), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		observationStarted <- struct{}{}
		return nil, false, errors.New("observation must not start after disconnect")
	}

	go func() {
		defer close(done)
		serveTaskPluginProtocol(c, pinned, deps)
	}()
	select {
	case <-submitStarted:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "submission did not start")
	}
	cancel()
	close(checkSubmissionContext)
	select {
	case <-submissionContextActive:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "submission context was canceled with the client")
	}
	select {
	case <-done:
		require.FailNow(t, "protocol handler stopped before submission became durable")
	default:
	}
	close(releaseSubmit)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "protocol handler did not finish after durable submission")
	}

	assert.Empty(t, recorder.Header().Get("Content-Type"))
	assert.Empty(t, recorder.Body.String())
	assert.False(t, recorder.Flushed)
	select {
	case <-observationStarted:
		require.FailNow(t, "protocol observation started after client disconnect")
	default:
	}
}

func TestServeTaskPluginProtocolDisconnectBeforeDurableBarrierPersistsAndSettlesWithoutRefund(t *testing.T) {
	events := make([]string, 0, 3)
	database := setupTaskSubmissionDatabase(t, true, &events)
	previousLogConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = false
	t.Cleanup(func() { common.LogConsumeEnabled = previousLogConsumeEnabled })

	pinned := compilePluginProtocolTestEndpoint(t, "disconnect-before-durable", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [], done: false}; },
			renderFinal: function() { return {}; }
		}};
	`)
	c, recorder := newPluginProtocolTestContext(true, true)
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	billing := &taskSubmissionTestBilling{events: &events}
	submitStarted := make(chan struct{})
	releaseSubmit := make(chan struct{})
	observationStarted := make(chan struct{}, 1)
	done := make(chan struct{})

	deps := pluginProtocolTestDeps()
	deps.submit = func(c *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		info.Billing = billing
		info.TaskRelayInfo.PublicTaskID = "task_disconnect_persisted"
		info.TaskRelayInfo.LockedChannel = &model.Channel{
			Id:   1,
			Type: constant.ChannelTypeTaskPlugin,
			Name: "disconnect-before-durable",
		}
		info.ChannelMeta = &relaycommon.ChannelMeta{
			ChannelId:   1,
			ChannelType: constant.ChannelTypeTaskPlugin,
		}
		return executeTaskSubmissionWith(c, info, func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
			close(submitStarted)
			<-releaseSubmit
			return &relay.TaskSubmitResult{
				UpstreamTaskID: "upstream_disconnect_persisted",
				Platform:       constant.TaskPlatform(pinned.Plugin.Meta.Key),
				Quota:          7,
			}, nil
		})
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		observationStarted <- struct{}{}
		return nil, false, errors.New("observation must not start after disconnect")
	}

	go func() {
		defer close(done)
		serveTaskPluginProtocol(c, pinned, deps)
	}()
	select {
	case <-submitStarted:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "submission did not start")
	}
	cancel()
	close(releaseSubmit)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "detached submission did not finish")
	}

	assert.Equal(t, []string{"reserve", "insert", "settle"}, events)
	assert.Zero(t, billing.refunds)
	var persisted model.Task
	require.NoError(t, database.Where("task_id = ?", "task_disconnect_persisted").First(&persisted).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusNotStart), persisted.Status)
	assert.Equal(t, 7, persisted.Quota)
	assert.Equal(t, "upstream_disconnect_persisted", persisted.PrivateData.UpstreamTaskID)
	assert.Empty(t, recorder.Header().Get("Content-Type"))
	assert.Empty(t, recorder.Body.String())
	assert.False(t, recorder.Flushed)
	select {
	case <-observationStarted:
		require.FailNow(t, "protocol observation started after client disconnect")
	default:
	}
}

func TestServeTaskPluginProtocolDisconnectDuringTerminalSettlementStopsOnlyObservation(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "disconnect-terminal-settlement", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [], done: false}; },
			renderFinal: function() { return {}; }
		}};
	`)

	previousDB := model.DB
	previousMemoryCache := common.MemoryCacheEnabled
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Task{}))
	model.DB = database
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
	})
	baseURL := "https://example.com"
	channel := model.Channel{
		Type:    constant.ChannelTypeTaskPlugin,
		Name:    "terminal-settlement",
		Key:     "test-key",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
	}
	require.NoError(t, database.Create(&channel).Error)
	task := model.Task{
		TaskID:    "task_terminal_disconnect",
		Platform:  constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:    71,
		ChannelId: channel.Id,
		Quota:     10,
		Status:    model.TaskStatusSubmitted,
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: "upstream-terminal",
		},
	}
	require.NoError(t, database.Create(&task).Error)

	c, recorder := newPluginProtocolTestContext(true, true)
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	billingEvents := make([]string, 0)
	billing := &taskSubmissionTestBilling{events: &billingEvents}
	observationStarted := make(chan struct{})
	settlementStarted := make(chan struct{})
	releaseSettlement := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseSettlement:
		default:
			close(releaseSettlement)
		}
	})
	pollingDone := make(chan struct{})
	adaptor := &terminalSettlementPollingAdaptor{
		started: settlementStarted,
		release: releaseSettlement,
	}
	previousAdaptorFactory := service.GetTaskAdaptorFunc
	service.GetTaskAdaptorFunc = func(constant.TaskPlatform) service.TaskPollingAdaptor {
		return adaptor
	}
	t.Cleanup(func() { service.GetTaskAdaptorFunc = previousAdaptorFactory })

	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		info.Billing = billing
		return &taskSubmissionOutcome{
			Result:    &relay.TaskSubmitResult{},
			Task:      &task,
			RelayInfo: info,
		}, nil
	}
	deps.loadTask = func(ctx context.Context, _ int, _ constant.TaskPlatform, _ string) (*model.Task, bool, error) {
		close(observationStarted)
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	done := make(chan struct{})

	go func() {
		<-observationStarted
		defer close(pollingDone)
		service.DispatchPlatformUpdate(
			context.Background(),
			task.Platform,
			map[int][]string{channel.Id: {"upstream-terminal"}},
			map[string]*model.Task{"upstream-terminal": &task},
		)
	}()
	go func() {
		defer close(done)
		serveTaskPluginProtocol(c, pinned, deps)
	}()
	select {
	case <-settlementStarted:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "terminal settlement did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "protocol observation did not stop after terminal disconnect")
	}
	assert.Equal(t, []string{"response.created"}, pluginProtocolTestSSEEventTypes(recorder.Body.String()))
	assert.Zero(t, billing.refunds)

	close(releaseSettlement)
	select {
	case <-pollingDone:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "terminal settlement was canceled with the client observation")
	}

	var persisted model.Task
	require.NoError(t, database.Where("task_id = ?", task.TaskID).First(&persisted).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), persisted.Status)
	assert.Equal(t, "100%", persisted.Progress)
	assert.Equal(t, 10, persisted.Quota)
	assert.True(t, adaptor.completed)
	assert.Empty(t, billingEvents)
}

type terminalSettlementPollingAdaptor struct {
	started   chan struct{}
	release   chan struct{}
	completed bool
}

func (a *terminalSettlementPollingAdaptor) Init(*relaycommon.RelayInfo) {}

func (a *terminalSettlementPollingAdaptor) FetchTask(string, string, *model.Task, string) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}, nil
}

func (a *terminalSettlementPollingAdaptor) ParseTaskResult(*model.Task, *http.Response, []byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{
		Status:   model.TaskStatusSuccess,
		Progress: "100%",
	}, nil
}

func (a *terminalSettlementPollingAdaptor) AdjustBillingOnComplete(task *model.Task, _ *relaycommon.TaskInfo) int {
	close(a.started)
	<-a.release
	a.completed = true
	return task.Quota
}

func TestPluginProtocolBridgeBoundsDatabaseReadBelowHeartbeat(t *testing.T) {
	deps := pluginProtocolBridgeDeps{
		observationTimeout: time.Minute,
		loadTimeout:        10 * time.Second,
		tickInterval:       time.Second,
		heartbeatInterval:  4 * time.Second,
		admissionTimeout:   time.Second,
	}.withDefaults()

	assert.Equal(t, 2*time.Second, deps.loadTimeout)
}

func TestServeTaskPluginProtocolPostDurableObservationFailureUsesCanonicalResponse(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "observation-failure", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [], done: false}; },
			renderFinal: function() { return {}; }
		}};
	`)
	c, recorder := newPluginProtocolTestContext(false, false)
	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_observation_failure", nil), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		return nil, false, errors.New("database-secret https://database.invalid/?token=hidden")
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.Equal(t, http.StatusOK, recorder.Code)
	var response dto.PluginResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "failed", response.Status)
	require.NotNil(t, response.Error)
	assert.Equal(t, "server_error", response.Error.Code)
	assert.Equal(t, "The task could not be observed.", response.Error.Message)
	assert.Equal(t, "queued", response.Metadata["task_status"])
	assert.Equal(t, "/v1/responses/resp_observation_failure", response.Metadata["retrieval_path"])
	assert.NotContains(t, recorder.Body.String(), "secret")
	assert.NotContains(t, recorder.Body.String(), "database.invalid")
}

func TestServeTaskPluginProtocolStreamsPinnedGenerationWithHostFraming(t *testing.T) {
	oldPinned := compilePluginProtocolTestEndpoint(t, "generation-pinned", `
		export const protocols = {openai_responses: {
			renderEvents: function(ctx, task, previousState) {
				if (ctx.stream !== true || ctx.body.value.stream !== true) {
					throw new Error("host did not preserve parsed stream mode");
				}
				if (arguments.length === 2) {
					return {events: [], state: null, done: false};
				}
				if (arguments.length !== 3 || previousState !== null) {
					throw new Error("explicit null state was not supplied on the next tick");
				}
				return {events: [{type: "output", data: "old-generation"}], done: true};
			},
			renderFinal: function() { throw new Error("stream called renderFinal"); }
		}};
	`)
	newPinned := compilePluginProtocolTestEndpoint(t, "generation-pinned", `
		export const protocols = {openai_responses: {
			renderEvents: function() {
				return {events: [{type: "output", data: "new-generation"}], done: true};
			},
			renderFinal: function() { return "new-generation"; }
		}};
	`)
	require.NotSame(t, oldPinned.Plugin.Engine, newPinned.Plugin.Engine)

	c, recorder := newPluginProtocolTestContext(true, true)
	c.Set(pluginruntime.ContextKeyRouteRequest, pluginruntime.RouteRequestContext{
		Path:        "/v1/responses",
		Method:      http.MethodPost,
		RequestBody: map[string]any{"model": "video-model", "stream": false},
	})
	loadCount := 0
	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, oldPinned.Plugin.Meta.Key, "task_generation", map[string]any{
			"client_response": "ignored",
		}), nil
	}
	deps.loadTask = func(_ context.Context, userID int, platform constant.TaskPlatform, taskID string) (*model.Task, bool, error) {
		loadCount++
		assert.Equal(t, 71, userID)
		assert.Equal(t, constant.TaskPlatform(oldPinned.Plugin.Meta.Key), platform)
		assert.Equal(t, "task_generation", taskID)
		status := model.TaskStatus(model.TaskStatusInProgress)
		if loadCount == 2 {
			status = model.TaskStatus(model.TaskStatusSuccess)
		}
		return &model.Task{
			TaskID:   taskID,
			UserId:   userID,
			Platform: platform,
			Status:   status,
		}, true, nil
	}

	serveTaskPluginProtocol(c, oldPinned, deps)

	assert.Equal(t, 2, loadCount)
	assert.True(t, recorder.Flushed)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Equal(t, []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}, pluginProtocolTestSSEEventTypes(recorder.Body.String()))
	assert.True(t, strings.HasPrefix(recorder.Body.String(), "event: response.created\ndata: {"))
	assert.Contains(t, recorder.Body.String(), `"sequence_number":0`)
	assert.Contains(t, recorder.Body.String(), `"sequence_number":7`)
	assert.Contains(t, recorder.Body.String(), "old-generation")
	assert.NotContains(t, recorder.Body.String(), "new-generation")
}

func TestServeTaskPluginProtocolStreamMissingRenderEventsUsesFailureEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		status model.TaskStatus
	}{
		{name: "success", status: model.TaskStatusSuccess},
		{name: "failure", status: model.TaskStatusFailure},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			pinned := compilePluginProtocolTestEndpoint(t, "default-events-"+testCase.name, `
				export const protocols = {openai_responses: {
					renderFinal: function() { throw new Error("stream must not call renderFinal"); }
				}};
			`)
			c, recorder := newPluginProtocolTestContext(true, true)
			deps := pluginProtocolTestDeps()
			deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
				return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_default_events"), nil
			}
			deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
				return &model.Task{TaskID: "task_default_events", Platform: constant.TaskPlatform(pinned.Plugin.Meta.Key), UserId: 71, Status: testCase.status}, true, nil
			}

			serveTaskPluginProtocol(c, pinned, deps)

			assert.Equal(t, []string{"response.created", "response.failed"}, pluginProtocolTestSSEEventTypes(recorder.Body.String()))
			assert.NotContains(t, recorder.Body.String(), "stream must not call")
		})
	}
}

func TestServeTaskPluginProtocolStreamInjectsHostArtifactCapabilities(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "stream-artifacts", `
		export function listArtifacts(task) {
			if (task.data.output.video_url !== "https://upstream.invalid/video.mp4?secret=hidden") {
				throw new Error("listArtifacts did not receive raw Task.Data");
			}
			return [{key: "video", type: "video", mimeType: "video/mp4"}];
		}
		export function buildContentRequest() {
			throw new Error("rendering must not resolve provider content");
		}
		export const protocols = {openai_responses: {
			renderEvents: function(ctx, task) {
				const artifact = ctx.artifacts && ctx.artifacts.video;
				if (!artifact || artifact.key !== "video" || artifact.type !== "video" ||
					artifact.mimeType !== "video/mp4") {
					throw new Error("host artifact context is invalid");
				}
				return {events: [{type: "output", data: artifact.url}], done: true};
			},
			renderFinal: function() { throw new Error("stream called renderFinal"); }
		}};
	`)
	c, recorder := newPluginProtocolTestContext(true, true)
	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_stream_artifact", nil), nil
	}
	task := &model.Task{
		TaskID:   "task_stream_artifact",
		Platform: constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:   71,
		Status:   model.TaskStatusSuccess,
	}
	task.SetData(map[string]any{
		"output": map[string]any{
			"video_url": "https://upstream.invalid/video.mp4?secret=hidden",
		},
	})
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		return task, true, nil
	}
	deps.artifactContentURL = func(taskID, artifactKey string) (string, error) {
		assert.Equal(t, "task_stream_artifact", taskID)
		assert.Equal(t, "video", artifactKey)
		return "https://gateway.example/v1/tasks/task_stream_artifact/artifacts/video/content?access=host-capability", nil
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), task.Status)
	assert.Equal(t, []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}, pluginProtocolTestSSEEventTypes(recorder.Body.String()))
	assert.Contains(t, recorder.Body.String(), "host-capability")
	assert.NotContains(t, recorder.Body.String(), "upstream.invalid")
	assert.NotContains(t, recorder.Body.String(), "secret")
}

func TestTaskPluginProtocolHeartbeatDoesNotDispatchEmptySDKEvent(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	require.NoError(t, writeTaskPluginProtocolHeartbeat(c))

	assert.Equal(t, ": PING\n", recorder.Body.String())
	assert.True(t, recorder.Flushed)
}

func TestServeTaskPluginProtocolNonStreamUsesFinalHookAndHostEnvelope(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "final-response", `
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("non-stream called renderEvents"); },
			renderFinal: function(ctx, task) {
				if (ctx.stream !== false || ctx.body.value.stream !== false) {
					throw new Error("host did not preserve parsed non-stream mode");
				}
				return {
					id: "plugin-controlled-id",
					status: "plugin-controlled-status",
					metadata: {plugin_field: "kept", task_id: "plugin-controlled-task"},
					output: [{
						id: "plugin-controlled-item",
						type: "message",
						status: "plugin-controlled-item-status",
						role: "assistant",
						content: [{
							id: "plugin-controlled-content",
							type: "output_text",
							text: task.data.value,
							annotations: [],
							logprobs: []
						}]
					}],
					custom_field: "kept"
				};
			}
		}};
	`)
	c, recorder := newPluginProtocolTestContext(false, false)
	c.Set(pluginruntime.ContextKeyRouteRequest, pluginruntime.RouteRequestContext{
		Path:        "/v1/responses",
		Method:      http.MethodPost,
		RequestBody: map[string]any{"model": "video-model", "stream": true},
	})
	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_final", map[string]any{
			"client_response_secret": "must-be-ignored",
		}), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		task := &model.Task{
			TaskID:   "task_final",
			Platform: constant.TaskPlatform(pinned.Plugin.Meta.Key),
			UserId:   71,
			Status:   model.TaskStatusSuccess,
		}
		task.SetData(map[string]any{"value": "plugin-semantic-result"})
		return task, true, nil
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.NotEqual(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	var response dto.PluginResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "resp_final", response.ID)
	assert.Equal(t, "response", response.Object)
	assert.Equal(t, "completed", response.Status)
	assert.Equal(t, "video-model", response.Model)
	assert.Equal(t, "task_final", response.Metadata["task_id"])
	assert.Equal(t, "kept", response.Metadata["plugin_field"])
	require.Len(t, response.Output, 1)
	assert.Equal(t, "item_task_final_0", response.Output[0].ID)
	assert.Equal(t, "completed", response.Output[0].Status)
	require.Len(t, response.Output[0].Content, 1)
	assert.Equal(t, "content_task_final_0_0", response.Output[0].Content[0].ID)
	assert.Equal(t, "plugin-semantic-result", response.Output[0].Content[0].Text)
	assert.Contains(t, recorder.Body.String(), `"custom_field":"kept"`)
	assert.NotContains(t, recorder.Body.String(), "plugin-controlled-id")
	assert.NotContains(t, recorder.Body.String(), "client_response_secret")
}

func TestServeTaskPluginProtocolNonStreamInjectsHostArtifactCapabilities(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "final-artifacts", `
		export function listArtifacts() {
			return [{key: "video", type: "video"}];
		}
		export function buildContentRequest() {
			throw new Error("rendering must not resolve provider content");
		}
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("non-stream called renderEvents"); },
			renderFinal: function(ctx) {
				const artifact = ctx.artifacts && ctx.artifacts.video;
				if (!artifact || artifact.key !== "video" || artifact.type !== "video" ||
					Object.prototype.hasOwnProperty.call(artifact, "mimeType")) {
					throw new Error("host artifact context is invalid");
				}
				return {
					output: [{
						type: "message",
						status: "completed",
						role: "assistant",
						content: [{
							type: "output_text",
							text: artifact.url,
							annotations: [],
							logprobs: []
						}]
					}]
				};
			}
		}};
	`)
	c, recorder := newPluginProtocolTestContext(false, false)
	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_final_artifact", nil), nil
	}
	task := &model.Task{
		TaskID:   "task_final_artifact",
		Platform: constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:   71,
		Status:   model.TaskStatusSuccess,
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		return task, true, nil
	}
	deps.artifactContentURL = func(taskID, artifactKey string) (string, error) {
		assert.Equal(t, "task_final_artifact", taskID)
		assert.Equal(t, "video", artifactKey)
		return "https://gateway.example/v1/tasks/task_final_artifact/artifacts/video/content?access=host-capability", nil
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), task.Status)
	var response dto.PluginResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "completed", response.Status)
	require.Len(t, response.Output, 1)
	require.Len(t, response.Output[0].Content, 1)
	assert.Contains(t, response.Output[0].Content[0].Text, "host-capability")
}

func TestServeTaskPluginProtocolArtifactURLFailureOnlyFailsCurrentRendering(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			pinned := compilePluginProtocolTestEndpoint(t, "artifact-url-failure-"+strconv.FormatBool(stream), `
				export function listArtifacts() {
					return [{key: "video", type: "video"}];
				}
				export function buildContentRequest() {
					throw new Error("unused");
				}
				export const protocols = {openai_responses: {
					renderEvents: function() {
						return {events: [{type: "output", data: "must-not-render"}], done: true};
					},
					renderFinal: function() {
						return {output: []};
					}
				}};
			`)
			c, recorder := newPluginProtocolTestContext(stream, stream)
			deps := pluginProtocolTestDeps()
			deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
				return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_capability_failure", nil), nil
			}
			task := &model.Task{
				TaskID:   "task_capability_failure",
				Platform: constant.TaskPlatform(pinned.Plugin.Meta.Key),
				UserId:   71,
				Status:   model.TaskStatusSuccess,
			}
			deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
				return task, true, nil
			}
			deps.artifactContentURL = func(string, string) (string, error) {
				return "", errors.New("public address is unavailable")
			}

			serveTaskPluginProtocol(c, pinned, deps)

			assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), task.Status)
			assert.NotContains(t, recorder.Body.String(), "must-not-render")
			assert.NotContains(t, recorder.Body.String(), "public address")
			if stream {
				assert.Equal(t, []string{"response.created", "response.failed"}, pluginProtocolTestSSEEventTypes(recorder.Body.String()))
			} else {
				var response dto.PluginResponsesResponse
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				assert.Equal(t, "failed", response.Status)
				require.NotNil(t, response.Error)
				assert.Equal(t, "server_error", response.Error.Code)
				assert.Equal(t, "completed", response.Metadata["task_status"])
			}
		})
	}
}

func TestServeTaskPluginProtocolNonStreamTaskFailureSkipsFinalHook(t *testing.T) {
	logs := make([]string, 0, 1)
	pinned := compilePluginProtocolTestEndpointWithOptions(t, "failed-final", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [], done: false}; },
			renderFinal: function() {
				console.log("renderFinal called");
				return {
					output: [{id: "secret-id", content: [{text: "plugin-secret"}]}],
					secret: "https://secret.invalid/"
				};
			}
		}};
	`, pluginruntime.Options{
		Log: func(message string) { logs = append(logs, message) },
	})
	c, recorder := newPluginProtocolTestContext(false, false)
	deps := pluginProtocolTestDeps()
	deps.artifactContentURL = func(string, string) (string, error) {
		require.FailNow(t, "failed tasks must not project artifact URLs")
		return "", nil
	}
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_failed", map[string]any{
			"credential": "client-response-secret",
		}), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		task := &model.Task{
			TaskID:     "task_failed",
			Platform:   constant.TaskPlatform(pinned.Plugin.Meta.Key),
			UserId:     71,
			Status:     model.TaskStatusFailure,
			FailReason: "upstream credential at https://secret.invalid/",
		}
		task.SetData(map[string]any{"secret": "database-secret"})
		return task, true, nil
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.Equal(t, http.StatusOK, recorder.Code)
	var response dto.PluginResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "failed", response.Status)
	require.NotNil(t, response.Error)
	assert.Equal(t, "server_error", response.Error.Code)
	assert.Equal(t, "The task failed.", response.Error.Message)
	assert.Empty(t, response.Output)
	assert.NotContains(t, recorder.Body.String(), "secret")
	assert.NotContains(t, recorder.Body.String(), "credential")
	assert.Empty(t, logs)
}

func TestServeTaskPluginProtocolStreamTaskFailureSuppressesPluginAndDatabaseDetails(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "failed-stream", `
		export const protocols = {openai_responses: {
			renderEvents: function() {
				return {
					events: [{type: "output", data: "plugin-secret https://plugin.invalid/?key=hidden"}],
					done: true
				};
			},
			renderFinal: function() { return "unused-secret"; }
		}};
	`)
	c, recorder := newPluginProtocolTestContext(true, false)
	deps := pluginProtocolTestDeps()
	deps.artifactContentURL = func(string, string) (string, error) {
		require.FailNow(t, "failed tasks must not project artifact URLs")
		return "", nil
	}
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_stream_failed", nil), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		task := &model.Task{
			TaskID:     "task_stream_failed",
			Platform:   constant.TaskPlatform(pinned.Plugin.Meta.Key),
			UserId:     71,
			Status:     model.TaskStatusFailure,
			FailReason: "database-secret https://database.invalid/?token=hidden",
		}
		task.SetData(map[string]any{"secret": "private-result"})
		return task, true, nil
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.Equal(t, []string{"response.created", "response.failed"}, pluginProtocolTestSSEEventTypes(recorder.Body.String()))
	assert.Contains(t, recorder.Body.String(), `"code":"server_error"`)
	assert.Contains(t, recorder.Body.String(), `"message":"The task failed."`)
	assert.Contains(t, recorder.Body.String(), `"task_status":"failed"`)
	assert.NotContains(t, recorder.Body.String(), "secret")
	assert.NotContains(t, recorder.Body.String(), "invalid")
	assert.NotContains(t, recorder.Body.String(), "hidden")
}

func TestServeTaskPluginProtocolRejectsUnsupportedProtocolBeforeSubmission(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "unsupported-protocol", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [], done: false}; },
			renderFinal: function() { return {}; }
		}};
	`)
	pinned.Protocol = "unsupported"
	c, recorder := newPluginProtocolTestContext(false, false)
	c.Set(pluginruntime.ContextKeyProtocolRequest, pluginruntime.ProtocolRequestContext{
		RouteRequestContext: pluginruntime.RouteRequestContext{
			Path:        "/v1/videos",
			Method:      http.MethodPost,
			RequestBody: map[string]any{"model": "video-model"},
		},
		Protocol: pinned.Protocol,
	})
	submitted := false
	deps := pluginProtocolTestDeps()
	deps.submit = func(*gin.Context, *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		submitted = true
		return nil, nil
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.False(t, submitted)
	assert.Equal(t, http.StatusNotImplemented, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"task_protocol_not_available"`)
}

func TestServeTaskPluginProtocolRejectsObservationAdmissionBeforeSubmission(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "admission-limit", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [], done: false}; },
			renderFinal: function() { return {}; }
		}};
	`)
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-stream", true: "stream"}[stream], func(t *testing.T) {
			c, recorder := newPluginProtocolTestContext(stream, stream)
			submitted := false
			deps := pluginProtocolTestDeps()
			deps.admissions = newPluginProtocolObservationLimiter(pluginProtocolObservationLimits{
				global:    0,
				perPlugin: 1,
				perUser:   1,
				perToken:  1,
			})
			deps.submit = func(*gin.Context, *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
				submitted = true
				return nil, nil
			}

			serveTaskPluginProtocol(c, pinned, deps)

			assert.False(t, submitted)
			assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"rate_limit_exceeded"`)
			assert.NotEqual(t, "text/event-stream", recorder.Header().Get("Content-Type"))
		})
	}
}

func TestServeTaskPluginProtocolBackgroundNonStreamReturnsPendingWithoutObservation(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "background-create", `
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("background create called renderEvents"); },
			renderFinal: function() { throw new Error("background create called renderFinal"); }
		}};
	`)
	c, recorder := newPluginProtocolTestContext(false, false)
	setProtocolRequestBackground(c, true)
	loadCalls := 0
	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_background", nil), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		loadCalls++
		return nil, false, errors.New("observation must not start for background create")
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.Equal(t, 0, loadCalls)
	assert.Equal(t, http.StatusOK, recorder.Code)
	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "resp_background", response["id"])
	assert.Equal(t, "response", response["object"])
	assert.Equal(t, "queued", response["status"])
	assert.Equal(t, true, response["background"])
	assert.Nil(t, response["completed_at"])
	assert.Nil(t, response["error"])
	assert.Nil(t, response["usage"])
	assert.Empty(t, response["output"])
	metadata, ok := response["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "task_background", metadata["task_id"])
	assert.Equal(t, "queued", metadata["task_status"])
	assert.Equal(t, "/v1/responses/resp_background", metadata["retrieval_path"])
	assert.NotEqual(t, "text/event-stream", recorder.Header().Get("Content-Type"))
}

func TestServeTaskPluginProtocolBackgroundStreamEntersObservation(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "background-stream", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [{type: "output", data: "streamed"}], done: true}; },
			renderFinal: function() { throw new Error("stream called renderFinal"); }
		}};
	`)
	c, recorder := newPluginProtocolTestContext(true, true)
	setProtocolRequestBackground(c, true)
	loadCalls := 0
	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_background_stream", nil), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		loadCalls++
		return &model.Task{
			TaskID:   "task_background_stream",
			Platform: constant.TaskPlatform(pinned.Plugin.Meta.Key),
			UserId:   71,
			Status:   model.TaskStatusSuccess,
		}, true, nil
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.Greater(t, loadCalls, 0)
	assert.Equal(t, []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}, pluginProtocolTestSSEEventTypes(recorder.Body.String()))
}

func TestRetrieveTaskPluginResponsePendingSkipsRenderFinal(t *testing.T) {
	logs := make([]string, 0, 1)
	pinned := compilePluginProtocolRetrieveEndpoint(t, "retrieve-pending", `
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("retrieve pending called renderEvents"); },
			renderFinal: function() {
				console.log("renderFinal called");
				return {};
			}
		}};
	`, logsAppender(&logs))
	c, recorder := newPluginProtocolRetrieveContext("resp_retrieve_pending")
	deps := pluginProtocolRetrieveDeps(pinned, &model.Task{
		TaskID:   "task_retrieve_pending",
		Platform: constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:   71,
		Status:   model.TaskStatusInProgress,
		PrivateData: model.TaskPrivateData{
			ResponsesBackground: true,
		},
		Properties: model.Properties{OriginModelName: "video-model"},
		CreatedAt:  1_710_000_000,
	}, true, nil)

	retrieveTaskPluginResponse(c, deps)

	assert.Empty(t, logs)
	assert.Equal(t, http.StatusOK, recorder.Code)
	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "resp_retrieve_pending", response["id"])
	assert.Equal(t, "in_progress", response["status"])
	assert.Equal(t, "video-model", response["model"])
	assert.Equal(t, true, response["background"])
	assert.Nil(t, response["completed_at"])
	assert.Empty(t, response["output"])
	metadata, ok := response["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "/v1/responses/resp_retrieve_pending", metadata["retrieval_path"])
}

func TestRetrieveTaskPluginResponseEchoesOriginModelName(t *testing.T) {
	pinned := compilePluginProtocolRetrieveEndpoint(t, "retrieve-alias-echo", `
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("pending retrieve called renderEvents"); },
			renderFinal: function() { throw new Error("pending retrieve called renderFinal"); }
		}};
	`, pluginruntime.Options{})
	c, recorder := newPluginProtocolRetrieveContext("resp_retrieve_alias")
	deps := pluginProtocolRetrieveDeps(pinned, &model.Task{
		TaskID:     "task_retrieve_alias",
		Platform:   constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:     71,
		Status:     model.TaskStatusInProgress,
		Properties: model.Properties{OriginModelName: "alias-model"},
		CreatedAt:  1_710_000_000,
	}, true, nil)

	retrieveTaskPluginResponse(c, deps)

	assert.Equal(t, http.StatusOK, recorder.Code)
	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "alias-model", response["model"])
}

func TestRetrieveTaskPluginResponseHidesDiscardedResult(t *testing.T) {
	pinned := compilePluginProtocolRetrieveEndpoint(t, "retrieve-discarded", `
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("discarded retrieve called renderEvents"); },
			renderFinal: function() { throw new Error("discarded retrieve called renderFinal"); }
		}};
	`, pluginruntime.Options{})
	c, recorder := newPluginProtocolRetrieveContext("resp_retrieve_discarded")
	deps := pluginProtocolRetrieveDeps(pinned, &model.Task{
		TaskID:      "task_retrieve_discarded",
		Platform:    constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:      71,
		Status:      model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{ResultDiscarded: true},
		CreatedAt:   1_710_000_000,
	}, true, nil)

	retrieveTaskPluginResponse(c, deps)

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"not_found"`)
}

func TestRetrieveTaskPluginResponseSuccessRendersFinal(t *testing.T) {
	logs := make([]string, 0, 1)
	pinned := compilePluginProtocolRetrieveEndpoint(t, "retrieve-success", `
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("retrieve success called renderEvents"); },
			renderFinal: function() {
				console.log("renderFinal called");
				return {
					output: [{
						type: "message",
						status: "completed",
						role: "assistant",
						content: [{type: "output_text", text: "retrieved-final", annotations: [], logprobs: []}]
					}]
				};
			}
		}};
	`, logsAppender(&logs))
	c, recorder := newPluginProtocolRetrieveContext("resp_retrieve_success")
	deps := pluginProtocolRetrieveDeps(pinned, &model.Task{
		TaskID:     "task_retrieve_success",
		Platform:   constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:     71,
		Status:     model.TaskStatusSuccess,
		Properties: model.Properties{OriginModelName: "video-model"},
		CreatedAt:  1_710_000_000,
	}, true, nil)

	retrieveTaskPluginResponse(c, deps)

	require.NotEmpty(t, logs)
	assert.Contains(t, logs[0], "renderFinal called")
	assert.Equal(t, http.StatusOK, recorder.Code)
	var response dto.PluginResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "completed", response.Status)
	assert.Equal(t, "resp_retrieve_success", response.ID)
	require.Len(t, response.Output, 1)
	require.Len(t, response.Output[0].Content, 1)
	assert.Equal(t, "retrieved-final", response.Output[0].Content[0].Text)
}

func TestRetrieveTaskPluginResponseStreamOnlySuccessSynthesizesFromEvents(t *testing.T) {
	logs := make([]string, 0, 1)
	pinned := compilePluginProtocolRetrieveEndpoint(t, "retrieve-stream-only", `
		export const protocols = {openai_responses: {
			renderEvents: function() {
				console.log("renderEvents called");
				return {events: [{type: "output", data: "synthesized-retrieve"}], done: true};
			},
			renderFinal: function() { throw new Error("stream-only retrieve called renderFinal"); }
		}};
	`, logsAppender(&logs))
	pinned.Plugin.Meta.Protocols = []pluginruntime.ProtocolClaim{{Name: "openai_responses", Supports: []string{"stream"}}}
	c, recorder := newPluginProtocolRetrieveContext("resp_retrieve_stream")
	deps := pluginProtocolRetrieveDeps(pinned, &model.Task{
		TaskID:     "task_retrieve_stream",
		Platform:   constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:     71,
		Status:     model.TaskStatusSuccess,
		Properties: model.Properties{OriginModelName: "video-model"},
		CreatedAt:  1_710_000_000,
	}, true, nil)

	retrieveTaskPluginResponse(c, deps)

	require.NotEmpty(t, logs)
	assert.Contains(t, logs[0], "renderEvents called")
	assert.Equal(t, http.StatusOK, recorder.Code)
	var response dto.PluginResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "completed", response.Status)
	require.NotEmpty(t, response.Output)
}

func TestRetrieveTaskPluginResponseStreamOnlyPendingAndFailureStayHostEnvelopes(t *testing.T) {
	logs := make([]string, 0, 1)
	pinned := compilePluginProtocolRetrieveEndpoint(t, "retrieve-stream-envelope", `
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("envelope retrieve called renderEvents"); },
			renderFinal: function() { throw new Error("envelope retrieve called renderFinal"); }
		}};
	`, logsAppender(&logs))
	pinned.Plugin.Meta.Protocols = []pluginruntime.ProtocolClaim{{Name: "openai_responses", Supports: []string{"stream"}}}

	t.Run("pending", func(t *testing.T) {
		logs = logs[:0]
		c, recorder := newPluginProtocolRetrieveContext("resp_retrieve_stream_pending")
		deps := pluginProtocolRetrieveDeps(pinned, &model.Task{
			TaskID:     "task_retrieve_stream_pending",
			Platform:   constant.TaskPlatform(pinned.Plugin.Meta.Key),
			UserId:     71,
			Status:     model.TaskStatusInProgress,
			Properties: model.Properties{OriginModelName: "video-model"},
			CreatedAt:  1_710_000_000,
		}, true, nil)
		retrieveTaskPluginResponse(c, deps)
		assert.Empty(t, logs)
		assert.Equal(t, http.StatusOK, recorder.Code)
		var response map[string]any
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, "in_progress", response["status"])
		assert.Empty(t, response["output"])
	})

	t.Run("failure", func(t *testing.T) {
		logs = logs[:0]
		c, recorder := newPluginProtocolRetrieveContext("resp_retrieve_stream_failure")
		deps := pluginProtocolRetrieveDeps(pinned, &model.Task{
			TaskID:     "task_retrieve_stream_failure",
			Platform:   constant.TaskPlatform(pinned.Plugin.Meta.Key),
			UserId:     71,
			Status:     model.TaskStatusFailure,
			Properties: model.Properties{OriginModelName: "video-model"},
		}, true, nil)
		retrieveTaskPluginResponse(c, deps)
		assert.Empty(t, logs)
		assert.Equal(t, http.StatusOK, recorder.Code)
		var response dto.PluginResponsesResponse
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, "failed", response.Status)
		require.NotNil(t, response.Error)
		assert.Equal(t, "The task failed.", response.Error.Message)
	})
}

func TestRetrieveTaskPluginResponseStreamOnlyRenderErrorUsesFailureEnvelope(t *testing.T) {
	pinned := compilePluginProtocolRetrieveEndpoint(t, "retrieve-stream-throw", `
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("retrieve boom"); },
			renderFinal: function() { throw new Error("stream-only retrieve called renderFinal"); }
		}};
	`, pluginruntime.Options{})
	pinned.Plugin.Meta.Protocols = []pluginruntime.ProtocolClaim{{Name: "openai_responses", Supports: []string{"stream"}}}
	c, recorder := newPluginProtocolRetrieveContext("resp_retrieve_stream_throw")
	deps := pluginProtocolRetrieveDeps(pinned, &model.Task{
		TaskID:     "task_retrieve_stream_throw",
		Platform:   constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:     71,
		Status:     model.TaskStatusSuccess,
		Properties: model.Properties{OriginModelName: "video-model"},
		CreatedAt:  1_710_000_000,
	}, true, nil)

	retrieveTaskPluginResponse(c, deps)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "retrieve boom")
	var response dto.PluginResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "failed", response.Status)
	require.NotNil(t, response.Error)
	assert.Equal(t, "server_error", response.Error.Code)
	assert.Equal(t, "The task could not be observed.", response.Error.Message)
}

func TestRetrieveTaskPluginResponseFailureUsesFailedEnvelope(t *testing.T) {
	logs := make([]string, 0, 1)
	pinned := compilePluginProtocolRetrieveEndpoint(t, "retrieve-failure", `
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("retrieve failure called renderEvents"); },
			renderFinal: function() {
				console.log("renderFinal called");
				return {};
			}
		}};
	`, logsAppender(&logs))
	c, recorder := newPluginProtocolRetrieveContext("resp_retrieve_failure")
	deps := pluginProtocolRetrieveDeps(pinned, &model.Task{
		TaskID:     "task_retrieve_failure",
		Platform:   constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:     71,
		Status:     model.TaskStatusFailure,
		Properties: model.Properties{OriginModelName: "video-model"},
	}, true, nil)

	retrieveTaskPluginResponse(c, deps)

	assert.Empty(t, logs)
	assert.Equal(t, http.StatusOK, recorder.Code)
	var response dto.PluginResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "failed", response.Status)
	require.NotNil(t, response.Error)
	assert.Equal(t, "server_error", response.Error.Code)
	assert.Equal(t, "The task failed.", response.Error.Message)
}

func TestRetrieveTaskPluginResponseNotFound(t *testing.T) {
	pinned := compilePluginProtocolRetrieveEndpoint(t, "retrieve-404", `
		export const protocols = {openai_responses: {
			renderFinal: function() { return {}; }
		}};
	`, pluginruntime.Options{})
	owned := &model.Task{
		TaskID:     "task_owned",
		Platform:   constant.TaskPlatform(pinned.Plugin.Meta.Key),
		UserId:     71,
		Status:     model.TaskStatusInProgress,
		Properties: model.Properties{OriginModelName: "video-model"},
	}

	tests := []struct {
		name       string
		responseID string
		userID     int
		task       *model.Task
		exists     bool
		plugin     *pluginruntime.LoadedPlugin
		claims     []pluginruntime.ProtocolClaim
	}{
		{name: "bad prefix", responseID: "task_owned", userID: 71, task: owned, exists: true, plugin: pinned.Plugin},
		{name: "missing", responseID: "resp_missing", userID: 71, exists: false, plugin: pinned.Plugin},
		{name: "other user", responseID: "resp_owned", userID: 99, task: owned, exists: false, plugin: pinned.Plugin},
		{name: "no plugin", responseID: "resp_owned", userID: 71, task: owned, exists: true},
		{name: "plugin does not claim protocol", responseID: "resp_owned", userID: 71, task: owned, exists: true, plugin: pinned.Plugin, claims: []pluginruntime.ProtocolClaim{{Name: "openai_video"}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.plugin != nil {
				if testCase.claims != nil {
					testCase.plugin.Meta.Protocols = testCase.claims
				} else {
					testCase.plugin.Meta.Protocols = []pluginruntime.ProtocolClaim{{Name: "openai_responses", Supports: []string{"stream", "sync", "background"}}}
				}
			}
			c, recorder := newPluginProtocolRetrieveContext(testCase.responseID)
			common.SetContextKey(c, constant.ContextKeyUserId, testCase.userID)
			deps := pluginProtocolRetrieveDeps(pinned, testCase.task, testCase.exists, nil)
			if testCase.plugin == nil {
				deps.resolvePlugin = func(constant.TaskPlatform) (*pluginruntime.LoadedPlugin, *pluginruntime.RoutingGeneration, bool) {
					return nil, nil, false
				}
			}

			retrieveTaskPluginResponse(c, deps)

			assert.Equal(t, http.StatusNotFound, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"not_found"`)
			assert.Contains(t, recorder.Body.String(), "No response found with id '"+testCase.responseID+"'.")
		})
	}
}

func TestRespondPluginProtocolSubmissionErrorPassesValidationMessage(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "protocol-validation-detail", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [], done: false}; },
			renderFinal: function() { return {}; }
		}};
	`)
	c, recorder := newPluginProtocolTestContext(false, false)
	deps := pluginProtocolTestDeps()
	deps.submit = func(*gin.Context, *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return nil, &dto.TaskError{
			Code:       "invalid_request",
			Message:    "model is required",
			StatusCode: http.StatusBadRequest,
			LocalError: true,
		}
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"message":"model is required"`)
	assert.Contains(t, recorder.Body.String(), `"code":"invalid_request_error"`)
	assert.NotContains(t, recorder.Body.String(), "Invalid task protocol request")
}

func TestRespondPluginProtocolSubmissionErrorKeepsGenericNonValidation400(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "protocol-generic-400", `
		export const protocols = {openai_responses: {
			renderEvents: function() { return {events: [], done: false}; },
			renderFinal: function() { return {}; }
		}};
	`)
	c, recorder := newPluginProtocolTestContext(false, false)
	deps := pluginProtocolTestDeps()
	deps.submit = func(*gin.Context, *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return nil, &dto.TaskError{
			Code:       "task_not_exist",
			Message:    "task_origin_not_exist",
			StatusCode: http.StatusBadRequest,
			LocalError: true,
		}
	}

	serveTaskPluginProtocol(c, pinned, deps)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "Invalid task protocol request")
	assert.NotContains(t, recorder.Body.String(), "task_origin_not_exist")
}

func compilePluginProtocolTestEndpoint(t *testing.T, key, source string) pluginruntime.PinnedEndpoint {
	t.Helper()
	return compilePluginProtocolTestEndpointWithOptions(t, key, source, pluginruntime.Options{})
}

func compilePluginProtocolTestEndpointWithOptions(
	t *testing.T,
	key string,
	source string,
	options pluginruntime.Options,
) pluginruntime.PinnedEndpoint {
	t.Helper()
	options.Key = key
	options.Version = "1.0.0"
	options.Concurrency = 1
	engine, err := pluginruntime.Compile(source, options)
	require.NoError(t, err)
	return pluginruntime.PinnedEndpoint{
		Generation: &pluginruntime.RoutingGeneration{Number: 41},
		Plugin: &pluginruntime.LoadedPlugin{
			Meta: pluginruntime.Meta{
				Key:     key,
				Version: "1.0.0",
				Protocols: []pluginruntime.ProtocolClaim{{
					Name:     "openai_responses",
					Supports: []string{"stream", "sync", "background"},
				}},
			},
			Engine: engine,
		},
		Protocol:  "openai_responses",
		Operation: pluginruntime.HostProtocolOperation{Name: "create", Methods: []string{http.MethodPost}, Path: "/v1/responses", BodyKinds: []pluginruntime.BodyKind{pluginruntime.BodyJSON}, ModelField: "model"},
		Model:     "video-model",
	}
}

func newPluginProtocolTestContext(stream, requestBodyStream bool) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	common.SetContextKey(c, constant.ContextKeyUserId, 71)
	common.SetContextKey(c, constant.ContextKeyTokenId, 81)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
	c.Set("resolved_task_model", "video-model")
	c.Set(pluginruntime.ContextKeyProtocolRequest, pluginruntime.ProtocolRequestContext{
		RouteRequestContext: pluginruntime.RouteRequestContext{
			Path:   "/v1/responses",
			Method: http.MethodPost,
			Params: map[string]string{},
			Query:  map[string][]string{},
			Body: map[string]any{
				"kind":  "json",
				"value": map[string]any{"model": "video-model", "stream": requestBodyStream},
			},
			RequestBody: map[string]any{
				"model":  "video-model",
				"stream": requestBodyStream,
			},
		},
		Protocol: "openai_responses",
		Stream:   stream,
	})
	return c, recorder
}

func pluginProtocolTestDeps() pluginProtocolBridgeDeps {
	return pluginProtocolBridgeDeps{
		now:                func() time.Time { return time.Unix(1_710_000_000, 0) },
		admissions:         newPluginProtocolObservationLimiter(defaultPluginProtocolObservationLimits),
		protocolLimits:     relay.DefaultPluginProtocolLimits(),
		observationTimeout: time.Hour,
		tickInterval:       time.Nanosecond,
		tickJitter:         0,
		heartbeatInterval:  time.Hour,
		admissionTimeout:   time.Second,
	}
}

func pluginProtocolTestOutcome(
	info *relaycommon.RelayInfo,
	pluginKey string,
	taskID string,
	_ ...any,
) *taskSubmissionOutcome {
	return &taskSubmissionOutcome{
		Result: &relay.TaskSubmitResult{},
		Task: &model.Task{
			TaskID:    taskID,
			Platform:  constant.TaskPlatform(pluginKey),
			UserId:    info.UserId,
			Status:    model.TaskStatusSubmitted,
			CreatedAt: 1_710_000_000,
		},
		RelayInfo: info,
	}
}

func pluginProtocolTestSSEEventTypes(body string) []string {
	lines := strings.Split(body, "\n")
	events := make([]string, 0)
	for _, line := range lines {
		if after, ok := strings.CutPrefix(line, "event: "); ok {
			events = append(events, after)
		}
	}
	return events
}

func setProtocolRequestBackground(c *gin.Context, background bool) {
	request := c.MustGet(pluginruntime.ContextKeyProtocolRequest).(pluginruntime.ProtocolRequestContext)
	if body, ok := request.Body.(map[string]any); ok {
		if value, ok := body["value"].(map[string]any); ok {
			value["background"] = background
		}
	}
}

func compilePluginProtocolRetrieveEndpoint(t *testing.T, key, source string, options pluginruntime.Options) pluginruntime.PinnedEndpoint {
	t.Helper()
	pinned := compilePluginProtocolTestEndpointWithOptions(t, key, source, options)
	pinned.Plugin.Meta.Protocols = []pluginruntime.ProtocolClaim{{Name: "openai_responses", Supports: []string{"stream", "sync", "background"}}}
	return pinned
}

func logsAppender(logs *[]string) pluginruntime.Options {
	if logs == nil {
		return pluginruntime.Options{}
	}
	return pluginruntime.Options{
		Log: func(message string) { *logs = append(*logs, message) },
	}
}

func newPluginProtocolRetrieveContext(responseID string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses/"+responseID, nil)
	c.Params = gin.Params{{Key: "response_id", Value: responseID}}
	common.SetContextKey(c, constant.ContextKeyUserId, 71)
	common.SetContextKey(c, constant.ContextKeyTokenId, 81)
	return c, recorder
}

func pluginProtocolRetrieveDeps(pinned pluginruntime.PinnedEndpoint, task *model.Task, exists bool, err error) pluginProtocolBridgeDeps {
	deps := pluginProtocolTestDeps()
	deps.getByTaskId = func(userId int, taskId string) (*model.Task, bool, error) {
		if !exists {
			return nil, false, err
		}
		if task != nil && (userId != task.UserId || taskId != task.TaskID) {
			return nil, false, err
		}
		return task, task != nil, err
	}
	deps.resolvePlugin = func(constant.TaskPlatform) (*pluginruntime.LoadedPlugin, *pluginruntime.RoutingGeneration, bool) {
		if pinned.Plugin == nil {
			return nil, nil, false
		}
		return pinned.Plugin, pinned.Generation, true
	}
	return deps
}

const imageProtocolTestPlugin = `
export const meta = {apiVersion:1,key:"image-bridge",name:"Image Bridge",version:"1.0.0",author:{name:"Test"},models:["image-model"],fetchMode:"per_task",protocols:["openai_image"]};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/images"};}
export function parseSubmitResponse(ctx,resp){return {taskId:"vendor"};}
export function buildQueryRequest(){return {url:"https://provider.example"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
export const protocols = {openai_image: {
  decodeRequest: function(ctx) { return {kind:"submit", model: ctx.model, requestBody: ctx.body.value}; },
  render: function(ctx, task) {
    const data = (task.data && task.data.images || []).map(function(url) { return {url: url}; });
    return {data: data, vendor: "test"};
  },
}};
`

func imageProtocolTestEndpoint(t *testing.T) pluginruntime.PinnedEndpoint {
	t.Helper()
	engine, err := pluginruntime.Compile(imageProtocolTestPlugin, pluginruntime.Options{Key: "image-bridge", Version: "1.0.0", Concurrency: 1})
	require.NoError(t, err)
	return pluginruntime.PinnedEndpoint{
		Generation: &pluginruntime.RoutingGeneration{Number: 7},
		Plugin:     &pluginruntime.LoadedPlugin{Meta: pluginruntime.Meta{Key: "image-bridge", Version: "1.0.0", Protocols: []pluginruntime.ProtocolClaim{{Name: pluginruntime.ProtocolOpenAIImage}}}, Engine: engine},
		Protocol:   pluginruntime.ProtocolOpenAIImage,
		Operation:  pluginruntime.HostProtocolOperation{Name: "generate", Methods: []string{http.MethodPost}, Path: "/v1/images/generations", BodyKinds: []pluginruntime.BodyKind{pluginruntime.BodyJSON}, ModelField: "model"},
		Model:      "image-model",
	}
}

func newImageProtocolTestContext(responseFormat string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{}`))
	common.SetContextKey(c, constant.ContextKeyUserId, 71)
	common.SetContextKey(c, constant.ContextKeyTokenId, 81)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
	c.Set("resolved_task_model", "image-model")
	value := map[string]any{"model": "image-model", "prompt": "a cat", "n": 2}
	if responseFormat != "" {
		value["response_format"] = responseFormat
	}
	c.Set(pluginruntime.ContextKeyProtocolRequest, pluginruntime.ProtocolRequestContext{
		RouteRequestContext: pluginruntime.RouteRequestContext{Path: "/v1/images/generations", Method: http.MethodPost, Params: map[string]string{}, Query: map[string][]string{}, Body: map[string]any{"kind": "json", "value": value}, RequestBody: value},
		Protocol:            pluginruntime.ProtocolOpenAIImage,
		Operation:           "generate",
		Model:               "image-model",
	})
	return c, recorder
}

func imageProtocolTestOutcome(info *relaycommon.RelayInfo, status model.TaskStatus, images ...string) *taskSubmissionOutcome {
	task := &model.Task{TaskID: "task_image", Platform: constant.TaskPlatform("image-bridge"), UserId: info.UserId, ChannelId: 3, Status: status, CreatedAt: 1_710_000_000}
	task.PrivateData.UpstreamTaskID = "vendor"
	if len(images) > 0 {
		task.SetData(map[string]any{"images": images})
	}
	return &taskSubmissionOutcome{Result: &relay.TaskSubmitResult{}, Task: task, RelayInfo: info}
}

// The OpenAI Images protocol answers synchronously: an immediate result is
// rendered by the plugin, `created` is host-filled, and response_format
// b64_json inlines each URL while keeping the URL.
func TestServeTaskPluginImageProtocolRendersImmediateResultAndInlinesBase64(t *testing.T) {
	for _, tc := range []struct {
		name, responseFormat string
		downloadErr          error
		wantB64              []any
	}{
		{"url", "", nil, []any{nil, nil}},
		{"b64_json", "b64_json", nil, []any{"QUFB", "QUFB"}},
		{"b64_json download failure keeps the URL", "b64_json", errors.New("blocked"), []any{nil, nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinned := imageProtocolTestEndpoint(t)
			c, recorder := newImageProtocolTestContext(tc.responseFormat)
			deps := pluginProtocolTestDeps()
			downloads := 0
			deps.downloadImage = func(url string) (string, string, error) {
				downloads++
				if tc.downloadErr != nil {
					return "", "", tc.downloadErr
				}
				return "image/png", "QUFB", nil
			}
			deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
				return imageProtocolTestOutcome(info, model.TaskStatusSuccess, "https://cdn.example/1.png", "https://cdn.example/2.png"), nil
			}
			serveTaskPluginImageProtocol(c, pinned, deps)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var response map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.Equal(t, float64(1_710_000_000), response["created"])
			assert.Equal(t, "test", response["vendor"], "plugin fields are preserved")
			data, ok := response["data"].([]any)
			require.True(t, ok)
			require.Len(t, data, 2)
			for index, entry := range data {
				item := entry.(map[string]any)
				assert.Equal(t, fmt.Sprintf("https://cdn.example/%d.png", index+1), item["url"])
				assert.Equal(t, tc.wantB64[index], item["b64_json"])
			}
			if tc.responseFormat == "b64_json" {
				assert.Equal(t, 2, downloads)
			} else {
				assert.Zero(t, downloads)
			}
		})
	}
}

// An asynchronous vendor task is polled inside the request until it is
// terminal; a failure and the protocol timeout become OpenAI error envelopes
// while the durable task stays with the background poller.
func TestServeTaskPluginImageProtocolWaitsForAsynchronousTask(t *testing.T) {
	for _, tc := range []struct {
		name        string
		terminal    model.TaskStatus
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{"success", model.TaskStatusSuccess, http.StatusOK, "", ""},
		{"failure", model.TaskStatusFailure, http.StatusBadRequest, "image_generation_failed", "DataInspectionFailed"},
		{"timeout", "", http.StatusGatewayTimeout, "task_timeout", "still running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinned := imageProtocolTestEndpoint(t)
			c, recorder := newImageProtocolTestContext("")
			deps := pluginProtocolTestDeps()
			deps.imagePollInterval = time.Millisecond
			deps.submissionTimeout = 50 * time.Millisecond
			polls := 0
			deps.pollTask = func(_ context.Context, task *model.Task) error {
				polls++
				if polls == 1 {
					return errors.New("transient poll failure")
				}
				if polls < 3 || tc.terminal == "" {
					return nil
				}
				task.Status = tc.terminal
				task.FailReason = "DataInspectionFailed"
				task.SetData(map[string]any{"images": []string{"https://cdn.example/polled.png"}})
				return nil
			}
			deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
				return imageProtocolTestOutcome(info, model.TaskStatusSubmitted), nil
			}
			serveTaskPluginImageProtocol(c, pinned, deps)
			require.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
			var response map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			if tc.wantStatus == http.StatusOK {
				assert.GreaterOrEqual(t, polls, 3)
				assert.Equal(t, []any{map[string]any{"url": "https://cdn.example/polled.png"}}, response["data"])
				return
			}
			errorBody := response["error"].(map[string]any)
			assert.Equal(t, tc.wantCode, errorBody["code"])
			assert.Contains(t, errorBody["message"], tc.wantMessage)
		})
	}
}

// Submission failures use the OpenAI error envelope and surface a DashScope
// {code, message} failure body as a readable vendor message.
func TestServeTaskPluginImageProtocolSubmissionErrorUsesOpenAIEnvelope(t *testing.T) {
	pinned := imageProtocolTestEndpoint(t)
	c, recorder := newImageProtocolTestContext("")
	deps := pluginProtocolTestDeps()
	deps.submit = func(*gin.Context, *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return nil, service.TaskErrorWrapper(errors.New(`{"code":"InvalidParameter","message":"n must be 1","request_id":"r"}`), "fail_to_fetch_task", http.StatusBadRequest)
	}
	serveTaskPluginImageProtocol(c, pinned, deps)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	errorBody := response["error"].(map[string]any)
	assert.Equal(t, "InvalidParameter", errorBody["code"])
	assert.Equal(t, "invalid_request_error", errorBody["type"])
	assert.Contains(t, errorBody["message"], "n must be 1")
	assert.NotContains(t, errorBody["message"], "request_id")
}

// A synchronous image submission is billable the moment DashScope accepts it.
// A client that disconnects while the upstream call is in flight must not
// cancel that call or refund a durable result: the task row, its settlement
// and its consume log stay, and nothing is written to the closed connection.
func TestServeTaskPluginImageProtocolDisconnectDuringSubmissionKeepsDurableSettlement(t *testing.T) {
	events := make([]string, 0, 3)
	database := setupTaskSubmissionDatabase(t, true, &events)
	previousLogConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = false
	t.Cleanup(func() { common.LogConsumeEnabled = previousLogConsumeEnabled })

	pinned := imageProtocolTestEndpoint(t)
	c, recorder := newImageProtocolTestContext("")
	c.Set(pluginruntime.ContextKeyPinnedEndpoint, pinned)
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	billing := &taskSubmissionTestBilling{events: &events}
	submitStarted := make(chan struct{})
	releaseSubmit := make(chan struct{})
	done := make(chan struct{})
	polls := 0

	deps := pluginProtocolTestDeps()
	deps.pollTask = func(context.Context, *model.Task) error {
		polls++
		return nil
	}
	deps.submit = func(c *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		info.Billing = billing
		info.TaskRelayInfo.PublicTaskID = "task_image_disconnect"
		info.TaskRelayInfo.LockedChannel = &model.Channel{Id: 1, Type: constant.ChannelTypeTaskPlugin, Name: "image-bridge"}
		info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 1, ChannelType: constant.ChannelTypeTaskPlugin}
		return executeTaskSubmissionWith(c, info, func(c *gin.Context, _ *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
			close(submitStarted)
			<-releaseSubmit
			// The upstream call runs on the request context: a context that
			// followed the client would abort here and refund.
			if requestErr := c.Request.Context().Err(); requestErr != nil {
				return nil, service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
			}
			return &relay.TaskSubmitResult{
				UpstreamTaskID: "task_image_disconnect",
				Platform:       constant.TaskPlatform(pinned.Plugin.Meta.Key),
				Quota:          7,
				TaskData:       []byte(`{"images":["https://cdn.example/1.png"]}`),
				Immediate:      &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, Progress: "100%"},
			}, nil
		})
	}

	go func() {
		defer close(done)
		serveTaskPluginImageProtocol(c, pinned, deps)
	}()
	select {
	case <-submitStarted:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "submission did not start")
	}
	cancel()
	close(releaseSubmit)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "detached submission did not finish")
	}

	assert.Equal(t, []string{"reserve", "insert", "settle"}, events)
	assert.Zero(t, billing.refunds)
	var persisted model.Task
	require.NoError(t, database.Where("task_id = ?", "task_image_disconnect").First(&persisted).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), persisted.Status)
	assert.Equal(t, 7, persisted.Quota)
	assert.True(t, persisted.PrivateData.ResultDiscarded, "the inline image result is not retained after the barrier")
	assert.Zero(t, polls, "an immediate result is never polled")
	assert.False(t, c.Writer.Written())
	assert.Empty(t, recorder.Body.String())
}
