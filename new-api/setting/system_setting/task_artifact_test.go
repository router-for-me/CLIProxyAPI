package system_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadTaskArtifactAccessLimitsUsesPositiveEnvironmentValues(t *testing.T) {
	t.Setenv(TaskArtifactInvalidRateLimitEnv, "17")
	t.Setenv(TaskArtifactGlobalLimitEnv, "23")
	t.Setenv(TaskArtifactIPLimitEnv, "11")
	t.Setenv(TaskArtifactObjectLimitEnv, "7")

	limits := LoadTaskArtifactAccessLimits()
	assert.Equal(t, 17, limits.InvalidRatePerMinute)
	assert.Equal(t, 23, limits.GlobalConcurrency)
	assert.Equal(t, 11, limits.IPConcurrency)
	assert.Equal(t, 7, limits.ObjectConcurrency)
}

func TestLoadTaskArtifactAccessLimitsFallsBackForInvalidValues(t *testing.T) {
	t.Setenv(TaskArtifactInvalidRateLimitEnv, "0")
	t.Setenv(TaskArtifactGlobalLimitEnv, "-1")
	t.Setenv(TaskArtifactIPLimitEnv, "invalid")
	t.Setenv(TaskArtifactObjectLimitEnv, "")

	limits := LoadTaskArtifactAccessLimits()
	assert.Equal(t, DefaultTaskArtifactInvalidRateLimitPerMinute, limits.InvalidRatePerMinute)
	assert.Equal(t, DefaultTaskArtifactGlobalConcurrency, limits.GlobalConcurrency)
	assert.Equal(t, DefaultTaskArtifactIPConcurrency, limits.IPConcurrency)
	assert.Equal(t, DefaultTaskArtifactObjectConcurrency, limits.ObjectConcurrency)
}
