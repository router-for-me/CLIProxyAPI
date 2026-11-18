package kvstore

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"go.etcd.io/bbolt"
)

const (
	envelopeVersionV1      byte = 0x01
	envelopeHeaderSize          = 9
	defaultSweepInterval        = 5 * time.Minute
	defaultSweepBatchSize       = 256
	defaultBoltOpenTimeout      = 5 * time.Second
)

var (
	boltKVBucket   = []byte("cpa_kv")
	errStoreClosed = errors.New("kvstore: bolt store is not open")

	_ KVClient = (*BoltStore)(nil)
)

func encodeEnvelope(value []byte, expiresAtUnixNano int64) []byte {
	buf := make([]byte, envelopeHeaderSize+len(value))
	buf[0] = envelopeVersionV1
	binary.BigEndian.PutUint64(buf[1:envelopeHeaderSize], uint64(expiresAtUnixNano))
	copy(buf[envelopeHeaderSize:], value)
	return buf
}

func decodeEnvelope(raw []byte, nowUnixNano int64) (payload []byte, expiresAt int64, expired bool, err error) {
	if len(raw) < envelopeHeaderSize {
		return nil, 0, false, fmt.Errorf("kvstore: corrupted entry (len=%d)", len(raw))
	}
	if raw[0] != envelopeVersionV1 {
		return nil, 0, false, fmt.Errorf("kvstore: unsupported envelope version %d", raw[0])
	}
	exp := int64(binary.BigEndian.Uint64(raw[1:envelopeHeaderSize]))
	if exp > 0 && nowUnixNano >= exp {
		return nil, exp, true, nil
	}
	return raw[envelopeHeaderSize:], exp, false, nil
}

// BoltStoreOption configures optional behavior on BoltStore.
type BoltStoreOption func(*BoltStore)

// WithNowFunc overrides the clock function used for TTL evaluation.
func WithNowFunc(fn func() time.Time) BoltStoreOption {
	return func(s *BoltStore) {
		if fn != nil {
			s.nowFunc = fn
		}
	}
}

// WithSweepInterval sets the background expired-key sweep interval.
// A non-positive interval disables the background sweeper goroutine.
func WithSweepInterval(interval time.Duration) BoltStoreOption {
	return func(s *BoltStore) {
		s.sweepInterval = interval
	}
}

// WithSweepBatchSize sets the maximum number of expired keys deleted per sweep batch.
func WithSweepBatchSize(batchSize int) BoltStoreOption {
	return func(s *BoltStore) {
		if batchSize > 0 {
			s.sweepBatchSize = batchSize
		}
	}
}

// BoltStore implements KVClient backed by a local bbolt database file.
type BoltStore struct {
	db                *bbolt.DB
	path              string
	nowFunc           func() time.Time
	sweepInterval     time.Duration
	sweepBatchSize    int
	stopCh            chan struct{}
	doneCh            chan struct{}
	closeOnce         sync.Once
	closeErr          error
	runUpdate         func(func(*bbolt.Tx) error) error
	beforeSweepDelete func(batch [][]byte)
}

// OpenBoltStore resolves path, creates parent directories, opens the bbolt file,
// ensures the KV bucket exists, and starts the background expiration sweeper.
func OpenBoltStore(path string, opts ...BoltStoreOption) (*BoltStore, error) {
	resolvedPath, errResolve := ResolvePath(path)
	if errResolve != nil {
		return nil, errResolve
	}
	if errMkdir := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); errMkdir != nil {
		return nil, fmt.Errorf("kvstore: create parent directory: %w", errMkdir)
	}
	db, errOpen := bbolt.Open(resolvedPath, 0o600, &bbolt.Options{Timeout: defaultBoltOpenTimeout})
	if errOpen != nil {
		return nil, fmt.Errorf("kvstore: open bolt db: %w", errOpen)
	}
	if errInit := db.Update(func(tx *bbolt.Tx) error {
		_, errBucket := tx.CreateBucketIfNotExists(boltKVBucket)
		return errBucket
	}); errInit != nil {
		_ = db.Close()
		return nil, fmt.Errorf("kvstore: initialize bucket: %w", errInit)
	}

	store := &BoltStore{
		db:             db,
		path:           resolvedPath,
		nowFunc:        time.Now,
		sweepInterval:  defaultSweepInterval,
		sweepBatchSize: defaultSweepBatchSize,
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
		runUpdate:      db.Update,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}
	if store.nowFunc == nil {
		store.nowFunc = time.Now
	}
	if store.runUpdate == nil {
		store.runUpdate = db.Update
	}
	if store.sweepBatchSize <= 0 {
		store.sweepBatchSize = defaultSweepBatchSize
	}
	if store.sweepInterval > 0 {
		go store.runSweeper()
	} else {
		close(store.doneCh)
	}
	return store, nil
}

// Path returns the resolved absolute file path of the underlying bbolt database.
func (s *BoltStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Close stops the background sweeper and closes the underlying bbolt database.
// Calling Close multiple times is safe and idempotent.
func (s *BoltStore) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		close(s.stopCh)
		<-s.doneCh
		if s.db != nil {
			s.closeErr = s.db.Close()
		}
	})
	return s.closeErr
}

func (s *BoltStore) now() time.Time {
	if s != nil && s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

func (s *BoltStore) view(fn func(tx *bbolt.Tx, now time.Time) error) error {
	return s.db.View(func(tx *bbolt.Tx) error {
		return fn(tx, s.now())
	})
}

func (s *BoltStore) update(fn func(tx *bbolt.Tx, now time.Time) error) error {
	runner := s.db.Update
	if s.runUpdate != nil {
		runner = s.runUpdate
	}
	return runner(func(tx *bbolt.Tx) error {
		return fn(tx, s.now())
	})
}

func checkContext(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func ceilDurationTTL(value time.Duration, unit time.Duration) (time.Duration, error) {
	if value <= 0 || unit <= 0 {
		return 0, nil
	}
	units := DurationCeil(value, unit)
	if units > math.MaxInt64/int64(unit) {
		return 0, fmt.Errorf("kvstore: ttl duration overflows int64 nanoseconds")
	}
	return time.Duration(units * int64(unit)), nil
}

func computeExpiresAtUnixNano(now time.Time, ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return 0, nil
	}
	if now.IsZero() {
		return 0, fmt.Errorf("kvstore: current time is zero")
	}
	sec := now.Unix()
	nsec := int64(now.Nanosecond())
	const nsPerSec = int64(time.Second)
	if sec > math.MaxInt64/nsPerSec || sec < math.MinInt64/nsPerSec {
		return 0, fmt.Errorf("kvstore: current time overflows unix nanoseconds")
	}
	secNano := sec * nsPerSec
	if (nsec > 0 && secNano > math.MaxInt64-nsec) || (nsec < 0 && secNano < math.MinInt64-nsec) {
		return 0, fmt.Errorf("kvstore: current time overflows unix nanoseconds")
	}
	nowNano := secNano + nsec
	ttlNano := int64(ttl)
	if nowNano > 0 && ttlNano > math.MaxInt64-nowNano {
		return 0, fmt.Errorf("kvstore: expiration time overflows int64 nanoseconds")
	}
	expiresAt := nowNano + ttlNano
	if expiresAt <= 0 {
		return 0, fmt.Errorf("kvstore: expiration time must be positive unix nanoseconds")
	}
	return expiresAt, nil
}

// KVGet retrieves the value for key if it exists and has not expired.
func (s *BoltStore) KVGet(ctx context.Context, key string) ([]byte, bool, error) {
	if errCtx := checkContext(ctx); errCtx != nil {
		return nil, false, errCtx
	}
	if s == nil || s.db == nil {
		return nil, false, errStoreClosed
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, false, fmt.Errorf("home kv: key is empty")
	}
	var value []byte
	var found bool
	errView := s.view(func(tx *bbolt.Tx, now time.Time) error {
		nowNano := now.UnixNano()
		b := tx.Bucket(boltKVBucket)
		if b == nil {
			return nil
		}
		raw := b.Get([]byte(key))
		if raw == nil {
			return nil
		}
		payload, _, expired, errDecode := decodeEnvelope(raw, nowNano)
		if errDecode != nil {
			return errDecode
		}
		if expired {
			return nil
		}
		value = bytes.Clone(payload)
		if value == nil {
			value = []byte{}
		}
		found = true
		return nil
	})
	if errView != nil {
		return nil, false, errView
	}
	return value, found, nil
}

// KVSet stores key and value with optional TTL and NX/XX conditions.
func (s *BoltStore) KVSet(ctx context.Context, key string, value []byte, opts KVSetOptions) (bool, error) {
	if errCtx := checkContext(ctx); errCtx != nil {
		return false, errCtx
	}
	if s == nil || s.db == nil {
		return false, errStoreClosed
	}
	if _, errArgs := BuildSetArgs(key, value, opts); errArgs != nil {
		return false, errArgs
	}
	key = strings.TrimSpace(key)

	var ttl time.Duration
	if opts.EX > 0 {
		var errTTL error
		ttl, errTTL = ceilDurationTTL(opts.EX, time.Second)
		if errTTL != nil {
			return false, errTTL
		}
	} else if opts.PX > 0 {
		var errTTL error
		ttl, errTTL = ceilDurationTTL(opts.PX, time.Millisecond)
		if errTTL != nil {
			return false, errTTL
		}
	}

	keyBytes := []byte(key)
	var written bool
	errUpdate := s.update(func(tx *bbolt.Tx, now time.Time) error {
		nowNano := now.UnixNano()
		expiresAt, errExp := computeExpiresAtUnixNano(now, ttl)
		if errExp != nil {
			return errExp
		}
		b := tx.Bucket(boltKVBucket)
		if b == nil {
			return fmt.Errorf("kvstore: bucket not found")
		}
		var exists bool
		if raw := b.Get(keyBytes); raw != nil {
			_, _, expired, errDecode := decodeEnvelope(raw, nowNano)
			if errDecode != nil {
				return errDecode
			}
			if expired {
				if errDel := b.Delete(keyBytes); errDel != nil {
					return errDel
				}
				exists = false
			} else {
				exists = true
			}
		}
		if opts.NX && exists {
			return nil
		}
		if opts.XX && !exists {
			return nil
		}
		if errPut := b.Put(keyBytes, encodeEnvelope(value, expiresAt)); errPut != nil {
			return errPut
		}
		written = true
		return nil
	})
	if errUpdate != nil {
		return false, errUpdate
	}
	return written, nil
}

// KVSetNX stores value only when key does not currently exist (or has expired).
func (s *BoltStore) KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if errCtx := checkContext(ctx); errCtx != nil {
		return false, errCtx
	}
	return s.KVSet(ctx, key, value, KVSetOptions{NX: true, EX: ttl})
}

// KVCompareAndSwap atomically updates key to value if its current state matches expectedExists and expected.
func (s *BoltStore) KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error) {
	if errCtx := checkContext(ctx); errCtx != nil {
		return false, errCtx
	}
	if s == nil || s.db == nil {
		return false, errStoreClosed
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false, fmt.Errorf("home kv: key is empty")
	}
	if ttl < 0 {
		return false, fmt.Errorf("home kv: ttl must not be negative")
	}

	var roundedTTL time.Duration
	if ttl > 0 {
		var errTTL error
		roundedTTL, errTTL = ceilDurationTTL(ttl, time.Millisecond)
		if errTTL != nil {
			return false, errTTL
		}
	}

	keyBytes := []byte(key)
	var swapped bool
	errUpdate := s.update(func(tx *bbolt.Tx, now time.Time) error {
		nowNano := now.UnixNano()
		expiresAt, errExp := computeExpiresAtUnixNano(now, roundedTTL)
		if errExp != nil {
			return errExp
		}
		b := tx.Bucket(boltKVBucket)
		if b == nil {
			return fmt.Errorf("kvstore: bucket not found")
		}
		var currentExists bool
		var currentPayload []byte
		if raw := b.Get(keyBytes); raw != nil {
			payload, _, expired, errDecode := decodeEnvelope(raw, nowNano)
			if errDecode != nil {
				return errDecode
			}
			if expired {
				if errDel := b.Delete(keyBytes); errDel != nil {
					return errDel
				}
			} else {
				currentExists = true
				currentPayload = payload
			}
		}
		if currentExists != expectedExists || (expectedExists && !bytes.Equal(currentPayload, expected)) {
			return nil
		}
		if errPut := b.Put(keyBytes, encodeEnvelope(value, expiresAt)); errPut != nil {
			return errPut
		}
		swapped = true
		return nil
	})
	if errUpdate != nil {
		return false, errUpdate
	}
	return swapped, nil
}

// KVDel deletes one or more keys and returns the number of unexpired keys removed.
func (s *BoltStore) KVDel(ctx context.Context, keys ...string) (int64, error) {
	if errCtx := checkContext(ctx); errCtx != nil {
		return 0, errCtx
	}
	if s == nil || s.db == nil {
		return 0, errStoreClosed
	}
	if len(keys) == 0 {
		return 0, nil
	}
	var deleted int64
	errUpdate := s.update(func(tx *bbolt.Tx, now time.Time) error {
		nowNano := now.UnixNano()
		b := tx.Bucket(boltKVBucket)
		if b == nil {
			return nil
		}
		for _, rawKey := range keys {
			key := strings.TrimSpace(rawKey)
			if key == "" {
				continue
			}
			keyBytes := []byte(key)
			raw := b.Get(keyBytes)
			if raw == nil {
				continue
			}
			_, _, expired, errDecode := decodeEnvelope(raw, nowNano)
			if errDecode == nil && !expired {
				deleted++
			}
			if errDel := b.Delete(keyBytes); errDel != nil {
				return errDel
			}
		}
		return nil
	})
	if errUpdate != nil {
		return 0, errUpdate
	}
	return deleted, nil
}

// KVExpire updates the TTL of an existing unexpired key, or immediately deletes it if ttl <= 0.
func (s *BoltStore) KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if errCtx := checkContext(ctx); errCtx != nil {
		return false, errCtx
	}
	if s == nil || s.db == nil {
		return false, errStoreClosed
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false, fmt.Errorf("home kv: key is empty")
	}
	keyBytes := []byte(key)
	var updated bool
	errUpdate := s.update(func(tx *bbolt.Tx, now time.Time) error {
		nowNano := now.UnixNano()
		b := tx.Bucket(boltKVBucket)
		if b == nil {
			return nil
		}
		raw := b.Get(keyBytes)
		if raw == nil {
			return nil
		}
		payload, _, expired, errDecode := decodeEnvelope(raw, nowNano)
		if errDecode != nil {
			return errDecode
		}
		if expired {
			return b.Delete(keyBytes)
		}
		if ttl <= 0 {
			if errDel := b.Delete(keyBytes); errDel != nil {
				return errDel
			}
			updated = true
			return nil
		}
		expiresAt, errExp := computeExpiresAtUnixNano(now, ttl)
		if errExp != nil {
			return errExp
		}
		if errPut := b.Put(keyBytes, encodeEnvelope(payload, expiresAt)); errPut != nil {
			return errPut
		}
		updated = true
		return nil
	})
	if errUpdate != nil {
		return false, errUpdate
	}
	return updated, nil
}

// KVTTL returns the remaining time-to-live of key.
// If key does not exist or has expired, it returns (0, false, nil).
// If key exists and has no expiration, it returns (0, true, nil).
func (s *BoltStore) KVTTL(ctx context.Context, key string) (time.Duration, bool, error) {
	if errCtx := checkContext(ctx); errCtx != nil {
		return 0, false, errCtx
	}
	if s == nil || s.db == nil {
		return 0, false, errStoreClosed
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false, fmt.Errorf("home kv: key is empty")
	}
	var remaining time.Duration
	var found bool
	errView := s.view(func(tx *bbolt.Tx, now time.Time) error {
		nowNano := now.UnixNano()
		b := tx.Bucket(boltKVBucket)
		if b == nil {
			return nil
		}
		raw := b.Get([]byte(key))
		if raw == nil {
			return nil
		}
		_, expiresAt, expired, errDecode := decodeEnvelope(raw, nowNano)
		if errDecode != nil {
			return errDecode
		}
		if expired {
			return nil
		}
		found = true
		if expiresAt > 0 {
			remaining = time.Duration(expiresAt - nowNano)
		}
		return nil
	})
	if errView != nil {
		return 0, false, errView
	}
	return remaining, found, nil
}

// KVIncrBy atomically increments the base-10 integer value of key by delta.
func (s *BoltStore) KVIncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	if errCtx := checkContext(ctx); errCtx != nil {
		return 0, errCtx
	}
	if s == nil || s.db == nil {
		return 0, errStoreClosed
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, fmt.Errorf("home kv: key is empty")
	}
	keyBytes := []byte(key)
	var next int64
	errUpdate := s.update(func(tx *bbolt.Tx, now time.Time) error {
		nowNano := now.UnixNano()
		b := tx.Bucket(boltKVBucket)
		if b == nil {
			return fmt.Errorf("kvstore: bucket not found")
		}
		var current int64
		var expiresAt int64
		if raw := b.Get(keyBytes); raw != nil {
			payload, exp, expired, errDecode := decodeEnvelope(raw, nowNano)
			if errDecode != nil {
				return errDecode
			}
			if expired {
				if errDel := b.Delete(keyBytes); errDel != nil {
					return errDel
				}
			} else {
				parsed, errParse := strconv.ParseInt(string(payload), 10, 64)
				if errParse != nil {
					return fmt.Errorf("kvstore: value is not an integer or out of range: %w", errParse)
				}
				current = parsed
				expiresAt = exp
			}
		}
		if (delta > 0 && current > math.MaxInt64-delta) || (delta < 0 && current < math.MinInt64-delta) {
			return fmt.Errorf("kvstore: increment or decrement would overflow")
		}
		next = current + delta
		encoded := encodeEnvelope([]byte(strconv.FormatInt(next, 10)), expiresAt)
		return b.Put(keyBytes, encoded)
	})
	if errUpdate != nil {
		return 0, errUpdate
	}
	return next, nil
}

// KVMGet retrieves multiple keys in a single read transaction.
func (s *BoltStore) KVMGet(ctx context.Context, keys ...string) ([][]byte, []bool, error) {
	if errCtx := checkContext(ctx); errCtx != nil {
		return nil, nil, errCtx
	}
	if s == nil || s.db == nil {
		return nil, nil, errStoreClosed
	}
	if len(keys) == 0 {
		return nil, nil, nil
	}
	values := make([][]byte, len(keys))
	found := make([]bool, len(keys))
	errView := s.view(func(tx *bbolt.Tx, now time.Time) error {
		nowNano := now.UnixNano()
		b := tx.Bucket(boltKVBucket)
		if b == nil {
			return nil
		}
		for i, rawKey := range keys {
			key := strings.TrimSpace(rawKey)
			if key == "" {
				continue
			}
			raw := b.Get([]byte(key))
			if raw == nil {
				continue
			}
			payload, _, expired, errDecode := decodeEnvelope(raw, nowNano)
			if errDecode != nil {
				return errDecode
			}
			if expired {
				continue
			}
			val := bytes.Clone(payload)
			if val == nil {
				val = []byte{}
			}
			values[i] = val
			found[i] = true
		}
		return nil
	})
	if errView != nil {
		return nil, nil, errView
	}
	return values, found, nil
}

// KVMSet writes multiple non-expiring key-value pairs in a single write transaction.
func (s *BoltStore) KVMSet(ctx context.Context, pairs map[string][]byte) error {
	if errCtx := checkContext(ctx); errCtx != nil {
		return errCtx
	}
	if s == nil || s.db == nil {
		return errStoreClosed
	}
	if len(pairs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("home kv: key is empty")
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return s.update(func(tx *bbolt.Tx, _ time.Time) error {
		b := tx.Bucket(boltKVBucket)
		if b == nil {
			return fmt.Errorf("kvstore: bucket not found")
		}
		for _, rawKey := range keys {
			key := strings.TrimSpace(rawKey)
			if errPut := b.Put([]byte(key), encodeEnvelope(pairs[rawKey], 0)); errPut != nil {
				return errPut
			}
		}
		return nil
	})
}

func (s *BoltStore) isStopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *BoltStore) runSweeper() {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			if errSweep := s.sweepExpired(); errSweep != nil {
				log.Warnf("kvstore: background sweep failed: %v", errSweep)
			}
		}
	}
}

func (s *BoltStore) sweepExpired() error {
	if s == nil || s.db == nil {
		return errStoreClosed
	}
	batchSize := s.sweepBatchSize
	if batchSize <= 0 {
		batchSize = defaultSweepBatchSize
	}

	var lastKey []byte
	for !s.isStopped() {
		var expiredKeys [][]byte
		errView := s.view(func(tx *bbolt.Tx, now time.Time) error {
			nowNano := now.UnixNano()
			b := tx.Bucket(boltKVBucket)
			if b == nil {
				return nil
			}
			c := b.Cursor()
			var k, v []byte
			if lastKey == nil {
				k, v = c.First()
			} else {
				k, v = c.Seek(lastKey)
				if k != nil && bytes.Equal(k, lastKey) {
					k, v = c.Next()
				}
			}
			for ; k != nil; k, v = c.Next() {
				_, _, expired, errDecode := decodeEnvelope(v, nowNano)
				if errDecode == nil && expired {
					keyCopy := bytes.Clone(k)
					expiredKeys = append(expiredKeys, keyCopy)
					if len(expiredKeys) >= batchSize {
						lastKey = keyCopy
						break
					}
				}
			}
			return nil
		})
		if errView != nil {
			return errView
		}
		if len(expiredKeys) == 0 {
			return nil
		}

		if s.beforeSweepDelete != nil {
			s.beforeSweepDelete(expiredKeys)
		}

		errUpdate := s.update(func(tx *bbolt.Tx, now time.Time) error {
			deleteNowNano := now.UnixNano()
			b := tx.Bucket(boltKVBucket)
			if b == nil {
				return nil
			}
			for _, keyBytes := range expiredKeys {
				raw := b.Get(keyBytes)
				if raw == nil {
					continue
				}
				_, _, expired, errDecode := decodeEnvelope(raw, deleteNowNano)
				if errDecode == nil && expired {
					if errDel := b.Delete(keyBytes); errDel != nil {
						return errDel
					}
				}
			}
			return nil
		})
		if errUpdate != nil {
			return errUpdate
		}
		if len(expiredKeys) < batchSize {
			return nil
		}
	}
	return nil
}
