// Package acp provides an ACP (Agent Communication Protocol) translator for CLIProxy.
//
// Ported from thegent/src/thegent/adapters/acp_client.py.
// Translates Claude/OpenAI API request format into ACP format and back.
package acp

// ACPMessage is a single message in ACP format.
type ACPMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ACPRequest is the ACP-format request payload.
type ACPRequest struct {
	Model    string       `json:"model"`
	Messages []ACPMessage `json:"messages"`
}

// ChatCompletionRequest is the OpenAI-compatible / Claude-compatible request format
// accepted by the ACP adapter.
type ChatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

// Message is an OpenAI/Claude-compatible message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
