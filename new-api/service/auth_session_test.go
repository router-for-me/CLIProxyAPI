package service

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAuthSessionTestDB(t *testing.T) *model.User {
	t.Helper()
	previousDB, previousRedis := model.DB, common.RedisEnabled
	previousActiveLimit := common.UserSessionActiveLimit
	previousIssuanceLimit := common.UserSessionIssuanceLimit
	previousIssuanceWindow := common.UserSessionIssuanceWindowSeconds
	previousRevokedRetention := common.UserSessionRevokedRetentionDays
	previousAlertThreshold := common.UserSessionHourlyAlertThreshold
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}))
	model.DB = db
	common.RedisEnabled = false
	common.UserSessionActiveLimit = common.DefaultUserSessionActiveLimit
	common.UserSessionIssuanceLimit = common.DefaultUserSessionIssuanceLimit
	common.UserSessionIssuanceWindowSeconds = int64(common.DefaultUserSessionIssuanceWindowSeconds)
	common.UserSessionRevokedRetentionDays = common.DefaultUserSessionRevokedRetentionDays
	common.UserSessionHourlyAlertThreshold = common.DefaultUserSessionHourlyAlertThreshold
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.UserSessionActiveLimit = previousActiveLimit
		common.UserSessionIssuanceLimit = previousIssuanceLimit
		common.UserSessionIssuanceWindowSeconds = previousIssuanceWindow
		common.UserSessionRevokedRetentionDays = previousRevokedRetention
		common.UserSessionHourlyAlertThreshold = previousAlertThreshold
		_ = sqlDB.Close()
	})
	user := &model.User{
		Username:    "session-user",
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func useIndependentAuthSessionRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	previousSyncFrequency := common.SyncFrequency
	serverA := miniredis.RunT(t)
	serverB := miniredis.RunT(t)
	clientA := redis.NewClient(&redis.Options{Addr: serverA.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: serverB.Addr()})
	common.RedisEnabled = true
	common.SyncFrequency = 2
	common.RDB = clientA
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
		common.SyncFrequency = previousSyncFrequency
	})
	return serverA, clientA, serverB, clientB
}

func cachedLoginSessionKey(t *testing.T, server *miniredis.Miniredis) string {
	t.Helper()
	for _, key := range server.Keys() {
		if strings.HasPrefix(key, "auth:session:") {
			return key
		}
	}
	require.FailNow(t, "login session was not cached")
	return ""
}

func TestCreateLoginSessionEnforcesActiveLimitAcrossAuthVersions(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	common.UserSessionActiveLimit = 50
	common.UserSessionIssuanceLimit = 100
	now := time.Now().Unix()
	rows := make([]model.UserSession, 0, 49)
	for i := range 49 {
		authVersion := user.AuthVersion
		if i == 0 {
			authVersion++
		}
		rows = append(rows, model.UserSession{
			SID:             fmt.Sprintf("active-limit-%02d", i),
			UserID:          user.Id,
			Version:         1,
			UserAuthVersion: authVersion,
			Status:          model.UserSessionStatusActive,
			RefreshHash:     fmt.Sprintf("hash-%02d", i),
			LoginMethod:     "password",
			CreatedAt:       now - int64(i),
			LastActiveAt:    now - int64(i),
			ExpiresAt:       now + 3600,
		})
	}
	require.NoError(t, model.DB.Create(&rows).Error)

	_, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err, "49 active sessions must allow creation of the 50th")

	_, err = CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, model.ErrUserSessionLimit)
	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Equal(t, int64(50), count)
}

func TestCreateLoginSessionEnforcesIssuanceLimitAcrossAllStatuses(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	common.UserSessionActiveLimit = 10
	common.UserSessionIssuanceLimit = 3
	common.UserSessionIssuanceWindowSeconds = 60
	now := time.Now().Unix()
	rows := []model.UserSession{
		{
			SID: "issuance-limit-revoked", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion + 1,
			Status: model.UserSessionStatusRevoked, RefreshHash: "hash-revoked", LoginMethod: "password",
			CreatedAt: now - 2, LastActiveAt: now - 2, ExpiresAt: now + 3600, RevokedAt: now - 1,
		},
		{
			SID: "issuance-limit-expired", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion,
			Status: model.UserSessionStatusActive, RefreshHash: "hash-expired", LoginMethod: "password",
			CreatedAt: now - 1, LastActiveAt: now - 1, ExpiresAt: now - 1,
		},
		{
			SID: "issuance-outside-effective-window", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion,
			Status: model.UserSessionStatusRevoked, RefreshHash: "hash-outside", LoginMethod: "password",
			CreatedAt: now - 61, LastActiveAt: now - 61, ExpiresAt: now + 3600, RevokedAt: now - 60,
		},
	}
	require.NoError(t, model.DB.Create(&rows).Error)

	_, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err, "rows outside the effective issuance window must not consume the limit")

	_, err = CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, model.ErrUserSessionIssuanceLimit)
	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Equal(t, int64(4), count)
}

func TestPasswordResetDoesNotClearSessionIssuanceHistory(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	common.UserSessionActiveLimit = 50
	common.UserSessionIssuanceLimit = 1
	email := "session-reset@example.com"
	require.NoError(t, model.DB.Model(user).Update("email", email).Error)

	_, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	require.NoError(t, model.ResetUserPasswordByEmail(email, "new-password"))

	_, err = CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, model.ErrUserSessionIssuanceLimit)
}

func TestCreateLoginSessionFailsClosedWhenLimitCountFails(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	forcedErr := errors.New("forced session count failure")
	callbackName := "test:fail_user_session_limit_count"
	callbackRegistered := true
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() {
		if callbackRegistered {
			_ = model.DB.Callback().Query().Remove(callbackName)
		}
	})

	_, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, forcedErr)
	require.NoError(t, model.DB.Callback().Query().Remove(callbackName))
	callbackRegistered = false
	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestCleanupAuthArtifactsAlertsBeforeDeletingHourlyIssuance(t *testing.T) {
	setupAuthSessionTestDB(t)
	common.UserSessionHourlyAlertThreshold = 2
	common.UserSessionIssuanceWindowSeconds = 1
	now := time.Now()
	boundaryRows := make([]model.UserSession, 0, 2)
	for i := range 2 {
		boundaryRows = append(boundaryRows, model.UserSession{
			SID: "hourly-boundary-" + string(rune('a'+i)), UserID: 1, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "hash", LoginMethod: "password",
			CreatedAt: now.Add(-2 * time.Second).Unix(), LastActiveAt: now.Add(-time.Hour).Unix(), ExpiresAt: now.Add(-time.Minute).Unix(),
		})
	}
	require.NoError(t, model.DB.Create(&boundaryRows).Error)

	var logBuffer bytes.Buffer
	common.LogWriterMu.Lock()
	previousErrorWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previousErrorWriter
		common.LogWriterMu.Unlock()
	})

	cleanupAuthArtifacts()
	assert.Empty(t, logBuffer.String(), "the hourly alert uses a strict greater-than threshold")
	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Zero(t, count)

	exceededRows := make([]model.UserSession, 0, 3)
	for i := range 3 {
		exceededRows = append(exceededRows, model.UserSession{
			SID: "hourly-exceeded-" + string(rune('a'+i)), UserID: 1, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "hash", LoginMethod: "password",
			CreatedAt: now.Add(-2 * time.Second).Unix(), LastActiveAt: now.Add(-time.Hour).Unix(), ExpiresAt: now.Add(-time.Minute).Unix(),
		})
	}
	require.NoError(t, model.DB.Create(&exceededRows).Error)
	logBuffer.Reset()
	cleanupAuthArtifacts()
	assert.Contains(t, logBuffer.String(), "hourly user session issuance exceeded alert threshold")
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Zero(t, count, "alerting must happen before expired rows are deleted")
}

func TestCleanupAuthArtifactsRemovesOnlyExpiredRecords(t *testing.T) {
	setupAuthSessionTestDB(t)
	now := time.Now()
	oldExpiry := now.Add(-25 * time.Hour)
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "expired-session", UserID: 1, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "hash", LoginMethod: "password",
		CreatedAt: oldExpiry.Unix(), LastActiveAt: oldExpiry.Unix(), ExpiresAt: oldExpiry.Unix(),
	}).Error)
	require.NoError(t, model.DB.Create(&model.AuthFlow{
		TokenHash: "expired-flow", Purpose: model.AuthFlowPurposeTwoFALogin,
		ExpiresAt: oldExpiry,
	}).Error)
	require.NoError(t, model.DB.Create(&model.AuthFlow{
		TokenHash: "recent-flow", Purpose: model.AuthFlowPurposeTwoFALogin,
		ExpiresAt: now.Add(time.Minute),
	}).Error)

	cleanupAuthArtifacts()

	var sessionCount int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&sessionCount).Error)
	assert.Zero(t, sessionCount)
	var flows []model.AuthFlow
	require.NoError(t, model.DB.Find(&flows).Error)
	require.Len(t, flows, 1)
	assert.Equal(t, "recent-flow", flows[0].TokenHash)
}

func TestCleanupAuthArtifactsContinuesWithRevokedCleanupAfterExpiredBatchFailure(t *testing.T) {
	setupAuthSessionTestDB(t)
	now := time.Now()
	oldCreatedAt := now.Add(-8 * 24 * time.Hour).Unix()
	require.NoError(t, model.DB.Create(&[]model.UserSession{
		{
			SID: "failed-expired-cleanup", UserID: 1, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "hash-expired", LoginMethod: "password",
			CreatedAt: oldCreatedAt, LastActiveAt: oldCreatedAt, ExpiresAt: now.Add(-time.Minute).Unix(),
		},
		{
			SID: "independent-revoked-cleanup", UserID: 1, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusRevoked, RefreshHash: "hash-revoked", LoginMethod: "password",
			CreatedAt: oldCreatedAt, LastActiveAt: oldCreatedAt, ExpiresAt: now.Add(time.Hour).Unix(),
			RevokedAt: now.Add(-8 * 24 * time.Hour).Unix(),
		},
	}).Error)

	forcedErr := errors.New("forced expired cleanup failure")
	callbackName := "test:fail_first_user_session_cleanup_batch"
	failedFirstDelete := false
	require.NoError(t, model.DB.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" && !failedFirstDelete {
			failedFirstDelete = true
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Delete().Remove(callbackName) })

	cleanupAuthArtifacts()

	var expired model.UserSession
	require.NoError(t, model.DB.First(&expired, "sid = ?", "failed-expired-cleanup").Error)
	var revoked model.UserSession
	assert.ErrorIs(t, model.DB.First(&revoked, "sid = ?", "independent-revoked-cleanup").Error, gorm.ErrRecordNotFound)
}

func TestLoginSessionCreateRefreshAndRevoke(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)

	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	assert.NotEmpty(t, bundle.RefreshToken)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	_, cachedUser, err := ValidateLoginSession(identity)
	require.NoError(t, err)
	assert.Equal(t, user.Id, cachedUser.Id)
	require.NoError(t, RevokeByRefreshToken(bundle.Session.SID+".wrong-refresh-secret", "", "logout"))
	_, _, err = ValidateLoginSession(identity)
	require.NoError(t, err, "a caller that only knows sid must not be able to revoke the session")

	refreshed, _, err := RefreshLoginSession(bundle.RefreshToken, bundle.Session.SID, "127.0.0.2", "test-agent-2")
	require.NoError(t, err)
	assert.NotEqual(t, bundle.RefreshToken, refreshed.RefreshToken)
	recovered, _, err := RefreshLoginSession(bundle.RefreshToken, bundle.Session.SID, "127.0.0.2", "test-agent-2")
	require.NoError(t, err)
	assert.Equal(t, refreshed.RefreshToken, recovered.RefreshToken, "a concurrent refresh must recover the winner's rotated token")

	_, _, err = RefreshLoginSession(refreshed.RefreshToken, "different-session", "127.0.0.2", "test-agent-2")
	assert.ErrorIs(t, err, ErrLoginSessionMismatch)

	require.NoError(t, RevokeByRefreshToken(refreshed.RefreshToken, refreshed.Session.SID, "logout"))
	_, _, err = ValidateLoginSession(identity)
	assert.True(t, errors.Is(err, ErrLoginSessionRevoked))
}

func TestIndependentRedisSessionRevokeConvergesAfterCacheTTL(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	_, clientA, serverB, clientB := useIndependentAuthSessionRedis(t)

	common.RDB = clientA
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "node-a")
	require.NoError(t, err)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)

	common.RDB = clientB
	_, _, err = ValidateLoginSession(identity)
	require.NoError(t, err)
	assert.NotEmpty(t, cachedLoginSessionKey(t, serverB), "node B must hold its own session cache entry")

	common.RDB = clientA
	require.NoError(t, RevokeByRefreshToken(bundle.RefreshToken, bundle.Session.SID, "logout"))

	serverB.FastForward(3 * time.Second)
	common.RDB = clientB
	_, _, err = ValidateLoginSession(identity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
}

func TestIndependentRedisAuthVersionAdvanceConvergesAfterCacheTTL(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	_, clientA, serverB, clientB := useIndependentAuthSessionRedis(t)

	common.RDB = clientA
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "node-a")
	require.NoError(t, err)
	oldIdentity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)

	common.RDB = clientB
	_, _, err = ValidateLoginSession(oldIdentity)
	require.NoError(t, err)
	cacheKey := cachedLoginSessionKey(t, serverB)
	version := serverB.HGet(cacheKey, "Version")
	assert.Equal(t, "1", version, "node B must hold the pre-rotation session version")

	common.RDB = clientA
	rotated, err := AdvanceCurrentSessionSecurity(oldIdentity, "security_update")
	require.NoError(t, err)
	newIdentity, err := ParseAccessToken(rotated.AccessToken)
	require.NoError(t, err)
	assert.Greater(t, newIdentity.SessionVersion, oldIdentity.SessionVersion)
	assert.Greater(t, newIdentity.UserAuthVersion, oldIdentity.UserAuthVersion)

	serverB.FastForward(3 * time.Second)
	common.RDB = clientB
	_, _, err = ValidateLoginSession(newIdentity)
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(oldIdentity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
}

func TestUserAuthVersionInvalidatesExistingSession(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)

	_, err = model.BumpUserAuthVersion(user.Id)
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(identity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
	_, err = CreateLoginSessionAtAuthVersion(user.Id, identity.UserAuthVersion, "2fa", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, ErrLoginSessionRevoked, "a pending 2FA flow must not survive an auth-version change")
}
