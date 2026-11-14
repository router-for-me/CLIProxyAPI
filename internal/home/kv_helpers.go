package home

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

func init() {
	kvstore.RegisterHomeProvider(currentHomeKVClient)
}

func currentHomeKVClient() (kvstore.KVClient, bool, error) {
	client := Current()
	if client == nil {
		return nil, false, nil
	}
	if !client.Enabled() {
		return nil, true, fmt.Errorf("home kv store unavailable: %w", ErrDisabled)
	}
	if !client.HeartbeatOK() {
		return nil, true, fmt.Errorf("home kv store unavailable: %w", ErrNotConnected)
	}
	return client, true, nil
}

func HashKeyPart(value string) string {
	return kvstore.HashKeyPart(value)
}

func CurrentKVClient() (kvstore.KVClient, bool, error) {
	return kvstore.CurrentKVClient()
}

func KVGetJSONRequired(ctx context.Context, key string, out any) (bool, bool, error) {
	return kvstore.KVGetJSONRequired(ctx, key, out)
}

func KVSetJSONRequired(ctx context.Context, key string, value any, ttl time.Duration) (bool, error) {
	return kvstore.KVSetJSONRequired(ctx, key, value, ttl)
}

func KVSetBytesRequired(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return kvstore.KVSetBytesRequired(ctx, key, value, ttl)
}

func KVSetNXRequired(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, bool, error) {
	return kvstore.KVSetNXRequired(ctx, key, value, ttl)
}

func KVDelRequired(ctx context.Context, keys ...string) (bool, int64, error) {
	return kvstore.KVDelRequired(ctx, keys...)
}

func KVExpireRequired(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return kvstore.KVExpireRequired(ctx, key, ttl)
}

func KVGetJSONBestEffort(ctx context.Context, key string, out any) (bool, bool) {
	return kvstore.KVGetJSONBestEffort(ctx, key, out)
}

func KVSetJSONBestEffort(ctx context.Context, key string, value any, ttl time.Duration) bool {
	return kvstore.KVSetJSONBestEffort(ctx, key, value, ttl)
}

func KVSetBytesBestEffort(ctx context.Context, key string, value []byte, ttl time.Duration) bool {
	return kvstore.KVSetBytesBestEffort(ctx, key, value, ttl)
}

func KVSetNXBestEffort(ctx context.Context, key string, value []byte, ttl time.Duration) bool {
	return kvstore.KVSetNXBestEffort(ctx, key, value, ttl)
}

func KVDelBestEffort(ctx context.Context, keys ...string) bool {
	return kvstore.KVDelBestEffort(ctx, keys...)
}

func KVExpireBestEffort(ctx context.Context, key string, ttl time.Duration) bool {
	return kvstore.KVExpireBestEffort(ctx, key, ttl)
}
