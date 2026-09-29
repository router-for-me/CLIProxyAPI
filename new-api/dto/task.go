package dto

import (
	"encoding/json"
)

type TaskError struct {
	// NoRetry prevents duplicate upstream work after a response has been accepted.
	NoRetry    bool   `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Data       any    `json:"data"`
	StatusCode int    `json:"-"`
	LocalError bool   `json:"-"`
	Error      error  `json:"-"`
}

type TaskData interface {
	SunoDataResponse | []SunoDataResponse | string | any
}

const TaskSuccessCode = "success"

type TaskResponse[T TaskData] struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

func (t *TaskResponse[T]) IsSuccess() bool {
	return t.Code == TaskSuccessCode
}

type TaskDto struct {
	ID                   int64  `json:"id"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
	TaskID               string `json:"task_id"`
	Platform             string `json:"platform"`
	UserId               int    `json:"user_id"`
	Group                string `json:"group"`
	ChannelId            int    `json:"channel_id"`
	Quota                int    `json:"quota"`
	Action               string `json:"action"`
	Status               string `json:"status"`
	FailReason           string `json:"fail_reason"`
	ResultURL            string `json:"result_url,omitempty"` // 任务结果 URL（视频地址等）
	LegacyVideoAvailable bool   `json:"legacy_video_available,omitempty"`
	// ResultDiscarded marks a synchronous result that was returned inline and
	// never persisted; the UI must not offer artifact retrieval for it.
	ResultDiscarded bool            `json:"result_discarded,omitempty"`
	SubmitTime      int64           `json:"submit_time"`
	StartTime       int64           `json:"start_time"`
	FinishTime      int64           `json:"finish_time"`
	Progress        string          `json:"progress"`
	Properties      any             `json:"properties"`
	Username        string          `json:"username,omitempty"`
	Data            json.RawMessage `json:"data"`
	AdminInfo       *TaskAdminInfo  `json:"admin_info,omitempty"`
	RootInfo        *TaskRootInfo   `json:"root_info,omitempty"`
}

type TaskPluginInfo struct {
	Key     string                `json:"key"`
	Name    string                `json:"name"`
	Version string                `json:"version,omitempty"`
	Author  *TaskPluginAuthorInfo `json:"author,omitempty"`
}

type TaskPluginAuthorInfo struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type TaskPluginRuntimeInfo struct {
	Key        string `json:"key"`
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
	Generation uint64 `json:"generation"`
}

type TaskAdminInfo struct {
	RequestID   string          `json:"request_id,omitempty"`
	RequestPath string          `json:"request_path,omitempty"`
	TaskPlugin  *TaskPluginInfo `json:"task_plugin,omitempty"`
}

type TaskRootInfo struct {
	TaskPlugin     *TaskPluginRuntimeInfo `json:"task_plugin,omitempty"`
	UpstreamTaskID string                 `json:"upstream_task_id,omitempty"`
	NodeName       string                 `json:"node_name,omitempty"`
}

type FetchReq struct {
	IDs []string `json:"ids"`
}
