package session

import (
	"net/http"
	"testing"
)

func TestDuplicateMetadataKeysPreserveSessionLookup(t *testing.T) {
	tests := []struct {
		name      string
		headers   http.Header
		payload   string
		sessionID string
		parentID  string
	}{
		{
			name:      "top-level session",
			payload:   `{"metadata":{},"metadata":{"session_id":"child"}}`,
			sessionID: "session:child",
		},
		{
			name:      "nested request session",
			payload:   `{"request":{"metadata":{},"metadata":{"session_id":"child"}}}`,
			sessionID: "session:child",
		},
		{
			name:      "parent with header session",
			headers:   http.Header{"X-Session-Id": {"child"}},
			payload:   `{"metadata":{},"metadata":{"parent_session_id":"parent"}}`,
			sessionID: "header:child",
			parentID:  "header:parent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info, ok := ExtractSessionInfo(test.headers, []byte(test.payload), nil)
			if !ok || info.SessionID != test.sessionID || info.ParentSessionID != test.parentID {
				t.Fatalf("ExtractSessionInfo() = (%+v, %v), want session %q parent %q", info, ok, test.sessionID, test.parentID)
			}
			if !hasExplicitSession(test.headers, []byte(test.payload)) {
				t.Fatal("hasExplicitSession() = false, want true")
			}
		})
	}
}
