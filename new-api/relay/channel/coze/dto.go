package coze

import "encoding/json"

type CozeError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type CozeEnterMessage struct {
	Role        string          `json:"role"`
	Type        string          `json:"type,omitempty"`
	Content     any             `json:"content,omitempty"`
	MetaData    json.RawMessage `json:"meta_data,omitempty"`
	ContentType string          `json:"content_type,omitempty"`
}

type CozeChatRequest struct {
	BotId              string             `json:"bot_id"`
	UserId             json.RawMessage    `json:"user_id"`
	AdditionalMessages []CozeEnterMessage `json:"additional_messages,omitempty"`
	Stream             bool               `json:"stream,omitempty"`
	CustomVariables    json.RawMessage    `json:"custom_variables,omitempty"`
	AutoSaveHistory    bool               `json:"auto_save_history,omitempty"`
	MetaData           json.RawMessage    `json:"meta_data,omitempty"`
	ExtraParams        json.RawMessage    `json:"extra_params,omitempty"`
	ShortcutCommand    json.RawMessage    `json:"shortcut_command,omitempty"`
	Parameters         json.RawMessage    `json:"parameters,omitempty"`
}

type CozeChatResponse struct {
	Code int                  `json:"code"`
	Msg  string               `json:"msg"`
	Data CozeChatResponseData `json:"data"`
}

type CozeChatResponseData struct {
	Id             string        `json:"id"`
	ConversationId string        `json:"conversation_id"`
	BotId          string        `json:"bot_id"`
	CreatedAt      int64         `json:"created_at"`
	LastError      CozeError     `json:"last_error"`
	Status         string        `json:"status"`
	Usage          CozeChatUsage `json:"usage"`
}

type CozeChatUsage struct {
	TokenCount  int `json:"token_count"`
	OutputCount int `json:"output_count"`
	InputCount  int `json:"input_count"`
}

type CozeChatDetailResponse struct {
	Data   []CozeChatV3MessageDetail `json:"data"`
	Code   int                       `json:"code"`
	Msg    string                    `json:"msg"`
	Detail CozeResponseDetail        `json:"detail"`
}

type CozeChatV3MessageDetail struct {
	Id               string          `json:"id"`
	Role             string          `json:"role"`
	Type             string          `json:"type"`
	BotId            string          `json:"bot_id"`
	ChatId           string          `json:"chat_id"`
	Content          json.RawMessage `json:"content"`
	MetaData         json.RawMessage `json:"meta_data"`
	CreatedAt        int64           `json:"created_at"`
	SectionId        string          `json:"section_id"`
	UpdatedAt        int64           `json:"updated_at"`
	ContentType      string          `json:"content_type"`
	ConversationId   string          `json:"conversation_id"`
	ReasoningContent string          `json:"reasoning_content"`
}

type CozeResponseDetail struct {
	Logid string `json:"logid"`
}
