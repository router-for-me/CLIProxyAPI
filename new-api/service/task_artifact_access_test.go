package service

import (
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskArtifactAccessBindsTaskAndKey(t *testing.T) {
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "task-artifact-access-test-secret"
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	access, err := IssueTaskArtifactAccess("task-1", "video-main")
	require.NoError(t, err)
	assert.Len(t, access, 43)
	assert.NotContains(t, access, ".")
	assert.True(t, VerifyTaskArtifactAccess(access, "task-1", "video-main"))
	assert.False(t, VerifyTaskArtifactAccess(access, "task-2", "video-main"))
	assert.False(t, VerifyTaskArtifactAccess(access, "task-1", "video-other"))
	assert.False(t, VerifyTaskArtifactAccess(access+"x", "task-1", "video-main"))

	common.CryptoSecret = "another-node-secret"
	assert.False(t, VerifyTaskArtifactAccess(access, "task-1", "video-main"))
}

func TestBuildTaskArtifactContentURLUsesConfiguredAddressAndPreservesPrefix(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousPublicAddress := system_setting.TaskPublicAddress
	previousServerAddress := system_setting.ServerAddress
	common.CryptoSecret = "task-artifact-url-test-secret"
	system_setting.TaskPublicAddress = "https://media.example/gateway/prefix/"
	system_setting.ServerAddress = "https://fallback.invalid"
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		system_setting.TaskPublicAddress = previousPublicAddress
		system_setting.ServerAddress = previousServerAddress
	})

	contentURL, err := BuildTaskArtifactContentURL("task-public", "video-main")
	require.NoError(t, err)
	parsed, err := url.Parse(contentURL)
	require.NoError(t, err)
	assert.Equal(t, "media.example", parsed.Host)
	assert.Equal(t, "/gateway/prefix/v1/tasks/task-public/artifacts/video-main/content", parsed.Path)
	assert.True(t, VerifyTaskArtifactAccess(
		parsed.Query().Get(TaskArtifactAccessQueryParameter),
		"task-public",
		"video-main",
	))
}

func TestBuildTaskArtifactContentURLFallsBackOnlyToServerAddress(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousPublicAddress := system_setting.TaskPublicAddress
	previousServerAddress := system_setting.ServerAddress
	common.CryptoSecret = "task-artifact-fallback-test-secret"
	system_setting.TaskPublicAddress = ""
	system_setting.ServerAddress = "https://gateway.example/root"
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		system_setting.TaskPublicAddress = previousPublicAddress
		system_setting.ServerAddress = previousServerAddress
	})

	contentURL, err := BuildTaskArtifactContentURL("task-fallback", "audio")
	require.NoError(t, err)
	assert.Contains(t, contentURL, "https://gateway.example/root/v1/tasks/task-fallback/artifacts/audio/content")

	system_setting.TaskPublicAddress = "not-a-url"
	_, err = BuildTaskArtifactContentURL("task-fallback", "audio")
	assert.Error(t, err)
}

func TestValidateTaskArtifactBaseURLOnlyAcceptsSafeAbsoluteHTTPURLs(t *testing.T) {
	for _, valid := range []string{
		"http://localhost:3000",
		"https://gateway.example",
		"https://gateway.example/prefix/path/",
	} {
		assert.NoError(t, ValidateTaskArtifactBaseURL(valid), valid)
	}
	for _, invalid := range []string{
		"",
		"/relative",
		"ftp://gateway.example",
		"https://user:secret@gateway.example",
		"https://gateway.example/path?tenant=1",
		"https://gateway.example/path#fragment",
		" https://gateway.example",
		"https://gateway.example ",
	} {
		assert.Error(t, ValidateTaskArtifactBaseURL(invalid), invalid)
	}
}
