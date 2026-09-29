package dto

type VideoRequest struct {
	Model          string         `json:"model,omitempty" example:"kling-v1"`                                                                                                                                    // Model/style ID
	Prompt         string         `json:"prompt,omitempty" example:"宇航员站起身走了"`                                                                                                                                   // Text prompt
	Image          string         `json:"image,omitempty" example:"https://h2.inkwai.com/bs2/upload-ylab-stunt/se/ai_portal_queue_mmu_image_upscale_aiweb/3214b798-e1b4-4b00-b7af-72b5b0417420_raw_image_0.jpg"` // Image input (URL/Base64)
	Duration       float64        `json:"duration" example:"5.0"`                                                                                                                                                // Video duration (seconds)
	Width          int            `json:"width" example:"512"`                                                                                                                                                   // Video width
	Height         int            `json:"height" example:"512"`                                                                                                                                                  // Video height
	Fps            int            `json:"fps,omitempty" example:"30"`                                                                                                                                            // Video frame rate
	Seed           int            `json:"seed,omitempty" example:"20231234"`                                                                                                                                     // Random seed
	N              int            `json:"n,omitempty" example:"1"`                                                                                                                                               // Number of videos to generate
	ResponseFormat string         `json:"response_format,omitempty" example:"url"`                                                                                                                               // Response format
	User           string         `json:"user,omitempty" example:"user-1234"`                                                                                                                                    // User identifier
	Metadata       map[string]any `json:"metadata,omitempty"`                                                                                                                                                    // Vendor-specific/custom params (e.g. negative_prompt, style, quality_level, etc.)
}

// VideoResponse 视频生成提交任务后的响应
type VideoResponse struct {
	TaskId string `json:"task_id"`
	Status string `json:"status"`
}

// VideoTaskResponse 查询视频生成任务状态的响应
type VideoTaskResponse struct {
	TaskId   string             `json:"task_id" example:"abcd1234efgh"` // 任务ID
	Status   string             `json:"status" example:"succeeded"`     // 任务状态
	Url      string             `json:"url,omitempty"`                  // 视频资源URL（成功时）
	Format   string             `json:"format,omitempty" example:"mp4"` // 视频格式
	Metadata *VideoTaskMetadata `json:"metadata,omitempty"`             // 结果元数据
	Error    *VideoTaskError    `json:"error,omitempty"`                // 错误信息（失败时）
}

// VideoTaskMetadata 视频任务元数据
type VideoTaskMetadata struct {
	Duration float64 `json:"duration" example:"5.0"`  // 实际生成的视频时长
	Fps      int     `json:"fps" example:"30"`        // 实际帧率
	Width    int     `json:"width" example:"512"`     // 实际宽度
	Height   int     `json:"height" example:"512"`    // 实际高度
	Seed     int     `json:"seed" example:"20231234"` // 使用的随机种子
}

// VideoTaskError 视频任务错误信息
type VideoTaskError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
