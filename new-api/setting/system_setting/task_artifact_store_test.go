package system_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTaskArtifactStoreConfig(t *testing.T) {
	valid := TaskArtifactStoreConfig{
		Mode:                TaskArtifactStoreModeS3,
		S3Endpoint:          "https://objects.example.com/storage",
		S3Bucket:            "task-artifacts",
		S3Region:            "us-east-1",
		S3AccessKey:         "access-key",
		S3SecretKey:         "secret-key",
		S3Prefix:            "tasks/v1/",
		S3PresignTTLSeconds: 900,
	}
	require.NoError(t, ValidateTaskArtifactStoreConfig(valid))
	require.NoError(t, ValidateTaskArtifactStoreConfig(TaskArtifactStoreConfig{
		Mode:                TaskArtifactStoreModeUpstream,
		S3PresignTTLSeconds: DefaultTaskArtifactStorePresignTTLSeconds,
	}))

	tests := []struct {
		name   string
		mutate func(*TaskArtifactStoreConfig)
		match  string
	}{
		{name: "mode", mutate: func(config *TaskArtifactStoreConfig) { config.Mode = "filesystem" }, match: "unsupported mode"},
		{name: "endpoint scheme", mutate: func(config *TaskArtifactStoreConfig) { config.S3Endpoint = "ftp://objects.example.com" }, match: "http or https"},
		{name: "endpoint credentials", mutate: func(config *TaskArtifactStoreConfig) { config.S3Endpoint = "https://user:pass@objects.example.com" }, match: "without userinfo"},
		{name: "endpoint query", mutate: func(config *TaskArtifactStoreConfig) { config.S3Endpoint = "https://objects.example.com?token=secret" }, match: "query or fragment"},
		{name: "bucket", mutate: func(config *TaskArtifactStoreConfig) { config.S3Bucket = "Invalid_Bucket" }, match: "bucket syntax"},
		{name: "IP bucket", mutate: func(config *TaskArtifactStoreConfig) { config.S3Bucket = "192.168.1.1" }, match: "bucket syntax"},
		{name: "region", mutate: func(config *TaskArtifactStoreConfig) { config.S3Region = "bad region" }, match: "region syntax"},
		{name: "access key", mutate: func(config *TaskArtifactStoreConfig) { config.S3AccessKey = " access-key" }, match: "access key syntax"},
		{name: "secret key", mutate: func(config *TaskArtifactStoreConfig) { config.S3SecretKey = "secret\nkey" }, match: "secret key syntax"},
		{name: "prefix root", mutate: func(config *TaskArtifactStoreConfig) { config.S3Prefix = "/tasks" }, match: "prefix syntax"},
		{name: "prefix traversal", mutate: func(config *TaskArtifactStoreConfig) { config.S3Prefix = "tasks/../private" }, match: "dot segments"},
		{name: "TTL zero", mutate: func(config *TaskArtifactStoreConfig) { config.S3PresignTTLSeconds = 0 }, match: "presign TTL"},
		{name: "TTL too long", mutate: func(config *TaskArtifactStoreConfig) {
			config.S3PresignTTLSeconds = MaxTaskArtifactStorePresignTTLSeconds + 1
		}, match: "presign TTL"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			config := valid
			testCase.mutate(&config)
			assert.ErrorContains(t, ValidateTaskArtifactStoreConfig(config), testCase.match)
		})
	}
}

func TestLoadTaskArtifactStoreConfigFallsBackToUpstream(t *testing.T) {
	t.Setenv(TaskArtifactStoreModeEnv, "filesystem")
	t.Setenv(TaskArtifactStoreS3PresignTTLEnv, "900")
	config := LoadTaskArtifactStoreConfig()
	assert.Equal(t, TaskArtifactStoreModeUpstream, config.Mode)

	t.Setenv(TaskArtifactStoreModeEnv, TaskArtifactStoreModeS3)
	t.Setenv(TaskArtifactStoreS3EndpointEnv, "https://objects.example.com")
	t.Setenv(TaskArtifactStoreS3BucketEnv, "task-artifacts")
	t.Setenv(TaskArtifactStoreS3RegionEnv, "us-east-1")
	t.Setenv(TaskArtifactStoreS3AccessKeyEnv, "access-key")
	t.Setenv(TaskArtifactStoreS3SecretKeyEnv, "secret-key")
	t.Setenv(TaskArtifactStoreS3PrefixEnv, "tasks/v1")
	t.Setenv(TaskArtifactStoreS3PresignTTLEnv, "600")
	config = LoadTaskArtifactStoreConfig()

	assert.Equal(t, TaskArtifactStoreModeUpstream, config.Mode)
	assert.Equal(t, "https://objects.example.com", config.S3Endpoint)
	assert.Equal(t, 600, config.S3PresignTTLSeconds)
}
