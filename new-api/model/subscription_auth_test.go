package model

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSubscriptionGroupTransitionsPreserveAuthVersionAndSessions(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	now := time.Now().Unix()
	user := User{
		Username:    "subscription-auth-user",
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
	}
	require.NoError(t, DB.Create(&user).Error)
	require.NoError(t, CreateUserSession(&UserSession{
		SID:             "subscription-auth-session",
		UserID:          user.Id,
		Version:         1,
		UserAuthVersion: 1,
		Status:          UserSessionStatusActive,
		RefreshHash:     "refresh-hash",
		LoginMethod:     "password",
		LastActiveAt:    now,
		ExpiresAt:       now + 3600,
	}))
	require.NoError(t, populateUserCache(user))
	plan := &SubscriptionPlan{
		Title:         "Upgraded",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   100,
		UpgradeGroup:  "pro",
		Enabled:       true,
	}
	require.NoError(t, DB.Create(plan).Error)

	subscription, err := CreateUserSubscriptionFromPlanTx(DB, user.Id, plan, "test")
	require.NoError(t, err)
	require.Equal(t, "default", subscription.PrevUserGroup)
	require.NoError(t, RefreshUserGroupCache(user.Id))

	var updated User
	require.NoError(t, DB.First(&updated, user.Id).Error)
	assert.Equal(t, "pro", updated.Group)
	assert.EqualValues(t, 1, updated.AuthVersion)
	var session UserSession
	require.NoError(t, DB.First(&session, "sid = ?", "subscription-auth-session").Error)
	assert.Equal(t, UserSessionStatusActive, session.Status)
	cached, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, "pro", cached.Group)
	assert.EqualValues(t, 1, cached.AuthVersion)

	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		target, err := downgradeUserGroupForSubscriptionTx(tx, subscription, now+1)
		assert.Equal(t, "default", target)
		return err
	}))
	require.NoError(t, RefreshUserGroupCache(user.Id))
	require.NoError(t, DB.First(&updated, user.Id).Error)
	assert.Equal(t, "default", updated.Group)
	assert.EqualValues(t, 1, updated.AuthVersion)
	require.NoError(t, DB.First(&session, "sid = ?", "subscription-auth-session").Error)
	assert.Equal(t, UserSessionStatusActive, session.Status)
	cached, err = GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, "default", cached.Group)
}

func TestSubscriptionGroupCacheRefreshFailureDoesNotChangeCommittedResult(t *testing.T) {
	previousDB, previousLogDB := DB, LOG_DB
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB, LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(&User{}, &SubscriptionPlan{}, &UserSubscription{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		_ = sqlDB.Close()
	})

	user := User{
		Username:    "subscription-cache-failure",
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
	}
	require.NoError(t, DB.Create(&user).Error)
	plan := &SubscriptionPlan{
		Title:         "Cache failure plan",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   100,
		UpgradeGroup:  "pro",
		Enabled:       true,
	}
	require.NoError(t, DB.Create(plan).Error)
	InvalidateSubscriptionPlanCache(plan.Id)

	oldRedisEnabled, oldRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("forced redis failure")
		},
		MaxRetries: -1,
	})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled, common.RDB = oldRedisEnabled, oldRDB
	})

	message, err := AdminBindSubscription(user.Id, plan.Id, "test")
	require.NoError(t, err)
	assert.Contains(t, message, "pro")

	var updated User
	require.NoError(t, DB.First(&updated, user.Id).Error)
	assert.Equal(t, "pro", updated.Group)
	assert.EqualValues(t, 1, updated.AuthVersion)
	var subscription UserSubscription
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&subscription).Error)
	assert.Equal(t, "active", subscription.Status)
}
