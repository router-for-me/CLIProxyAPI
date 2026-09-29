package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskPluginResponsesNonStreamDecodesWithOfficialGoSDK(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "official-sdk-non-stream", `
		export const protocols = {openai_responses: {
			renderEvents: function() { throw new Error("non-stream called renderEvents"); },
			renderFinal: function() {
				return {
					output: [{
						type: "message",
						status: "completed",
						role: "assistant",
						content: [{
							type: "output_text",
							text: "official-sdk-final",
							annotations: [],
							logprobs: []
						}]
					}]
				};
			}
		}};
	`)
	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_sdk_final", nil), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		return &model.Task{
			TaskID:   "task_sdk_final",
			UserId:   71,
			Platform: constant.TaskPlatform(pinned.Plugin.Meta.Key),
			Status:   model.TaskStatusSuccess,
		}, true, nil
	}
	server := newPluginProtocolSDKTestServer(t, pinned, deps)
	defer server.Close()
	client := openai.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(server.URL+"/v1/"),
		option.WithMaxRetries(0),
	)
	requestContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	response, err := client.Responses.New(requestContext, responses.ResponseNewParams{
		Model: shared.ResponsesModel("video-model"),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String("create a video"),
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "resp_sdk_final", response.ID)
	assert.Equal(t, responses.ResponseStatusCompleted, response.Status)
	assert.Equal(t, "video-model", response.Model)
	assert.Equal(t, "official-sdk-final", response.OutputText())
	assert.Equal(t, "task_sdk_final", response.Metadata["task_id"])
}

func TestTaskPluginResponsesStreamDecodesWithOfficialGoSDK(t *testing.T) {
	pinned := compilePluginProtocolTestEndpoint(t, "official-sdk-stream", `
		export const protocols = {openai_responses: {
			renderEvents: function() {
				return {events: [{type: "output", data: "official-sdk-stream"}], done: true};
			},
			renderFinal: function() { throw new Error("stream called renderFinal"); }
		}};
	`)
	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, pinned.Plugin.Meta.Key, "task_sdk_stream", nil), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		return &model.Task{
			TaskID:   "task_sdk_stream",
			UserId:   71,
			Platform: constant.TaskPlatform(pinned.Plugin.Meta.Key),
			Status:   model.TaskStatusSuccess,
		}, true, nil
	}
	server := newPluginProtocolSDKTestServer(t, pinned, deps)
	defer server.Close()
	client := openai.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(server.URL+"/v1/"),
		option.WithMaxRetries(0),
	)
	requestContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := client.Responses.NewStreaming(requestContext, responses.ResponseNewParams{
		Model: shared.ResponsesModel("video-model"),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String("create a video"),
		},
	})
	defer stream.Close()
	eventTypes := make([]string, 0, 8)
	sequenceNumbers := make([]int64, 0, 8)
	var completedText string
	for stream.Next() {
		event := stream.Current()
		eventTypes = append(eventTypes, event.Type)
		sequenceNumbers = append(sequenceNumbers, event.SequenceNumber)
		if event.Type == "response.completed" {
			completedText = event.Response.OutputText()
		}
	}

	require.NoError(t, stream.Err())
	assert.Equal(t, []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}, eventTypes)
	assert.Equal(t, []int64{0, 1, 2, 3, 4, 5, 6, 7}, sequenceNumbers)
	assert.Equal(t, "official-sdk-stream", completedText)
}

func TestBuiltInKlingResponsesNonStreamDecodesWithOfficialGoSDK(t *testing.T) {
	pinned, deps := builtInKlingProtocolSDKFixture(t, "task_kling_sdk_final")
	server := newPluginProtocolSDKTestServer(t, pinned, deps)
	defer server.Close()
	client := openai.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(server.URL+"/v1/"),
		option.WithMaxRetries(0),
	)
	requestContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	response, err := client.Responses.New(requestContext, responses.ResponseNewParams{
		Model: shared.ResponsesModel("kling-v2-master"),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String("camera orbit"),
		},
	})

	require.NoError(t, err)
	assert.Equal(t, responses.ResponseStatusCompleted, response.Status)
	assert.Equal(t, "kling-v2-master", response.Model)
	assert.Contains(t, response.OutputText(), "https://gateway.example/v1/tasks/task_kling_sdk_final/artifacts/video/content")
	assert.NotContains(t, response.OutputText(), "upstream.example")
	assert.Equal(t, "kling", response.Metadata["vendor"])
	assert.Equal(t, "task_kling_sdk_final", response.Metadata["task_id"])
}

func TestBuiltInKlingResponsesStreamDecodesWithOfficialGoSDK(t *testing.T) {
	pinned, deps := builtInKlingProtocolSDKFixture(t, "task_kling_sdk_stream")
	server := newPluginProtocolSDKTestServer(t, pinned, deps)
	defer server.Close()
	client := openai.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(server.URL+"/v1/"),
		option.WithMaxRetries(0),
	)
	requestContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := client.Responses.NewStreaming(requestContext, responses.ResponseNewParams{
		Model: shared.ResponsesModel("kling-v2-master"),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String("camera orbit"),
		},
	})
	defer stream.Close()
	eventTypes := make([]string, 0, 8)
	sequenceNumbers := make([]int64, 0, 8)
	var completedText string
	for stream.Next() {
		event := stream.Current()
		eventTypes = append(eventTypes, event.Type)
		sequenceNumbers = append(sequenceNumbers, event.SequenceNumber)
		if event.Type == "response.completed" {
			completedText = event.Response.OutputText()
		}
	}

	require.NoError(t, stream.Err())
	assert.Equal(t, []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}, eventTypes)
	assert.Equal(t, []int64{0, 1, 2, 3, 4, 5, 6, 7}, sequenceNumbers)
	assert.Contains(t, completedText, "https://gateway.example/v1/tasks/task_kling_sdk_stream/artifacts/video/content")
	assert.NotContains(t, completedText, "upstream.example")
}

func builtInKlingProtocolSDKFixture(t *testing.T, taskID string) (pluginruntime.PinnedEndpoint, pluginProtocolBridgeDeps) {
	t.Helper()
	source, err := builtinplugins.Source("kling")
	require.NoError(t, err)
	registry := pluginruntime.NewRegistry()
	plugin, err := registry.RegisterFactory(source, pluginruntime.Options{Key: "kling"})
	require.NoError(t, err)
	binding, found := registry.Generation().LookupEndpoint(http.MethodPost, "/v1/responses", "kling-v2-master")
	require.True(t, found)
	pinned := pluginruntime.PinnedEndpoint{
		Generation: registry.Generation(),
		Plugin:     plugin,
		Protocol:   binding.Protocol,
		Operation:  binding.Operation,
		Model:      binding.Model,
		Candidates: []pluginruntime.ProtocolBinding{binding},
	}
	deps := pluginProtocolTestDeps()
	deps.submit = func(_ *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
		return pluginProtocolTestOutcome(info, plugin.Meta.Key, taskID, nil), nil
	}
	deps.loadTask = func(context.Context, int, constant.TaskPlatform, string) (*model.Task, bool, error) {
		task := &model.Task{
			TaskID: taskID, Platform: constant.TaskPlatform(plugin.Meta.Key), UserId: 71,
			Status: model.TaskStatusSuccess, Progress: "100%", CreatedAt: 1_710_000_000,
		}
		task.SetData(map[string]any{
			"code": 0,
			"data": map[string]any{
				"task_id":     "upstream-private",
				"task_status": "succeed",
				"task_result": map[string]any{
					"videos": []any{map[string]any{"url": "https://upstream.example/private-video.mp4"}},
				},
			},
		})
		return task, true, nil
	}
	deps.artifactContentURL = func(publicTaskID, artifactKey string) (string, error) {
		return "https://gateway.example/v1/tasks/" + publicTaskID + "/artifacts/" + artifactKey + "/content", nil
	}
	return pinned, deps
}

func newPluginProtocolSDKTestServer(
	t *testing.T,
	pinned pluginruntime.PinnedEndpoint,
	deps pluginProtocolBridgeDeps,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" {
			http.NotFound(writer, request)
			return
		}
		var requestBody map[string]any
		if err := common.DecodeJson(request.Body, &requestBody); err != nil {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		modelName, _ := requestBody["model"].(string)
		stream, _ := requestBody["stream"].(bool)
		c, _ := gin.CreateTestContext(writer)
		c.Request = request
		common.SetContextKey(c, constant.ContextKeyUserId, 71)
		common.SetContextKey(c, constant.ContextKeyTokenId, 81)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		c.Set("resolved_task_model", modelName)
		c.Set(pluginruntime.ContextKeyProtocolRequest, pluginruntime.ProtocolRequestContext{
			RouteRequestContext: pluginruntime.RouteRequestContext{
				Path:        request.URL.Path,
				Method:      request.Method,
				Params:      map[string]string{},
				Query:       request.URL.Query(),
				RequestBody: requestBody,
			},
			Protocol: pinned.Protocol,
			Stream:   stream,
		})
		serveTaskPluginProtocol(c, pinned, deps)
	}))
}
