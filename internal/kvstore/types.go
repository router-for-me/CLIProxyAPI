// Package kvstore provides a unified key-value storage abstraction and a local
// bbolt-backed implementation with binary TTL envelopes.
package kvstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ErrCompareAndSwapUnsupported reports that the backing KV store does not support CAS.
var ErrCompareAndSwapUnsupported = errors.New("kvstore: compare-and-swap command is not supported")

// KVSetOptions configures optional expiration and conditional write behavior for KVSet.
type KVSetOptions struct {
	EX time.Duration
	PX time.Duration
	NX bool
	XX bool
}

// KVClient defines the key-value storage contract shared by Home KV and local BoltStore.
type KVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts KVSetOptions) (bool, error)
	KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error)
	KVDel(ctx context.Context, keys ...string) (int64, error)
	KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)
	KVTTL(ctx context.Context, key string) (time.Duration, bool, error)
	KVIncrBy(ctx context.Context, key string, delta int64) (int64, error)
	KVMGet(ctx context.Context, keys ...string) ([][]byte, []bool, error)
	KVMSet(ctx context.Context, pairs map[string][]byte) error
}

// BuildSetArgs validates KVSet arguments and builds the canonical Redis-style argument list.
func BuildSetArgs(key string, value []byte, opts KVSetOptions) ([]any, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("home kv: key is empty")
	}
	if opts.EX > 0 && opts.PX > 0 {
		return nil, fmt.Errorf("home kv: EX and PX are mutually exclusive")
	}
	if opts.EX < 0 || opts.PX < 0 {
		return nil, fmt.Errorf("home kv: ttl must not be negative")
	}
	if opts.NX && opts.XX {
		return nil, fmt.Errorf("home kv: NX and XX are mutually exclusive")
	}

	args := []any{key, append([]byte(nil), value...)}
	if opts.EX > 0 {
		args = append(args, "EX", DurationCeil(opts.EX, time.Second))
	}
	if opts.PX > 0 {
		args = append(args, "PX", DurationCeil(opts.PX, time.Millisecond))
	}
	if opts.NX {
		args = append(args, "NX")
	}
	if opts.XX {
		args = append(args, "XX")
	}
	return args, nil
}

// DurationCeil divides value by unit, rounding positive remainders up to the next integer unit.
func DurationCeil(value time.Duration, unit time.Duration) int64 {
	if value <= 0 || unit <= 0 {
		return 0
	}
	q := int64(value / unit)
	if value%unit != 0 && q < math.MaxInt64 {
		q++
	}
	return q
}
