package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskToOpenAIVideoDoesNotExposeResultURL(t *testing.T) {
	task := &Task{
		TaskID:    "task_public",
		Status:    TaskStatusSuccess,
		Progress:  "100%",
		CreatedAt: 10,
		UpdatedAt: 20,
		Properties: Properties{
			OriginModelName: "video-model",
		},
		PrivateData: TaskPrivateData{
			ResultURL: "https://upstream.example/video.mp4?signature=secret",
		},
	}

	video := task.ToOpenAIVideo()

	assert.Equal(t, "task_public", video.ID)
	assert.Equal(t, "video", video.Object)
	assert.Equal(t, "completed", video.Status)
	assert.Nil(t, video.Metadata)

	encoded, err := common.Marshal(video)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "upstream.example")
	assert.NotContains(t, string(encoded), "signature")
}

func TestTaskToOpenAIVideoStatusAndCompletedAt(t *testing.T) {
	tests := []struct {
		name                string
		task                Task
		wantStatus          string
		wantCompletedAt     int64
		wantCompletedAtJSON bool
	}{
		{
			name:            "not start maps to queued",
			task:            Task{TaskID: "t1", Status: TaskStatusNotStart, CreatedAt: 10, UpdatedAt: 20},
			wantStatus:      "queued",
			wantCompletedAt: 0,
		},
		{
			name:            "in progress omits completed at",
			task:            Task{TaskID: "t2", Status: TaskStatusInProgress, CreatedAt: 10, UpdatedAt: 20, FinishTime: 30},
			wantStatus:      "in_progress",
			wantCompletedAt: 0,
		},
		{
			name:                "success uses finish time",
			task:                Task{TaskID: "t3", Status: TaskStatusSuccess, CreatedAt: 10, UpdatedAt: 20, FinishTime: 30},
			wantStatus:          "completed",
			wantCompletedAt:     30,
			wantCompletedAtJSON: true,
		},
		{
			name:                "success falls back to updated at",
			task:                Task{TaskID: "t4", Status: TaskStatusSuccess, CreatedAt: 10, UpdatedAt: 20},
			wantStatus:          "completed",
			wantCompletedAt:     20,
			wantCompletedAtJSON: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			video := tt.task.ToOpenAIVideo()
			assert.Equal(t, tt.wantStatus, video.Status)
			assert.Equal(t, tt.wantCompletedAt, video.CompletedAt)

			encoded, err := common.Marshal(video)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, common.Unmarshal(encoded, &fields))
			if tt.wantCompletedAtJSON {
				require.Contains(t, fields, "completed_at")
				assert.Equal(t, float64(tt.wantCompletedAt), fields["completed_at"])
			} else {
				assert.NotContains(t, fields, "completed_at")
			}
		})
	}
}
