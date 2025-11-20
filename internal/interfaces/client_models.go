// Package interfaces defines the core interfaces and shared structures for the CLI Proxy API server.
// These interfaces provide a common contract for different components of the application,
// such as AI service clients, API handlers, and data models.
package interfaces

import (
	"time"
)

// GCPProject represents the response structure for a Google Cloud project list request.
// This structure is used when fetching available projects for a Google Cloud account.
type GCPProject struct {
	// Projects is a list of Google Cloud projects accessible by the user.
	Projects []GCPProjectProjects `json:"projects"`
}

// GCPProjectLabels defines the labels associated with a GCP project.
// These labels can contain metadata about the project's purpose or configuration.
type GCPProjectLabels struct {
	// GenerativeLanguage indicates if the project has generative language APIs enabled.
	GenerativeLanguage string `json:"generative-language"`
}

// GCPProjectProjects contains details about a single Google Cloud project.
// This includes identifying information, metadata, and configuration details.
type GCPProjectProjects struct {
	// ProjectNumber is the unique numeric identifier for the project.
	ProjectNumber string `json:"projectNumber"`

	// ProjectID is the unique string identifier for the project.
	ProjectID string `json:"projectId"`

	// LifecycleState indicates the current state of the project (e.g., "ACTIVE").
	LifecycleState string `json:"lifecycleState"`

	// Name is the human-readable name of the project.
	Name string `json:"name"`

	// Labels contains metadata labels associated with the project.
	Labels GCPProjectLabels `json:"labels"`

	// CreateTime is the timestamp when the project was created.
	CreateTime time.Time `json:"createTime"`
}

// Content represents a single message in a conversation, with a role and parts.
// This structure models a message exchange between a user and an AI model.
type Content struct {
	// Role indicates who sent the message ("user", "model", or "tool").
	Role string `json:"role"`

	// Parts is a collection of content parts that make up the message.
	Parts []Part `json:"parts"`
}

// Part represents a distinct piece of content within a message.
// A part can be text, inline data (like an image), a function call, or a function response.
type Part struct {
	// Text contains plain text content.
	Text string `json:"text,omitempty"`

	// InlineData contains base64-encoded data with its MIME type (e.g., images).
	InlineData *InlineData `json:"inlineData,omitempty"`

	// ThoughtSignature is a provider-required signature that accompanies certain parts.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`

	// FunctionCall represents a tool call requested by the model.
	FunctionCall *FunctionCall `json:"functionCall,omitempty"`

	// FunctionResponse represents the result of a tool execution.
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

// InlineData represents base64-encoded data with its MIME type.
// This is typically used for embedding images or other binary data in requests.
type InlineData struct {
	// MimeType specifies the media type of the embedded data (e.g., "image/png").
	MimeType string `json:"mime_type,omitempty"`

	// Data contains the base64-encoded binary data.
	Data string `json:"data,omitempty"`
}

// FunctionCall represents a tool call requested by the model.
// It includes the function name and its arguments that the model wants to execute.
type FunctionCall struct {
	// Name is the identifier of the function to be called.
	Name string `json:"name"`

	// Args contains the arguments to pass to the function.
	Args map[string]interface{} `json:"args"`
}

// FunctionResponse represents the result of a tool execution.
// This is sent back to the model after a tool call has been processed.
type FunctionResponse struct {
	// Name is the identifier of the function that was called.
	Name string `json:"name"`

	// Response contains the result data from the function execution.
	Response map[string]interface{} `json:"response"`
}

// GenerateContentRequest is the top-level request structure for the streamGenerateContent endpoint.
// This structure defines all the parameters needed for generating content from an AI model.
type GenerateContentRequest struct {
	// SystemInstruction provides system-level instructions that guide the model's behavior.
	SystemInstruction *Content `json:"systemInstruction,omitempty"`

	// Contents is the conversation history between the user and the model.
	Contents []Content `json:"contents"`

	// Tools defines the available tools/functions that the model can call.
	Tools []ToolDeclaration `json:"tools,omitempty"`

	// GenerationConfig contains parameters that control the model's generation behavior.
	GenerationConfig `json:"generationConfig"`
}

// GenerationConfig defines parameters that control the model's generation behavior.
// These parameters affect the creativity, randomness, and reasoning of the model's responses.
type GenerationConfig struct {
	// ThinkingConfig specifies configuration for the model's "thinking" process.
	ThinkingConfig GenerationConfigThinkingConfig `json:"thinkingConfig,omitempty"`

	// Temperature controls the randomness of the model's responses.
	// Values closer to 0 make responses more deterministic, while values closer to 1 increase randomness.
	Temperature float64 `json:"temperature,omitempty"`

	// TopP controls nucleus sampling, which affects the diversity of responses.
	// It limits the model to consider only the top P% of probability mass.
	TopP float64 `json:"topP,omitempty"`

	// TopK limits the model to consider only the top K most likely tokens.
	// This can help control the quality and diversity of generated text.
	TopK float64 `json:"topK,omitempty"`
}

// GenerationConfigThinkingConfig specifies configuration for the model's "thinking" process.
// This controls whether the model should output its reasoning process along with the final answer.
type GenerationConfigThinkingConfig struct {
	// IncludeThoughts determines whether the model should output its reasoning process.
	// When enabled, the model will include its step-by-step thinking in the response.
	IncludeThoughts bool `json:"include_thoughts,omitempty"`
}

// ToolDeclaration defines the structure for declaring tools (like functions)
// that the model can call during content generation.
type ToolDeclaration struct {
	// FunctionDeclarations is a list of available functions that the model can call.
	FunctionDeclarations []interface{} `json:"functionDeclarations"`
}
