package home

type authDispatchRequest struct {
	Type      string            `json:"type"`
	Model     string            `json:"model"`
	Count     int               `json:"count"`
	SessionID string            `json:"session_id,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

type refreshRequest struct {
	Type      string `json:"type"`
	AuthIndex string `json:"auth_index"`
}
