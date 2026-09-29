package kitutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrorHooksRemainDistinct(t *testing.T) {
	previousError := logError.Load()
	previousSystemError := logSystemError.Load()
	t.Cleanup(func() {
		logError.Store(previousError)
		logSystemError.Store(previousSystemError)
	})

	var ordinaryMessages []string
	var systemMessages []string
	SetLogging(nil, func(message string) {
		ordinaryMessages = append(ordinaryMessages, message)
	})
	SetSystemErrorLogging(func(message string) {
		systemMessages = append(systemMessages, message)
	})

	LogError("invalid dto")
	LogSystemError("converter failure")

	assert.Equal(t, []string{"invalid dto"}, ordinaryMessages)
	assert.Equal(t, []string{"converter failure"}, systemMessages)
}
