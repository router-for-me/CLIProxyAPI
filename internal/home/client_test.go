package home

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAuthDispatchRequestIncludesCount(t *testing.T) {
	req := newAuthDispatchRequest("gpt-5.4", "session-1", http.Header{"Authorization": {"Bearer test"}}, 2)

	raw, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal auth dispatch request: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal auth dispatch request: %v", err)
	}
	if got := int(payload["count"].(float64)); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

func TestAuthDispatchRequestDefaultsCountToOne(t *testing.T) {
	req := newAuthDispatchRequest("gpt-5.4", "", nil, 0)

	if req.Count != 1 {
		t.Fatalf("count = %d, want 1", req.Count)
	}
}
