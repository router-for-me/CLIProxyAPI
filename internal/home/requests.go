package home

type authDispatchRequest struct {
	Type       string            `json:"type"`
	Model      string            `json:"model"`
	Count      int               `json:"count"`
	RequestID  string            `json:"request_id,omitempty"`
	DispatchID string            `json:"dispatch_id,omitempty"`
	SessionID  string            `json:"session_id,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
}

type inFlightLeaseRequest struct {
	Action  string `json:"action"`
	LeaseID string `json:"lease_id"`
	Reason  string `json:"reason,omitempty"`
}

type modelsRequest struct {
	Type    string            `json:"type"`
	Headers map[string]string `json:"headers,omitempty"`
	Query   map[string]string `json:"query,omitempty"`
}

type refreshRequest struct {
	Type      string `json:"type"`
	AuthIndex string `json:"auth_index"`
}
