package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskPluginSubmitDiagnosticsArePluginOnlyAndDoNotLogPayloads(t *testing.T) {
	previousDebug := common.DebugEnabled
	common.DebugEnabled = true
	t.Cleanup(func() { common.DebugEnabled = previousDebug })

	var output bytes.Buffer
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &output
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previousWriter
		common.LogWriterMu.Unlock()
	})

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/tasks/debug-plugin", nil)
	c.Set(common.RequestIdKey, "plugin-submit-request")
	info := &relaycommon.RelayInfo{
		OriginModelName: "safe-model",
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{Action: "https://private-action.invalid/?key=hidden"},
	}

	newTaskPluginSubmitDiagnostics(c).start(info)
	assert.Empty(t, output.String())

	c.Set("expected_task_plugin_key", "debug-plugin")
	diagnostics := newTaskPluginSubmitDiagnostics(c)
	diagnostics.start(info)
	diagnostics.attemptSucceeded(1, &relay.TaskSubmitResult{
		UpstreamTaskID: "private-upstream-canary",
		TaskData:       []byte("private-task-data-canary"),
		ClientResponse: map[string]any{"secret": "private-client-response-canary"},
		Platform:       constant.TaskPlatform("debug-plugin"),
		Quota:          12,
	})
	task := &model.Task{
		TaskID:   "public-task-id",
		Platform: constant.TaskPlatform("debug-plugin"),
		Status:   model.TaskStatus("https://private-status.invalid/?key=hidden"),
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: "private-task-record-canary",
			ResultURL:      "https://private-url.invalid/result",
		},
	}
	diagnostics.insertStart(task)
	diagnostics.durable(task)
	diagnostics.complete(task, 12)

	logOutput := output.String()
	require.Contains(t, logOutput, "plugin-submit-request")
	assert.Contains(t, logOutput, `plugin="debug-plugin"`)
	assert.Contains(t, logOutput, `public_task_id="public-task-id"`)
	assert.Contains(t, logOutput, "task_data_bytes=24")
	assert.Contains(t, logOutput, "action_present=true")
	assert.Contains(t, logOutput, `status="unknown"`)
	for _, secret := range []string{
		"private-upstream-canary",
		"private-task-data-canary",
		"private-client-response-canary",
		"private-task-record-canary",
		"private-url.invalid",
		"private-action.invalid",
		"private-status.invalid",
		"key=hidden",
	} {
		assert.NotContains(t, logOutput, secret)
	}
}
