package constant

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeTaskAction(t *testing.T) {
	tests := map[string]string{
		"generate":            TaskActionImageToVideo,
		"textGenerate":        TaskActionTextToVideo,
		"firstTailGenerate":   TaskActionFirstTailToVideo,
		"referenceGenerate":   TaskActionReferenceToVideo,
		"remixGenerate":       TaskActionRemix,
		TaskActionTextToVideo: TaskActionTextToVideo,
		"MUSIC":               "MUSIC",
		"custom_action":       "custom_action",
		"":                    "",
	}

	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			assert.Equal(t, expected, NormalizeTaskAction(input))
		})
	}
}
