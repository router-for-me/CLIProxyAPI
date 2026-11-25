package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

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
			name:           "interaction.completed with requires_action status",
			payload:        `{"event_type":"interaction.completed","interaction":{"id":"int_1b","status":"requires_action"}}`,
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
			name:           "interaction.done event_type",
			payload:        `{"event_type":"interaction.done","interaction":{"id":"int_2b","status":"completed"}}`,
			wantCompleted:  true,
			wantIncomplete: false,
		},
		{
			name:           "finish event_type with completed status",
			payload:        `{"event_type":"finish","status":"completed"}`,
			wantCompleted:  true,
			wantIncomplete: false,
		},
		{
			name:           "finish event_type without status",
			payload:        `{"event_type":"finish","metadata":{"total_usage":{"total_tokens":10}}}`,
			wantCompleted:  true,
			wantIncomplete: false,
		},
		{
			name:           "finish event_type with requires_action status",
			payload:        `{"event_type":"finish","status":"requires_action"}`,
			wantCompleted:  true,
			wantIncomplete: false,
		},
		{
			name:           "finish event_type with length finish_reason marks incomplete",
			payload:        `{"event_type":"finish","status":"completed","finish_reason":"length"}`,
			wantCompleted:  true,
			wantIncomplete: true,
		},
		{
			name:           "finish event_type with incomplete status",
			payload:        `{"event_type":"finish","status":"incomplete"}`,
			wantCompleted:  false,
			wantIncomplete: true,
		},
		{
			name:           "interaction.failed event_type",
			payload:        `{"event_type":"interaction.failed","interaction":{"id":"int_3","status":"failed"}}`,
			wantCompleted:  false,
			wantIncomplete: true,
		},
		{
			name:           "interaction.failed event_type without nested interaction",
			payload:        `{"event_type":"interaction.failed"}`,
			wantCompleted:  false,
			wantIncomplete: true,
		},
		{
			name:           "interaction.failed overrides completed interaction status",
			payload:        `{"event_type":"interaction.failed","interaction":{"id":"int_3b","status":"completed"}}`,
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
			name:           "interaction.incomplete event_type without nested interaction",
			payload:        `{"event_type":"interaction.incomplete"}`,
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
			name:           "interaction.canceled event_type without nested interaction",
			payload:        `{"event_type":"interaction.canceled"}`,
			wantCompleted:  false,
			wantIncomplete: true,
		},
		{
			name:           "interaction.completed with incomplete status",
			payload:        `{"event_type":"interaction.completed","interaction":{"id":"int_5b","status":"incomplete","finish_reason":"length"}}`,
			wantCompleted:  false,
			wantIncomplete: true,
		},
		{
			name:           "interaction.completed with in_progress status",
			payload:        `{"event_type":"interaction.completed","interaction":{"id":"int_5c","status":"in_progress"}}`,
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
			name:           "interaction.status requires_action without event_type",
			payload:        `{"interaction":{"id":"int_6b","status":"requires_action"}}`,
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
			name:           "interaction.incomplete_details marks incomplete",
			payload:        `{"event_type":"interaction.completed","interaction":{"id":"int_8b","status":"completed","incomplete_details":{"reason":"max_output_tokens"}}}`,
			wantCompleted:  true,
			wantIncomplete: true,
		},
		{
			name:           "interaction.finish_reason length marks incomplete",
			payload:        `{"event_type":"interaction.completed","interaction":{"id":"int_8c","status":"completed","finish_reason":"length"}}`,
			wantCompleted:  true,
			wantIncomplete: true,
		},
		{
			name:           "interaction.finish_reason content_filter marks incomplete",
			payload:        `{"event_type":"interaction.completed","interaction":{"id":"int_8d","status":"completed","finish_reason":"content_filter"}}`,
			wantCompleted:  true,
			wantIncomplete: true,
		},
		{
			name:           "flat interaction object completed without event_type",
			payload:        `{"id":"int_v1","object":"interaction","status":"completed"}`,
			wantCompleted:  true,
			wantIncomplete: false,
		},
		{
			name:           "flat interaction object requires_action without event_type",
			payload:        `{"id":"int_v2","object":"interaction","status":"requires_action"}`,
			wantCompleted:  true,
			wantIncomplete: false,
		},
		{
			name:           "flat interaction object with incomplete_details marks incomplete",
			payload:        `{"id":"int_v3","object":"interaction","status":"completed","incomplete_details":{"reason":"max_output_tokens"}}`,
			wantCompleted:  true,
			wantIncomplete: true,
		},
		{
			name:           "interaction.in_progress is neither completed nor incomplete",
			payload:        `{"event_type":"content.delta","interaction":{"id":"int_9","status":"in_progress"}}`,
			wantCompleted:  false,
			wantIncomplete: false,
		},
		{
			name:           "interaction.created in_progress is neither completed nor incomplete",
			payload:        `{"event_type":"interaction.created","interaction":{"id":"int_10","status":"in_progress","object":"interaction"}}`,
			wantCompleted:  false,
			wantIncomplete: false,
		},
		{
			name:           "interaction.status_update in_progress is neither completed nor incomplete",
			payload:        `{"event_type":"interaction.status_update","interaction_id":"int_11","status":"in_progress"}`,
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
			sse := []byte("data: " + tc.payload + "\n\n")
			streamCompleted, streamIncomplete := streamPayloadCompletionFlags(sse)
			if streamCompleted != tc.wantCompleted || streamIncomplete != tc.wantIncomplete {
				t.Fatalf("streamPayloadCompletionFlags(%s) = (completed=%v, incomplete=%v), want (completed=%v, incomplete=%v)",
					tc.payload, streamCompleted, streamIncomplete, tc.wantCompleted, tc.wantIncomplete)
			}
		})
	}
}

func TestWrapStreamResult_InteractionsSSEInferenceVerified(t *testing.T) {
	tests := []struct {
		name                  string
		frames                []string
		wantCompletedResponse bool
		wantInferenceVerified bool
	}{
		{
			name: "completed interaction stream verifies inference",
			frames: []string{
				"event: interaction.created\ndata: " + `{"event_type":"interaction.created","interaction":{"id":"int_1","status":"in_progress"}}` + "\n\n",
				"event: interaction.completed\ndata: " + `{"event_type":"interaction.completed","interaction":{"id":"int_1","status":"completed"}}` + "\n\n",
			},
			wantCompletedResponse: true,
			wantInferenceVerified: true,
		},
		{
			name: "requires_action interaction stream verifies inference",
			frames: []string{
				"event: interaction.created\ndata: " + `{"event_type":"interaction.created","interaction":{"id":"int_2","status":"in_progress"}}` + "\n\n",
				"event: interaction.completed\ndata: " + `{"event_type":"interaction.completed","interaction":{"id":"int_2","status":"requires_action"}}` + "\n\n",
			},
			wantCompletedResponse: true,
			wantInferenceVerified: true,
		},
		{
			name: "finish event_type interaction stream verifies inference",
			frames: []string{
				"data: " + `{"event_type":"start"}` + "\n\n",
				"data: " + `{"event_type":"finish","status":"completed"}` + "\n\n",
			},
			wantCompletedResponse: true,
			wantInferenceVerified: true,
		},
		{
			name: "truncated finish event_type interaction stream does not verify inference",
			frames: []string{
				"data: " + `{"event_type":"start"}` + "\n\n",
				"data: " + `{"event_type":"finish","finish_reason":"length"}` + "\n\n",
			},
			wantCompletedResponse: false,
			wantInferenceVerified: false,
		},
		{
			name: "failed interaction stream does not verify inference",
			frames: []string{
				"event: interaction.created\ndata: " + `{"event_type":"interaction.created","interaction":{"id":"int_3","status":"in_progress"}}` + "\n\n",
				"event: interaction.failed\ndata: " + `{"event_type":"interaction.failed","interaction":{"id":"int_3","status":"failed"}}` + "\n\n",
			},
			wantCompletedResponse: false,
			wantInferenceVerified: false,
		},
		{
			name: "incomplete interaction stream does not verify inference",
			frames: []string{
				"event: interaction.created\ndata: " + `{"event_type":"interaction.created","interaction":{"id":"int_4","status":"in_progress"}}` + "\n\n",
				"event: interaction.incomplete\ndata: " + `{"event_type":"interaction.incomplete","interaction":{"id":"int_4","status":"incomplete"}}` + "\n\n",
			},
			wantCompletedResponse: false,
			wantInferenceVerified: false,
		},
		{
			name: "failed frame after completed frame keeps CompletedResponse false",
			frames: []string{
				"event: interaction.completed\ndata: " + `{"event_type":"interaction.completed","interaction":{"id":"int_5","status":"completed"}}` + "\n\n",
				"event: interaction.failed\ndata: " + `{"event_type":"interaction.failed"}` + "\n\n",
			},
			wantCompletedResponse: false,
			wantInferenceVerified: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			hook := newIssue7738ResultHook()
			manager := NewManager(nil, nil, hook)
			authEntry := &Auth{
				ID:       "interactions-auth-" + tc.name,
				Provider: "gemini",
				Status:   StatusActive,
			}
			registered, errRegister := manager.Register(ctx, authEntry)
			if errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}

			remaining := make(chan cliproxyexecutor.StreamChunk, len(tc.frames))
			for _, frame := range tc.frames {
				remaining <- cliproxyexecutor.StreamChunk{Payload: []byte(frame)}
			}
			close(remaining)

			res := manager.wrapStreamResult(
				ctx,
				registered,
				registered.Provider,
				"gemini-3-flash",
				"gemini-3-flash",
				nil,
				nil,
				remaining,
				OAuthModelAliasResult{},
				false,
				cliproxyexecutor.Options{},
				manager.nextExecutionSeq(),
			)
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected chunk error: %v", chunk.Err)
				}
			}

			select {
			case <-hook.done:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for recorded stream execution result")
			}

			results := hook.snapshot()
			if len(results) != 1 {
				t.Fatalf("recorded results len = %d, want 1", len(results))
			}
			if results[0].CompletedResponse != tc.wantCompletedResponse {
				t.Fatalf("CompletedResponse = %v, want %v", results[0].CompletedResponse, tc.wantCompletedResponse)
			}

			updated, ok := manager.GetByID(registered.ID)
			if !ok || updated == nil {
				t.Fatalf("GetByID(%q) not found", registered.ID)
			}
			if updated.InferenceVerified != tc.wantInferenceVerified {
				t.Fatalf("InferenceVerified = %v, want %v", updated.InferenceVerified, tc.wantInferenceVerified)
			}
		})
	}
}

func TestIsCompletedExecutionPayload_PreservesNonStreamInteractionsBehavior(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "non-stream interaction completed remains true",
			payload: `{"id":"int_1","object":"interaction","status":"completed"}`,
			want:    true,
		},
		{
			name:    "non-stream interaction requires_action remains false",
			payload: `{"id":"int_2","object":"interaction","status":"requires_action"}`,
			want:    false,
		},
		{
			name:    "non-stream nested interaction wrapper without top-level status remains false",
			payload: `{"interaction":{"id":"int_3","status":"completed"}}`,
			want:    false,
		},
		{
			name:    "non-stream event_type finish without top-level status remains false",
			payload: `{"event_type":"finish"}`,
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCompletedExecutionPayload([]byte(tc.payload)); got != tc.want {
				t.Fatalf("isCompletedExecutionPayload(%s) = %v, want %v", tc.payload, got, tc.want)
			}
		})
	}
}
