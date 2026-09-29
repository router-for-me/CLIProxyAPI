package dto

type TaskPluginError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"httpStatus"`
	Retryable  bool   `json:"retryable"`
}

// TaskView is the only persisted-task shape exposed to JavaScript plugins.
// It deliberately excludes ownership, channel, quota, properties, and private
// upstream identifiers.
type TaskView struct {
	TaskID     string `json:"task_id"`
	Platform   string `json:"platform"`
	Status     string `json:"status"`
	Progress   string `json:"progress"`
	FailReason string `json:"fail_reason"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	Data       any    `json:"data,omitempty"`
}
