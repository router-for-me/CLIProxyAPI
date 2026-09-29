package model

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// User auth cache fencing uses three Redis keys per user: the cached user
// hash, a short-lived pending fence published before a restrictive database
// transaction, and a monotonic committed version floor published after
// commit. Cache writes below either floor are rejected, readers below the
// effective floor fall back to the database, and the pending fence outlives
// every user-hash TTL so a rolled-back transaction heals without allowing a
// stale snapshot to re-authorize the user.

var ErrUserAuthCachePending = errors.New("user authentication state update is pending")

var ErrUserAuthVersionConflict = errors.New("user authentication version update conflicted")

func getUserAuthFenceKey(userId int) string {
	return fmt.Sprintf("auth:user:fence:%d", userId)
}

func getUserAuthVersionKey(userId int) string {
	return fmt.Sprintf("auth:user:version:%d", userId)
}

// A pending fence only covers the interval between publishing the next
// version and the surrounding database transaction reaching a decision. Its
// TTL must outlive every user hash that could have been populated before the
// fence, while still allowing an automatically rolled-back transaction to
// recover without an operator repairing Redis.
func userAuthFenceTTLSeconds() int {
	cacheTTL := userCacheTTLSeconds()
	extra := max(cacheTTL, 60)
	return cacheTTL + extra
}

func writeUserCache(user *UserBase, includeQuota bool) error {
	if user == nil || user.Id <= 0 || !common.RedisEnabled {
		return nil
	}
	user.CacheSchema = userCacheSchemaVersion
	if user.AuthVersion <= 0 {
		return fmt.Errorf("invalid user auth version")
	}
	includeQuotaArg := "0"
	if includeQuota {
		includeQuotaArg = "1"
	}
	ttl := userCacheTTLSeconds()
	const script = `
local incoming = tonumber(ARGV[1])
local pending = tonumber(redis.call('GET', KEYS[2]) or '0')
local committed = tonumber(redis.call('GET', KEYS[3]) or '0')
local current = tonumber(redis.call('HGET', KEYS[1], 'AuthVersion') or '0')
if pending > incoming or committed > incoming or current > incoming then
  return 0
end
if committed < incoming then
  redis.call('SET', KEYS[3], ARGV[1])
end
if pending > 0 and pending <= incoming then
  redis.call('DEL', KEYS[2])
end
if ARGV[10] == '0' and redis.call('EXISTS', KEYS[1]) == 0 then
  return 1
end
redis.call('HSET', KEYS[1],
  'Id', ARGV[2], 'Group', ARGV[3], 'Email', ARGV[4],
  'Status', ARGV[5], 'Role', ARGV[6], 'Username', ARGV[7],
  'Setting', ARGV[8], 'AuthVersion', ARGV[1], 'CacheSchema', ARGV[9])
if ARGV[10] == '1' and redis.call('HEXISTS', KEYS[1], 'Quota') == 0 then
  redis.call('HSET', KEYS[1], 'Quota', ARGV[11])
end
redis.call('EXPIRE', KEYS[1], ARGV[12])
return 1`
	result, err := common.RDB.Eval(context.Background(), script,
		[]string{getUserCacheKey(user.Id), getUserAuthFenceKey(user.Id), getUserAuthVersionKey(user.Id)},
		user.AuthVersion, user.Id, user.Group, user.Email, user.Status, user.Role,
		user.Username, user.Setting, user.CacheSchema, includeQuotaArg, user.Quota, ttl,
	).Int()
	if err != nil {
		return err
	}
	if result == 0 {
		return ErrUserAuthCachePending
	}
	return nil
}

func getUserAuthVersionFloor(userId int) (int64, error) {
	if !common.RedisEnabled {
		return 0, nil
	}
	values, err := common.RDB.MGet(context.Background(), getUserAuthFenceKey(userId), getUserAuthVersionKey(userId)).Result()
	if err != nil {
		return 0, err
	}
	var floor int64
	for _, value := range values {
		if value == nil {
			continue
		}
		parsed, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		if err != nil {
			return 0, err
		}
		if parsed > floor {
			floor = parsed
		}
	}
	return floor, nil
}

// SetUserAuthVersionFence publishes a fail-closed version before a restrictive
// database update. Pending fences expire only after every pre-existing user
// hash must have expired; a committed update is promoted separately to a
// permanent monotonic version floor.
func SetUserAuthVersionFence(userId int, authVersion int64) error {
	if !common.RedisEnabled {
		return nil
	}
	if userId <= 0 || authVersion <= 0 {
		return fmt.Errorf("invalid user auth fence")
	}
	const script = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
local incoming = tonumber(ARGV[1])
if current < incoming then
  redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
elseif current == incoming then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
elseif redis.call('TTL', KEYS[1]) < 0 then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
end
return 1`
	return common.RDB.Eval(context.Background(), script, []string{getUserAuthFenceKey(userId)}, authVersion, userAuthFenceTTLSeconds()).Err()
}

// publishCommittedUserAuthVersion records the durable lower bound used to
// reject an arbitrarily delayed cache fill after a committed security change.
// It also removes this transaction's now-obsolete pending fence.
func publishCommittedUserAuthVersion(userId int, authVersion int64) error {
	if !common.RedisEnabled {
		return nil
	}
	if userId <= 0 || authVersion <= 0 {
		return fmt.Errorf("invalid committed user auth version")
	}
	const script = `
local incoming = tonumber(ARGV[1])
local committed = tonumber(redis.call('GET', KEYS[1]) or '0')
local pending = tonumber(redis.call('GET', KEYS[2]) or '0')
if committed < incoming then
  redis.call('SET', KEYS[1], ARGV[1])
end
if pending > 0 and pending <= incoming then
  redis.call('DEL', KEYS[2])
end
return 1`
	return common.RDB.Eval(context.Background(), script,
		[]string{getUserAuthVersionKey(userId), getUserAuthFenceKey(userId)}, authVersion,
	).Err()
}

// IncrementUserAuthVersionWithTx locks the user, publishes the next deny
// fence, then persists the version in the caller's transaction. Unscoped is
// intentional so the same fail-closed path also covers hard deletion of an
// already soft-deleted user.
func IncrementUserAuthVersionWithTx(tx *gorm.DB, userId int) (int64, error) {
	if tx == nil || userId <= 0 {
		return 0, fmt.Errorf("invalid user auth version update")
	}
	for range 3 {
		var user User
		if err := lockForUpdate(tx.Unscoped()).Select("id", "auth_version").Where("id = ?", userId).First(&user).Error; err != nil {
			return 0, err
		}
		current := max(user.AuthVersion, 1)
		next := current + 1
		if err := SetUserAuthVersionFence(userId, next); err != nil {
			return 0, err
		}
		result := tx.Unscoped().Model(&User{}).
			Where("id = ? AND auth_version = ?", userId, user.AuthVersion).
			Update("auth_version", next)
		if result.Error != nil {
			return 0, result.Error
		}
		if result.RowsAffected == 1 {
			return next, nil
		}
	}
	return 0, ErrUserAuthVersionConflict
}

// BumpUserAuthVersion is the transaction-owning variant used by password,
// role, status and security-factor changes outside another transaction.
func BumpUserAuthVersion(userId int) (int64, error) {
	var next int64
	if err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		next, err = IncrementUserAuthVersionWithTx(tx, userId)
		return err
	}); err != nil {
		return 0, err
	}
	if err := PublishUserAuthCache(userId); err != nil {
		return next, err
	}
	return next, nil
}

// PublishUserAuthCache refreshes the current database state after a successful
// auth-sensitive transaction without touching the cached quota field.
func PublishUserAuthCache(userId int) error {
	user, err := GetUserById(userId, false)
	if err != nil {
		return err
	}
	return updateUserCache(*user)
}

// InitializeUserAuthVersions must run after AutoMigrate when upgrading an
// existing database. It is idempotent and portable across all supported DBs.
func InitializeUserAuthVersions() error {
	return DB.Model(&User{}).Where("auth_version IS NULL OR auth_version < ?", 1).Update("auth_version", 1).Error
}

func updateUserCacheFieldAtVersion(userId int, field string, value any, authVersion int64) error {
	if !common.RedisEnabled {
		return nil
	}
	if userId <= 0 || authVersion <= 0 {
		return fmt.Errorf("invalid user auth version")
	}
	const script = `
local incoming = tonumber(ARGV[1])
local pending = tonumber(redis.call('GET', KEYS[2]) or '0')
local committed = tonumber(redis.call('GET', KEYS[3]) or '0')
local current = tonumber(redis.call('HGET', KEYS[1], 'AuthVersion') or '0')
if pending > incoming or committed > incoming or current > incoming then
  return 0
end
if committed < incoming then
  redis.call('SET', KEYS[3], ARGV[1])
end
if pending > 0 and pending <= incoming then
  redis.call('DEL', KEYS[2])
end
if redis.call('EXISTS', KEYS[1]) == 0 then
  return 1
end
if current ~= incoming then
  return 1
end
redis.call('HSET', KEYS[1], ARGV[2], ARGV[3], 'CacheSchema', ARGV[4])
return 1`
	result, err := common.RDB.Eval(context.Background(), script,
		[]string{getUserCacheKey(userId), getUserAuthFenceKey(userId), getUserAuthVersionKey(userId)},
		authVersion, field, value, userCacheSchemaVersion,
	).Int()
	if err != nil {
		return err
	}
	if result == 0 {
		return ErrUserAuthCachePending
	}
	return nil
}
