package acp

// ACPResponse is the ACP-format response payload.
type ACPResponse struct {
	ID      string      `json:"id"`
	Model   string      `json:"model"`
	Choices []ACPChoice `json:"choices"`
}

// ACPChoice is a single choice in an ACP response.
type ACPChoice struct {
	Message ACPMessage `json:"message"`
}
