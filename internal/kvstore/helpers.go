package kvstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// HashKeyPart returns the lowercase hex-encoded SHA-256 digest of value.
func HashKeyPart(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// KVGetJSONRequired fetches a JSON value from the active KV store if KV mode is enabled.
func KVGetJSONRequired(ctx context.Context, key string, out any) (bool, bool, error) {
	client, kvMode, errClient := CurrentKVClient()
	if !kvMode || errClient != nil {
		return kvMode, false, errClient
	}
	raw, found, errGet := client.KVGet(ctx, key)
	if errGet != nil || !found {
		return true, false, errGet
	}
	if errUnmarshal := json.Unmarshal(raw, out); errUnmarshal != nil {
		return true, false, errUnmarshal
	}
	return true, true, nil
}

// KVSetJSONRequired marshals value as JSON and writes it to the active KV store if KV mode is enabled.
func KVSetJSONRequired(ctx context.Context, key string, value any, ttl time.Duration) (bool, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return false, errMarshal
	}
	return KVSetBytesRequired(ctx, key, raw, ttl)
}

// KVSetBytesRequired writes raw bytes to the active KV store if KV mode is enabled.
func KVSetBytesRequired(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	client, kvMode, errClient := CurrentKVClient()
	if !kvMode || errClient != nil {
		return kvMode, errClient
	}
	written, errSet := client.KVSet(ctx, key, value, kvSetOptionsForTTL(ttl))
	if errSet != nil {
		return true, errSet
	}
	if !written {
		return true, fmt.Errorf("home kv store unavailable")
	}
	return true, nil
}

// KVSetNXRequired conditionally writes value only if key does not exist in the active KV store.
func KVSetNXRequired(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, bool, error) {
	client, kvMode, errClient := CurrentKVClient()
	if !kvMode || errClient != nil {
		return kvMode, false, errClient
	}
	written, errSet := client.KVSetNX(ctx, key, value, ttl)
	return true, written, errSet
}

// KVDelRequired deletes keys from the active KV store if KV mode is enabled.
func KVDelRequired(ctx context.Context, keys ...string) (bool, int64, error) {
	client, kvMode, errClient := CurrentKVClient()
	if !kvMode || errClient != nil {
		return kvMode, 0, errClient
	}
	deleted, errDel := client.KVDel(ctx, keys...)
	return true, deleted, errDel
}

// KVExpireRequired updates the expiration of key in the active KV store if KV mode is enabled.
func KVExpireRequired(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	client, kvMode, errClient := CurrentKVClient()
	if !kvMode || errClient != nil {
		return kvMode, errClient
	}
	_, errExpire := client.KVExpire(ctx, key, ttl)
	return true, errExpire
}

// KVGetJSONBestEffort fetches a JSON value from the active KV store, logging and swallowing errors.
func KVGetJSONBestEffort(ctx context.Context, key string, out any) (bool, bool) {
	kvMode, found, errGet := KVGetJSONRequired(ctx, key, out)
	if errGet != nil {
		log.Errorf("home kv best-effort get failed prefix=%s: %v", kvLogPrefix(key), errGet)
		return kvMode, false
	}
	return kvMode, found
}

// KVSetJSONBestEffort marshals and stores value in the active KV store, logging and swallowing errors.
func KVSetJSONBestEffort(ctx context.Context, key string, value any, ttl time.Duration) bool {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		log.Errorf("home kv best-effort set failed prefix=%s: %v", kvLogPrefix(key), errMarshal)
		return false
	}
	return KVSetBytesBestEffort(ctx, key, raw, ttl)
}

// KVSetBytesBestEffort stores raw bytes in the active KV store, logging and swallowing errors.
func KVSetBytesBestEffort(ctx context.Context, key string, value []byte, ttl time.Duration) bool {
	kvMode, errSet := KVSetBytesRequired(ctx, key, value, ttl)
	if !kvMode {
		return false
	}
	if errSet != nil {
		log.Errorf("home kv best-effort set failed prefix=%s: %v", kvLogPrefix(key), errSet)
		return false
	}
	return true
}

// KVSetNXBestEffort conditionally stores value if key does not exist, logging and swallowing errors.
func KVSetNXBestEffort(ctx context.Context, key string, value []byte, ttl time.Duration) bool {
	kvMode, written, errSet := KVSetNXRequired(ctx, key, value, ttl)
	if !kvMode {
		return false
	}
	if errSet != nil {
		log.Errorf("home kv best-effort setnx failed prefix=%s: %v", kvLogPrefix(key), errSet)
		return false
	}
	return written
}

// KVDelBestEffort deletes keys from the active KV store, logging and swallowing errors.
func KVDelBestEffort(ctx context.Context, keys ...string) bool {
	kvMode, _, errDel := KVDelRequired(ctx, keys...)
	if !kvMode {
		return false
	}
	if errDel != nil {
		log.Errorf("home kv best-effort del failed prefix=%s: %v", kvLogPrefix(firstKVKey(keys)), errDel)
		return false
	}
	return true
}

// KVExpireBestEffort updates key expiration in the active KV store, logging and swallowing errors.
func KVExpireBestEffort(ctx context.Context, key string, ttl time.Duration) bool {
	kvMode, errExpire := KVExpireRequired(ctx, key, ttl)
	if !kvMode {
		return false
	}
	if errExpire != nil {
		log.Errorf("home kv best-effort expire failed prefix=%s: %v", kvLogPrefix(key), errExpire)
		return false
	}
	return true
}

func kvSetOptionsForTTL(ttl time.Duration) KVSetOptions {
	if ttl <= 0 {
		return KVSetOptions{}
	}
	return KVSetOptions{EX: ttl}
}

func kvLogPrefix(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "unknown"
	}
	parts := strings.Split(key, ":")
	if len(parts) >= 2 {
		return parts[0] + ":" + parts[1] + ":*"
	}
	return parts[0] + ":*"
}

func firstKVKey(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}
