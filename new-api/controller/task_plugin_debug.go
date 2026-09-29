package controller

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// taskPluginSubmitDiagnostics keeps plugin-only lifecycle logging out of the
// ordinary task path. An empty plugin key makes every method a no-op.
type taskPluginSubmitDiagnostics struct {
	context    *gin.Context
	pluginKey  string
	generation uint64
}

func newTaskPluginSubmitDiagnostics(c *gin.Context) taskPluginSubmitDiagnostics {
	diagnostics := taskPluginSubmitDiagnostics{
		context:   c,
		pluginKey: c.GetString("expected_task_plugin_key"),
	}
	if diagnostics.pluginKey == "" {
		return diagnostics
	}
	if pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedPlugin); exists {
		if pinned, ok := pinnedValue.(pluginruntime.PinnedPlugin); ok && pinned.Generation != nil {
			diagnostics.generation = pinned.Generation.Number
		}
	}
	return diagnostics
}

func (d taskPluginSubmitDiagnostics) start(info *relaycommon.RelayInfo) {
	if d.pluginKey == "" {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=start generation=%d plugin=%q model=%q action_present=%t",
		d.generation,
		d.pluginKey,
		info.OriginModelName,
		info.Action != "",
	)
}

func (d taskPluginSubmitDiagnostics) refund(stage string) {
	if d.pluginKey == "" {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=refund_invoked generation=%d plugin=%q stage=%q durable=false",
		d.generation,
		d.pluginKey,
		stage,
	)
}

func (d taskPluginSubmitDiagnostics) cancelled(stage string, attempt int) {
	if d.pluginKey == "" {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=cancelled generation=%d plugin=%q stage=%q attempt=%d",
		d.generation,
		d.pluginKey,
		stage,
		attempt,
	)
}

func (d taskPluginSubmitDiagnostics) attempt(attempt int, channel *model.Channel, locked bool) {
	if d.pluginKey == "" || channel == nil {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=attempt generation=%d plugin=%q attempt=%d channel_id=%d channel_type=%d locked=%t",
		d.generation,
		d.pluginKey,
		attempt,
		channel.Id,
		channel.Type,
		locked,
	)
}

func (d taskPluginSubmitDiagnostics) attemptSucceeded(attempt int, result *relay.TaskSubmitResult) {
	if d.pluginKey == "" || result == nil {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=attempt_succeeded generation=%d plugin=%q attempt=%d platform=%q quota=%d task_data_bytes=%d client_response=%t immediate=%t",
		d.generation,
		d.pluginKey,
		attempt,
		result.Platform,
		result.Quota,
		len(result.TaskData),
		result.ClientResponse != nil,
		result.Immediate != nil,
	)
}

func (d taskPluginSubmitDiagnostics) attemptFailed(attempt int, channel *model.Channel, taskErr *dto.TaskError, willRetry bool) {
	if d.pluginKey == "" || channel == nil || taskErr == nil {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=attempt_failed generation=%d plugin=%q attempt=%d channel_id=%d channel_type=%d code=%q status=%d local=%t will_retry=%t",
		d.generation,
		d.pluginKey,
		attempt,
		channel.Id,
		channel.Type,
		taskErr.Code,
		taskErr.StatusCode,
		taskErr.LocalError,
		willRetry,
	)
}

func (d taskPluginSubmitDiagnostics) failed(stage, reason string, taskErr *dto.TaskError, durable bool) {
	if d.pluginKey == "" {
		return
	}
	code := ""
	status := 0
	local := true
	if taskErr != nil {
		code = taskErr.Code
		status = taskErr.StatusCode
		local = taskErr.LocalError
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=failed generation=%d plugin=%q stage=%q reason=%q code=%q status=%d local=%t durable=%t",
		d.generation,
		d.pluginKey,
		stage,
		reason,
		code,
		status,
		local,
		durable,
	)
}

func (d taskPluginSubmitDiagnostics) reserve(event string, quota int) {
	if d.pluginKey == "" {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=%s generation=%d plugin=%q quota=%d",
		event,
		d.generation,
		d.pluginKey,
		quota,
	)
}

func (d taskPluginSubmitDiagnostics) insertStart(task *model.Task) {
	if d.pluginKey == "" || task == nil {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=insert_start generation=%d plugin=%q public_task_id=%q platform=%q channel_id=%d quota=%d",
		d.generation,
		d.pluginKey,
		task.TaskID,
		task.Platform,
		task.ChannelId,
		task.Quota,
	)
}

func (d taskPluginSubmitDiagnostics) durable(task *model.Task) {
	if d.pluginKey == "" || task == nil {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=durable generation=%d plugin=%q public_task_id=%q status=%q durable=true",
		d.generation,
		d.pluginKey,
		task.TaskID,
		taskPluginDebugStatus(string(task.Status)),
	)
}

func (d taskPluginSubmitDiagnostics) settleStart(task *model.Task, quota int) {
	if d.pluginKey == "" || task == nil {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=settle_start generation=%d plugin=%q public_task_id=%q quota=%d durable=true",
		d.generation,
		d.pluginKey,
		task.TaskID,
		quota,
	)
}

func (d taskPluginSubmitDiagnostics) complete(task *model.Task, quota int) {
	if d.pluginKey == "" || task == nil {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=complete generation=%d plugin=%q public_task_id=%q quota=%d durable=true",
		d.generation,
		d.pluginKey,
		task.TaskID,
		quota,
	)
}

func (d taskPluginSubmitDiagnostics) present(task *model.Task, presenter string) {
	if d.pluginKey == "" || task == nil {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=present generation=%d plugin=%q public_task_id=%q presenter=%q durable=true",
		d.generation,
		d.pluginKey,
		task.TaskID,
		presenter,
	)
}

func (d taskPluginSubmitDiagnostics) presentError(taskErr *dto.TaskError) {
	if d.pluginKey == "" || taskErr == nil {
		return
	}
	logger.LogDebug(
		d.context,
		"task_plugin subsystem=submit event=present_error generation=%d plugin=%q code=%q status=%d local=%t",
		d.generation,
		d.pluginKey,
		taskErr.Code,
		taskErr.StatusCode,
		taskErr.LocalError,
	)
}

func taskPluginDebugStatus(status string) string {
	switch model.TaskStatus(status) {
	case model.TaskStatusSubmitted,
		model.TaskStatusQueued,
		model.TaskStatusInProgress,
		model.TaskStatusSuccess,
		model.TaskStatusFailure:
		return status
	default:
		return "unknown"
	}
}
