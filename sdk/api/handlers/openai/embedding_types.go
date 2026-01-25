// Package openai provides HTTP handlers for OpenAI API endpoints.
package openai

// EmbeddingRequest represents the OpenAI embedding API request format.
// See: https://platform.openai.com/docs/api-reference/embeddings/create
type EmbeddingRequest struct {
	// Model is the ID of the model to use (e.g., "text-embedding-ada-002", "text-embedding-3-small")
	Model string `json:"model"`

	// Input is the text to embed. Can be a string or an array of strings.
	Input interface{} `json:"input"`

	// EncodingFormat is the format to return the embeddings in ("float" or "base64")
	EncodingFormat string `json:"encoding_format,omitempty"`

	// Dimensions is the number of dimensions for the output embeddings (only for text-embedding-3 models)
	Dimensions int `json:"dimensions,omitempty"`

	// User is a unique identifier representing the end-user
	User string `json:"user,omitempty"`
}

// EmbeddingResponse represents the OpenAI embedding API response format.
type EmbeddingResponse struct {
	// Object is always "list"
	Object string `json:"object"`

	// Data contains the embedding objects
	Data []EmbeddingData `json:"data"`

	// Model is the model used for embedding
	Model string `json:"model"`

	// Usage contains token usage information
	Usage EmbeddingUsage `json:"usage"`
}

// EmbeddingData represents a single embedding result.
type EmbeddingData struct {
	// Object is always "embedding"
	Object string `json:"object"`

	// Embedding is the embedding vector (list of floats)
	Embedding []float64 `json:"embedding"`

	// Index is the index of this embedding in the input list
	Index int `json:"index"`
}

// EmbeddingUsage contains token usage information for the embedding request.
type EmbeddingUsage struct {
	// PromptTokens is the number of tokens in the input
	PromptTokens int `json:"prompt_tokens"`

	// TotalTokens is the total number of tokens used
	TotalTokens int `json:"total_tokens"`
}

// GeminiEmbedContentRequest represents the Gemini embedContent API request format.
// See: https://ai.google.dev/api/embeddings
type GeminiEmbedContentRequest struct {
	// Content contains the text to embed
	Content GeminiContent `json:"content"`

	// TaskType specifies the intended use case for the embedding
	// Supported values: RETRIEVAL_QUERY, RETRIEVAL_DOCUMENT, SEMANTIC_SIMILARITY, CLASSIFICATION, CLUSTERING
	TaskType string `json:"taskType,omitempty"`

	// Title is an optional title for the content (only used with RETRIEVAL_DOCUMENT)
	Title string `json:"title,omitempty"`
}

// GeminiContent represents the content structure for Gemini API.
type GeminiContent struct {
	// Parts contains the content parts
	Parts []GeminiPart `json:"parts"`
}

// GeminiPart represents a single content part.
type GeminiPart struct {
	// Text is the text content
	Text string `json:"text"`
}

// GeminiEmbedContentResponse represents the Gemini embedContent API response format.
type GeminiEmbedContentResponse struct {
	// Embedding contains the embedding values
	Embedding GeminiEmbeddingValues `json:"embedding"`
}

// GeminiEmbeddingValues contains the embedding vector.
type GeminiEmbeddingValues struct {
	// Values is the embedding vector (list of floats)
	Values []float64 `json:"values"`
}
