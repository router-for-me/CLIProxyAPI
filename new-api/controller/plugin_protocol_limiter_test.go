package controller

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPluginProtocolObservationLimiterCaps(t *testing.T) {
	t.Run("global", func(t *testing.T) {
		limiter := newPluginProtocolObservationLimiter(pluginProtocolObservationLimits{
			global:    2,
			perPlugin: 2,
			perUser:   2,
			perToken:  2,
		})
		releaseFirst, err := limiter.acquire("first", 1, 1)
		require.NoError(t, err)
		defer releaseFirst()
		releaseSecond, err := limiter.acquire("second", 2, 2)
		require.NoError(t, err)
		defer releaseSecond()

		release, err := limiter.acquire("third", 3, 3)
		assert.Nil(t, release)
		assertLimitError(t, err, "global", 2)
	})

	t.Run("plugin", func(t *testing.T) {
		limiter := newPluginProtocolObservationLimiter(pluginProtocolObservationLimits{
			global:    3,
			perPlugin: 1,
			perUser:   3,
			perToken:  3,
		})
		releaseFirst, err := limiter.acquire("shared", 1, 1)
		require.NoError(t, err)
		defer releaseFirst()

		release, err := limiter.acquire("shared", 2, 2)
		assert.Nil(t, release)
		assertLimitError(t, err, "plugin", 1)

		releaseOther, err := limiter.acquire("other", 2, 2)
		require.NoError(t, err)
		defer releaseOther()
	})

	t.Run("user", func(t *testing.T) {
		limiter := newPluginProtocolObservationLimiter(pluginProtocolObservationLimits{
			global:    3,
			perPlugin: 3,
			perUser:   1,
			perToken:  3,
		})
		releaseFirst, err := limiter.acquire("first", 1, 1)
		require.NoError(t, err)
		defer releaseFirst()

		release, err := limiter.acquire("second", 1, 2)
		assert.Nil(t, release)
		assertLimitError(t, err, "user", 1)

		releaseOther, err := limiter.acquire("second", 2, 2)
		require.NoError(t, err)
		defer releaseOther()
	})

	t.Run("token", func(t *testing.T) {
		limiter := newPluginProtocolObservationLimiter(pluginProtocolObservationLimits{
			global:    3,
			perPlugin: 3,
			perUser:   3,
			perToken:  1,
		})
		releaseFirst, err := limiter.acquire("first", 1, 1)
		require.NoError(t, err)
		defer releaseFirst()

		release, err := limiter.acquire("second", 2, 1)
		assert.Nil(t, release)
		assertLimitError(t, err, "token", 1)

		releaseOther, err := limiter.acquire("second", 2, 2)
		require.NoError(t, err)
		defer releaseOther()
	})
}

func TestPluginProtocolObservationLimiterRollsBackFailedAdmission(t *testing.T) {
	t.Run("user failure", func(t *testing.T) {
		limiter := newPluginProtocolObservationLimiter(pluginProtocolObservationLimits{
			global:    2,
			perPlugin: 2,
			perUser:   1,
			perToken:  2,
		})
		releaseHeld, err := limiter.acquire("first", 1, 1)
		require.NoError(t, err)
		defer releaseHeld()

		release, err := limiter.acquire("second", 1, 2)
		assert.Nil(t, release)
		assertLimitError(t, err, "user", 1)

		releaseReplacement, err := limiter.acquire("second", 2, 2)
		require.NoError(t, err)
		defer releaseReplacement()
	})

	t.Run("token failure", func(t *testing.T) {
		limiter := newPluginProtocolObservationLimiter(pluginProtocolObservationLimits{
			global:    2,
			perPlugin: 2,
			perUser:   2,
			perToken:  1,
		})
		releaseHeld, err := limiter.acquire("first", 1, 1)
		require.NoError(t, err)
		defer releaseHeld()

		release, err := limiter.acquire("second", 2, 1)
		assert.Nil(t, release)
		assertLimitError(t, err, "token", 1)

		releaseReplacement, err := limiter.acquire("second", 2, 2)
		require.NoError(t, err)
		defer releaseReplacement()
	})
}

func TestPluginProtocolObservationLimiterReleaseIsIdempotent(t *testing.T) {
	limiter := newPluginProtocolObservationLimiter(pluginProtocolObservationLimits{
		global:    1,
		perPlugin: 1,
		perUser:   1,
		perToken:  1,
	})
	release, err := limiter.acquire("plugin", 1, 1)
	require.NoError(t, err)

	release()
	release()

	releaseAgain, err := limiter.acquire("plugin", 1, 1)
	require.NoError(t, err)
	releaseAgain()
}

func TestPluginProtocolObservationLimiterRejectsMissingIdentityWithoutConsumingCapacity(t *testing.T) {
	limiter := newPluginProtocolObservationLimiter(pluginProtocolObservationLimits{
		global:    1,
		perPlugin: 1,
		perUser:   1,
		perToken:  1,
	})

	for _, testCase := range []struct {
		name      string
		pluginKey string
		userID    int
		tokenID   int
	}{
		{name: "empty plugin", userID: 1, tokenID: 1},
		{name: "blank plugin", pluginKey: " \t", userID: 1, tokenID: 1},
		{name: "zero user", pluginKey: "plugin", tokenID: 1},
		{name: "negative user", pluginKey: "plugin", userID: -1, tokenID: 1},
		{name: "zero token", pluginKey: "plugin", userID: 1},
		{name: "negative token", pluginKey: "plugin", userID: 1, tokenID: -1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			release, err := limiter.acquire(testCase.pluginKey, testCase.userID, testCase.tokenID)
			assert.Nil(t, release)
			assert.ErrorIs(t, err, errInvalidPluginProtocolObservationIdentity)
		})
	}

	release, err := limiter.acquire("plugin", 1, 1)
	require.NoError(t, err)
	release()
}

func TestPluginProtocolObservationLimiterConcurrentAdmissionsRespectCap(t *testing.T) {
	const (
		workerCount = 8
		globalLimit = 3
	)
	limiter := newPluginProtocolObservationLimiter(pluginProtocolObservationLimits{
		global:    globalLimit,
		perPlugin: workerCount,
		perUser:   workerCount,
		perToken:  workerCount,
	})
	start := make(chan struct{})
	releases := make(chan func(), workerCount)
	errorsFound := make(chan error, workerCount)

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 1; worker <= workerCount; worker++ {
		go func(id int) {
			defer workers.Done()
			<-start
			release, err := limiter.acquire("plugin", id, id)
			if err != nil {
				errorsFound <- err
				return
			}
			releases <- release
		}(worker)
	}
	close(start)
	workers.Wait()
	close(releases)
	close(errorsFound)

	assert.Len(t, releases, globalLimit)
	assert.Len(t, errorsFound, workerCount-globalLimit)
	for err := range errorsFound {
		assert.ErrorIs(t, err, errPluginProtocolObservationLimitExceeded)
	}
	for release := range releases {
		release()
	}

	release, err := limiter.acquire("plugin", 1, 1)
	require.NoError(t, err)
	release()
}

func assertLimitError(
	t *testing.T,
	err error,
	expectedScope string,
	expectedLimit int,
) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, errPluginProtocolObservationLimitExceeded)

	var limitError *pluginProtocolObservationLimitError
	require.True(t, errors.As(err, &limitError))
	assert.Equal(t, expectedScope, limitError.scope)
	assert.Equal(t, expectedLimit, limitError.limit)
}
