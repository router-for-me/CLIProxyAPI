package auth

import "testing"

func TestJSONObjectCompletionFlags_InteractionsSSE(t *testing.T) {
	tests := []struct {
		name           string
		payload        string
		wantCompleted  bool
		wantIncomplete bool
	}{
		{
			name:           "interaction.completed event_type",
			payload:        `{"event_type":"interaction.completed","interaction":{"id":"int_1","status":"completed"}}`,
			wantCompleted:  true,
			wantIncomplete: false,
		},
		{
			name:           "interaction.complete event_type",
			payload:        `{"event_type":"interaction.complete","interaction":{"id":"int_2","status":"completed"}}`,
			wantCompleted:  true,
			wantIncomplete: false,
		},
		{
			name:           "interaction.complete event_type without nested status",
			payload:        `{"event_type":"interaction.complete"}`,
			wantCompleted:  true,
			wantIncomplete: false,
		},
		{
			name:           "interaction.failed event_type",
			payload:        `{"event_type":"interaction.failed","interaction":{"id":"int_3","status":"failed"}}`,
			wantCompleted:  false,
			wantIncomplete: true,
		},
		{
			name:           "interaction.incomplete event_type",
			payload:        `{"event_type":"interaction.incomplete","interaction":{"id":"int_4","status":"incomplete"}}`,
			wantCompleted:  false,
			wantIncomplete: true,
		},
		{
			name:           "interaction.cancelled event_type",
			payload:        `{"event_type":"interaction.cancelled","interaction":{"id":"int_5","status":"cancelled"}}`,
			wantCompleted:  false,
			wantIncomplete: true,
		},
		{
			name:           "interaction.status completed without event_type",
			payload:        `{"interaction":{"id":"int_6","status":"completed"}}`,
			wantCompleted:  true,
			wantIncomplete: false,
		},
		{
			name:           "interaction.status failed without event_type",
			payload:        `{"interaction":{"id":"int_7","status":"failed"}}`,
			wantCompleted:  false,
			wantIncomplete: true,
		},
		{
			name:           "interaction.error marks incomplete",
			payload:        `{"event_type":"interaction.complete","interaction":{"id":"int_8","status":"completed","error":{"code":"internal","message":"boom"}}}`,
			wantCompleted:  true,
			wantIncomplete: true,
		},
		{
			name:           "interaction.in_progress is neither completed nor incomplete",
			payload:        `{"event_type":"content.delta","interaction":{"id":"int_9","status":"in_progress"}}`,
			wantCompleted:  false,
			wantIncomplete: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCompleted, gotIncomplete := jsonObjectCompletionFlags([]byte(tc.payload))
			if gotCompleted != tc.wantCompleted || gotIncomplete != tc.wantIncomplete {
				t.Fatalf("jsonObjectCompletionFlags(%s) = (completed=%v, incomplete=%v), want (completed=%v, incomplete=%v)",
					tc.payload, gotCompleted, gotIncomplete, tc.wantCompleted, tc.wantIncomplete)
			}
		})
	}
}
