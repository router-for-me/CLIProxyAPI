package system_setting

import "github.com/QuantumNous/new-api/common"

const (
	DefaultTaskArtifactInvalidRateLimitPerMinute = 60
	DefaultTaskArtifactGlobalConcurrency         = 128
	DefaultTaskArtifactIPConcurrency             = 64
	DefaultTaskArtifactObjectConcurrency         = 16
)

const (
	TaskArtifactInvalidRateLimitEnv = "TASK_ARTIFACT_INVALID_RATE_LIMIT_PER_MINUTE"
	TaskArtifactGlobalLimitEnv      = "TASK_ARTIFACT_GLOBAL_CONCURRENCY"
	TaskArtifactIPLimitEnv          = "TASK_ARTIFACT_IP_CONCURRENCY"
	TaskArtifactObjectLimitEnv      = "TASK_ARTIFACT_OBJECT_CONCURRENCY"
)

type TaskArtifactAccessLimits struct {
	InvalidRatePerMinute int
	GlobalConcurrency    int
	IPConcurrency        int
	ObjectConcurrency    int
}

func positiveTaskArtifactLimit(env string, defaultValue int) int {
	value := common.GetEnvOrDefault(env, defaultValue)
	if value <= 0 {
		common.SysError(env + " must be positive; using default")
		return defaultValue
	}
	return value
}

// LoadTaskArtifactAccessLimits reads startup configuration. Invalid and
// non-positive values safely fall back to the documented defaults.
func LoadTaskArtifactAccessLimits() TaskArtifactAccessLimits {
	return TaskArtifactAccessLimits{
		InvalidRatePerMinute: positiveTaskArtifactLimit(
			TaskArtifactInvalidRateLimitEnv,
			DefaultTaskArtifactInvalidRateLimitPerMinute,
		),
		GlobalConcurrency: positiveTaskArtifactLimit(
			TaskArtifactGlobalLimitEnv,
			DefaultTaskArtifactGlobalConcurrency,
		),
		IPConcurrency: positiveTaskArtifactLimit(
			TaskArtifactIPLimitEnv,
			DefaultTaskArtifactIPConcurrency,
		),
		ObjectConcurrency: positiveTaskArtifactLimit(
			TaskArtifactObjectLimitEnv,
			DefaultTaskArtifactObjectConcurrency,
		),
	}
}
