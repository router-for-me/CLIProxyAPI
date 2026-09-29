package types

type FileType string

const (
	FileTypeImage FileType = "image" // Image file type
	FileTypeAudio FileType = "audio" // Audio file type
	FileTypeVideo FileType = "video" // Video file type
	FileTypeFile  FileType = "file"  // Generic file type
)

type TokenType string

const (
	TokenTypeTextNumber TokenType = "text_number" // Text or number tokens
	TokenTypeTokenizer  TokenType = "tokenizer"   // Tokenizer tokens
	TokenTypeImage      TokenType = "image"       // Image tokens
)

type TokenCountMeta struct {
	TokenType     TokenType   `json:"token_type,omitempty"`     // Type of tokens used in the request
	CombineText   string      `json:"combine_text,omitempty"`   // Combined text from all messages
	ToolsCount    int         `json:"tools_count,omitempty"`    // Number of tools used
	NameCount     int         `json:"name_count,omitempty"`     // Number of names in the request
	MessagesCount int         `json:"messages_count,omitempty"` // Number of messages in the request
	Files         []*FileMeta `json:"files,omitempty"`          // List of files, each with type and content
	MaxTokens     int         `json:"max_tokens,omitempty"`     // Maximum tokens allowed in the request

	ImagePriceRatio float64            `json:"image_ratio,omitempty"`    // Ratio for image size, if applicable
	BillingRatios   map[string]float64 `json:"billing_ratios,omitempty"` // Validated request multipliers used by pre-consume billing
	//IsStreaming   bool        `json:"is_streaming,omitempty"`   // Indicates if the request is streaming
}

type FileMeta struct {
	FileType
	Source FileSource // 统一的文件来源（URL 或 base64）
	Detail string     // 图片细节级别（low/high/auto）
}

// NewFileMeta 创建新的 FileMeta
func NewFileMeta(fileType FileType, source FileSource) *FileMeta {
	return &FileMeta{
		FileType: fileType,
		Source:   source,
	}
}

// NewImageFileMeta 创建图片类型的 FileMeta
func NewImageFileMeta(source FileSource, detail string) *FileMeta {
	return &FileMeta{
		FileType: FileTypeImage,
		Source:   source,
		Detail:   detail,
	}
}

// GetIdentifier 获取文件标识符（用于日志）
func (f *FileMeta) GetIdentifier() string {
	if f.Source != nil {
		return f.Source.GetIdentifier()
	}
	return "unknown"
}

// IsURL 判断是否是 URL 来源
func (f *FileMeta) IsURL() bool {
	return f.Source != nil && f.Source.IsURL()
}

// GetRawData 获取原始数据（兼容旧代码）
// Deprecated: 请使用 Source.GetRawData()
func (f *FileMeta) GetRawData() string {
	if f.Source != nil {
		return f.Source.GetRawData()
	}
	return ""
}

type RequestMeta struct {
	OriginalModelName string `json:"original_model_name"`
	UserUsingGroup    string `json:"user_using_group"`
	PromptTokens      int    `json:"prompt_tokens"`
	PreConsumedQuota  int    `json:"pre_consumed_quota"`
}
