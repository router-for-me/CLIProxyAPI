package kvstore

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"go.etcd.io/bbolt"
)

func TestResolvePath_DefaultRelativeTildeAndAbsolute(t *testing.T) {
	cwd, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("os.Getwd() error = %v", errGetwd)
	}
	wantDefault := filepath.Clean(filepath.Join(cwd, config.DefaultKVStorePath))

	for _, raw := range []string{"", "   ", "\t\n"} {
		got, errResolve := ResolvePath(raw)
		if errResolve != nil {
			t.Fatalf("ResolvePath(%q) error = %v", raw, errResolve)
		}
		if got != wantDefault {
			t.Fatalf("ResolvePath(%q) = %q, want %q", raw, got, wantDefault)
		}
	}

	gotRel, errRel := ResolvePath(" ./data/nested/../custom.db ")
	if errRel != nil {
		t.Fatalf("ResolvePath(relative) error = %v", errRel)
	}
	wantRel := filepath.Clean(filepath.Join(cwd, "data", "custom.db"))
	if gotRel != wantRel {
		t.Fatalf("ResolvePath(relative) = %q, want %q", gotRel, wantRel)
	}

	homeDir, errHome := os.UserHomeDir()
	if errHome != nil {
		t.Fatalf("os.UserHomeDir() error = %v", errHome)
	}
	gotTilde, errTilde := ResolvePath("~/custom/kv.db")
	if errTilde != nil {
		t.Fatalf("ResolvePath(tilde) error = %v", errTilde)
	}
	wantTilde := filepath.Clean(filepath.Join(homeDir, "custom", "kv.db"))
	if gotTilde != wantTilde {
		t.Fatalf("ResolvePath(tilde) = %q, want %q", gotTilde, wantTilde)
	}

	gotHomeOnly, errHomeOnly := ResolvePath("~")
	if errHomeOnly != nil {
		t.Fatalf("ResolvePath(~) error = %v", errHomeOnly)
	}
	if gotHomeOnly != filepath.Clean(homeDir) {
		t.Fatalf("ResolvePath(~) = %q, want %q", gotHomeOnly, filepath.Clean(homeDir))
	}

	absInput := filepath.Join(t.TempDir(), "sub", "kv.db")
	gotAbs, errAbs := ResolvePath(absInput)
	if errAbs != nil {
		t.Fatalf("ResolvePath(abs) error = %v", errAbs)
	}
	if gotAbs != filepath.Clean(absInput) {
		t.Fatalf("ResolvePath(abs) = %q, want %q", gotAbs, filepath.Clean(absInput))
	}

	gotDefaultAuth, errDefaultAuth := ResolvePathWithAuthDir("", "")
	if errDefaultAuth != nil {
		t.Fatalf("ResolvePathWithAuthDir('', '') error = %v", errDefaultAuth)
	}
	wantDefaultAuth := filepath.Clean(filepath.Join(homeDir, ".cli-proxy-api", config.DefaultKVStorePath))
	if gotDefaultAuth != wantDefaultAuth {
		t.Fatalf("ResolvePathWithAuthDir('', '') = %q, want %q", gotDefaultAuth, wantDefaultAuth)
	}

	customAuthDir := filepath.Join(t.TempDir(), "auth-dir")
	gotCustomAuth, errCustomAuth := ResolvePathWithAuthDir("   ", customAuthDir)
	if errCustomAuth != nil {
		t.Fatalf("ResolvePathWithAuthDir(blank, customAuthDir) error = %v", errCustomAuth)
	}
	wantCustomAuth := filepath.Clean(filepath.Join(customAuthDir, config.DefaultKVStorePath))
	if gotCustomAuth != wantCustomAuth {
		t.Fatalf("ResolvePathWithAuthDir(blank, customAuthDir) = %q, want %q", gotCustomAuth, wantCustomAuth)
	}

	explicitOverride := filepath.Join(t.TempDir(), "explicit", "override.db")
	gotOverride, errOverride := ResolvePathWithAuthDir(explicitOverride, customAuthDir)
	if errOverride != nil {
		t.Fatalf("ResolvePathWithAuthDir(explicit, customAuthDir) error = %v", errOverride)
	}
	if gotOverride != filepath.Clean(explicitOverride) {
		t.Fatalf("ResolvePathWithAuthDir(explicit, customAuthDir) = %q, want %q", gotOverride, filepath.Clean(explicitOverride))
	}
}

func TestBuildSetArgsAndDurationCeil(t *testing.T) {
	if got := DurationCeil(0, time.Second); got != 0 {
		t.Fatalf("DurationCeil(0) = %d, want 0", got)
	}
	if got := DurationCeil(1001*time.Millisecond, time.Second); got != 2 {
		t.Fatalf("DurationCeil(1001ms, 1s) = %d, want 2", got)
	}
	if got := DurationCeil(time.Duration(math.MaxInt64), time.Second); got <= 0 {
		t.Fatalf("DurationCeil(MaxInt64, 1s) = %d, want positive int64 without overflow", got)
	}
	if got := DurationCeil(time.Duration(math.MaxInt64), 1); got != math.MaxInt64 {
		t.Fatalf("DurationCeil(MaxInt64, 1ns) = %d, want MaxInt64", got)
	}

	args, errArgs := BuildSetArgs("key", []byte("value"), KVSetOptions{EX: 2 * time.Second, NX: true})
	if errArgs != nil {
		t.Fatalf("BuildSetArgs(EX NX) error = %v", errArgs)
	}
	want := []any{"key", []byte("value"), "EX", int64(2), "NX"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("BuildSetArgs(EX NX) = %#v, want %#v", args, want)
	}

	args, errArgs = BuildSetArgs("key", []byte("value"), KVSetOptions{PX: 1500 * time.Millisecond, XX: true})
	if errArgs != nil {
		t.Fatalf("BuildSetArgs(PX XX) error = %v", errArgs)
	}
	want = []any{"key", []byte("value"), "PX", int64(1500), "XX"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("BuildSetArgs(PX XX) = %#v, want %#v", args, want)
	}

	if _, errEmpty := BuildSetArgs("   ", []byte("v"), KVSetOptions{}); errEmpty == nil || errEmpty.Error() != "home kv: key is empty" {
		t.Fatalf("BuildSetArgs(empty key) error = %v, want 'home kv: key is empty'", errEmpty)
	}
	if _, errExPx := BuildSetArgs("k", []byte("v"), KVSetOptions{EX: time.Second, PX: time.Millisecond}); errExPx == nil || errExPx.Error() != "home kv: EX and PX are mutually exclusive" {
		t.Fatalf("BuildSetArgs(EX PX) error = %v, want 'home kv: EX and PX are mutually exclusive'", errExPx)
	}
	if _, errNeg := BuildSetArgs("k", []byte("v"), KVSetOptions{EX: -time.Second}); errNeg == nil || errNeg.Error() != "home kv: ttl must not be negative" {
		t.Fatalf("BuildSetArgs(negative EX) error = %v, want 'home kv: ttl must not be negative'", errNeg)
	}
	if _, errNxXx := BuildSetArgs("k", []byte("v"), KVSetOptions{NX: true, XX: true}); errNxXx == nil || errNxXx.Error() != "home kv: NX and XX are mutually exclusive" {
		t.Fatalf("BuildSetArgs(NX XX) error = %v, want 'home kv: NX and XX are mutually exclusive'", errNxXx)
	}
	if ErrCompareAndSwapUnsupported == nil || ErrCompareAndSwapUnsupported.Error() != "kvstore: compare-and-swap command is not supported" {
		t.Fatalf("ErrCompareAndSwapUnsupported = %v, want 'kvstore: compare-and-swap command is not supported'", ErrCompareAndSwapUnsupported)
	}
}

func TestBoltStore_EnvelopeAndBasicOperations(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	var clockMu sync.RWMutex
	currentNow := func() time.Time {
		clockMu.RLock()
		defer clockMu.RUnlock()
		return now
	}

	dbPath := filepath.Join(t.TempDir(), "nested", "dir", "kv.db")
	store, errOpen := OpenBoltStore(dbPath, WithNowFunc(currentNow), WithSweepInterval(0))
	if errOpen != nil {
		t.Fatalf("OpenBoltStore() error = %v", errOpen)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	if store.Path() != filepath.Clean(dbPath) {
		t.Fatalf("store.Path() = %q, want %q", store.Path(), filepath.Clean(dbPath))
	}

	ctx := context.Background()

	// Basic Set & Get
	written, errSet := store.KVSet(ctx, "cpa:test:k1", []byte("val1"), KVSetOptions{})
	if errSet != nil || !written {
		t.Fatalf("KVSet(k1) = %v, %v; want true, nil", written, errSet)
	}
	// Direct 9-byte binary envelope layout assertions (version == 0x01, big-endian expiresAtUnixNano, raw payload)
	if _, errSetTTL := store.KVSet(ctx, "cpa:test:env-ttl", []byte("payload-bytes"), KVSetOptions{EX: 3 * time.Second}); errSetTTL != nil {
		t.Fatalf("KVSet(env-ttl) error = %v", errSetTTL)
	}
	errInspectEnvelope := store.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(boltKVBucket)
		rawNoTTL := b.Get([]byte("cpa:test:k1"))
		if len(rawNoTTL) != envelopeHeaderSize+len("val1") {
			t.Fatalf("rawNoTTL len = %d, want %d", len(rawNoTTL), envelopeHeaderSize+len("val1"))
		}
		if rawNoTTL[0] != envelopeVersionV1 {
			t.Fatalf("rawNoTTL version = 0x%02x, want 0x%02x", rawNoTTL[0], envelopeVersionV1)
		}
		if exp := binary.BigEndian.Uint64(rawNoTTL[1:envelopeHeaderSize]); exp != 0 {
			t.Fatalf("rawNoTTL expiresAt = %d, want 0", exp)
		}
		if string(rawNoTTL[envelopeHeaderSize:]) != "val1" {
			t.Fatalf("rawNoTTL payload = %q, want val1", rawNoTTL[envelopeHeaderSize:])
		}

		rawTTL := b.Get([]byte("cpa:test:env-ttl"))
		wantExp := uint64(now.Add(3 * time.Second).UnixNano())
		if rawTTL[0] != envelopeVersionV1 {
			t.Fatalf("rawTTL version = 0x%02x, want 0x%02x", rawTTL[0], envelopeVersionV1)
		}
		if gotExp := binary.BigEndian.Uint64(rawTTL[1:envelopeHeaderSize]); gotExp != wantExp {
			t.Fatalf("rawTTL expiresAt = %d, want %d", gotExp, wantExp)
		}
		if string(rawTTL[envelopeHeaderSize:]) != "payload-bytes" {
			t.Fatalf("rawTTL payload = %q, want payload-bytes", rawTTL[envelopeHeaderSize:])
		}
		return nil
	})
	if errInspectEnvelope != nil {
		t.Fatalf("inspect binary envelope error = %v", errInspectEnvelope)
	}

	// Explicit KVSetNX and KVCompareAndSwap match/mismatch branches on unexpired keys
	nxOK, errSetNX1 := store.KVSetNX(ctx, "cpa:test:setnx", []byte("first"), 10*time.Second)
	if errSetNX1 != nil || !nxOK {
		t.Fatalf("KVSetNX(first) = %v, %v; want true, nil", nxOK, errSetNX1)
	}
	nxDup, errSetNX2 := store.KVSetNX(ctx, "cpa:test:setnx", []byte("second"), 10*time.Second)
	if errSetNX2 != nil || nxDup {
		t.Fatalf("KVSetNX(duplicate) = %v, %v; want false, nil", nxDup, errSetNX2)
	}
	if casMismatchVal, errCASMismatchVal := store.KVCompareAndSwap(ctx, "cpa:test:setnx", []byte("wrong"), true, []byte("updated"), 10*time.Second); errCASMismatchVal != nil || casMismatchVal {
		t.Fatalf("KVCompareAndSwap(value mismatch) = %v, %v; want false, nil", casMismatchVal, errCASMismatchVal)
	}
	if casMismatchExist, errCASMismatchExist := store.KVCompareAndSwap(ctx, "cpa:test:setnx", nil, false, []byte("updated"), 10*time.Second); errCASMismatchExist != nil || casMismatchExist {
		t.Fatalf("KVCompareAndSwap(exists mismatch) = %v, %v; want false, nil", casMismatchExist, errCASMismatchExist)
	}
	if casMatch, errCASMatch := store.KVCompareAndSwap(ctx, "cpa:test:setnx", []byte("first"), true, []byte("updated"), 10*time.Second); errCASMatch != nil || !casMatch {
		t.Fatalf("KVCompareAndSwap(match) = %v, %v; want true, nil", casMatch, errCASMatch)
	}
	if gotCAS, foundCAS, errGetCAS := store.KVGet(ctx, "cpa:test:setnx"); errGetCAS != nil || !foundCAS || string(gotCAS) != "updated" {
		t.Fatalf("KVGet(setnx) after CAS = %q, %v, %v; want updated, true, nil", gotCAS, foundCAS, errGetCAS)
	}
	got, found, errGet := store.KVGet(ctx, "cpa:test:k1")
	if errGet != nil || !found || string(got) != "val1" {
		t.Fatalf("KVGet(k1) = %q, %v, %v; want val1, true, nil", got, found, errGet)
	}
	// Mutating returned slice must not mutate stored value
	got[0] = 'X'
	gotAgain, foundAgain, errGetAgain := store.KVGet(ctx, "cpa:test:k1")
	if errGetAgain != nil || !foundAgain || string(gotAgain) != "val1" {
		t.Fatalf("KVGet(k1) after caller slice mutation = %q, %v, %v; want val1, true, nil", gotAgain, foundAgain, errGetAgain)
	}

	// Missing key
	missVal, missFound, errMiss := store.KVGet(ctx, "cpa:test:missing")
	if errMiss != nil || missFound || missVal != nil {
		t.Fatalf("KVGet(missing) = %v, %v, %v; want nil, false, nil", missVal, missFound, errMiss)
	}

	// KVMSet & KVMGet
	if errMSet := store.KVMSet(ctx, map[string][]byte{
		"cpa:test:m1": []byte("mv1"),
		"cpa:test:m2": []byte("mv2"),
	}); errMSet != nil {
		t.Fatalf("KVMSet() error = %v", errMSet)
	}
	mvals, mfound, errMGet := store.KVMGet(ctx, "cpa:test:m1", "cpa:test:missing", "cpa:test:m2")
	if errMGet != nil {
		t.Fatalf("KVMGet() error = %v", errMGet)
	}
	if len(mvals) != 3 || len(mfound) != 3 {
		t.Fatalf("KVMGet() lengths = %d, %d; want 3, 3", len(mvals), len(mfound))
	}
	if !mfound[0] || string(mvals[0]) != "mv1" || mfound[1] || mvals[1] != nil || !mfound[2] || string(mvals[2]) != "mv2" {
		t.Fatalf("KVMGet() = %q / %v; unexpected values", mvals, mfound)
	}

	// KVDel
	deleted, errDel := store.KVDel(ctx, "cpa:test:m1", "cpa:test:m1", "cpa:test:missing")
	if errDel != nil || deleted != 1 {
		t.Fatalf("KVDel() = %d, %v; want 1, nil", deleted, errDel)
	}
	_, foundAfterDel, errAfterDel := store.KVGet(ctx, "cpa:test:m1")
	if errAfterDel != nil || foundAfterDel {
		t.Fatalf("KVGet(m1) after KVDel = %v, %v; want false, nil", foundAfterDel, errAfterDel)
	}

	// KVIncrBy & overflow defense
	valIncr, errIncr := store.KVIncrBy(ctx, "cpa:test:counter", 5)
	if errIncr != nil || valIncr != 5 {
		t.Fatalf("KVIncrBy(new, 5) = %d, %v; want 5, nil", valIncr, errIncr)
	}
	valIncr, errIncr = store.KVIncrBy(ctx, "cpa:test:counter", -2)
	if errIncr != nil || valIncr != 3 {
		t.Fatalf("KVIncrBy(existing, -2) = %d, %v; want 3, nil", valIncr, errIncr)
	}
	if _, errNonInt := store.KVIncrBy(ctx, "cpa:test:k1", 1); errNonInt == nil {
		t.Fatal("KVIncrBy(non-integer) error = nil, want error")
	}
	if _, errMaxSet := store.KVSet(ctx, "cpa:test:max", []byte("9223372036854775807"), KVSetOptions{}); errMaxSet != nil {
		t.Fatalf("KVSet(max int64) error = %v", errMaxSet)
	}
	if _, errOverflow := store.KVIncrBy(ctx, "cpa:test:max", 1); errOverflow == nil {
		t.Fatal("KVIncrBy(positive overflow) error = nil, want overflow error")
	}
	if _, errMinSet := store.KVSet(ctx, "cpa:test:min", []byte("-9223372036854775808"), KVSetOptions{}); errMinSet != nil {
		t.Fatalf("KVSet(min int64) error = %v", errMinSet)
	}
	if _, errUnderflow := store.KVIncrBy(ctx, "cpa:test:min", -1); errUnderflow == nil || math.MinInt64 == 0 {
		t.Fatal("KVIncrBy(negative overflow) error = nil, want overflow error")
	}

	// Corrupted envelope defense: length < 9 and unsupported version != 0x01
	errCorruptWrite := store.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(boltKVBucket)
		if errPut := b.Put([]byte("cpa:test:short"), []byte{0x01, 0x02, 0x03}); errPut != nil {
			return errPut
		}
		badVer := make([]byte, envelopeHeaderSize+3)
		badVer[0] = 0x02
		copy(badVer[envelopeHeaderSize:], "abc")
		return b.Put([]byte("cpa:test:badver"), badVer)
	})
	if errCorruptWrite != nil {
		t.Fatalf("inject corrupted entries error = %v", errCorruptWrite)
	}

	if _, _, errShort := store.KVGet(ctx, "cpa:test:short"); errShort == nil || !strings.Contains(errShort.Error(), "corrupted entry") {
		t.Fatalf("KVGet(short envelope) error = %v, want corrupted entry error", errShort)
	}
	if _, _, errBadVer := store.KVGet(ctx, "cpa:test:badver"); errBadVer == nil || !strings.Contains(errBadVer.Error(), "unsupported envelope version") {
		t.Fatalf("KVGet(bad version envelope) error = %v, want unsupported envelope version error", errBadVer)
	}
	if _, _, errTTLShort := store.KVTTL(ctx, "cpa:test:short"); errTTLShort == nil {
		t.Fatal("KVTTL(short envelope) error = nil, want error")
	}
	if _, errIncrShort := store.KVIncrBy(ctx, "cpa:test:short", 1); errIncrShort == nil {
		t.Fatal("KVIncrBy(short envelope) error = nil, want error")
	}

	// Context cancellation check across all KV methods
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, errKVGet := store.KVGet(canceledCtx, "cpa:test:k1"); !errors.Is(errKVGet, context.Canceled) {
		t.Fatalf("KVGet(canceledCtx) error = %v, want context.Canceled", errKVGet)
	}
	if _, errKVSet := store.KVSet(canceledCtx, "cpa:test:k1", []byte("v"), KVSetOptions{}); !errors.Is(errKVSet, context.Canceled) {
		t.Fatalf("KVSet(canceledCtx) error = %v, want context.Canceled", errKVSet)
	}
	if _, errKVSetNX := store.KVSetNX(canceledCtx, "cpa:test:k1", []byte("v"), time.Second); !errors.Is(errKVSetNX, context.Canceled) {
		t.Fatalf("KVSetNX(canceledCtx) error = %v, want context.Canceled", errKVSetNX)
	}
	if _, errKVCAS := store.KVCompareAndSwap(canceledCtx, "cpa:test:k1", nil, false, []byte("v"), 0); !errors.Is(errKVCAS, context.Canceled) {
		t.Fatalf("KVCompareAndSwap(canceledCtx) error = %v, want context.Canceled", errKVCAS)
	}
	if _, errKVDel := store.KVDel(canceledCtx, "cpa:test:k1"); !errors.Is(errKVDel, context.Canceled) {
		t.Fatalf("KVDel(canceledCtx) error = %v, want context.Canceled", errKVDel)
	}
	if _, errKVExpire := store.KVExpire(canceledCtx, "cpa:test:k1", time.Second); !errors.Is(errKVExpire, context.Canceled) {
		t.Fatalf("KVExpire(canceledCtx) error = %v, want context.Canceled", errKVExpire)
	}
	if _, _, errKVTTL := store.KVTTL(canceledCtx, "cpa:test:k1"); !errors.Is(errKVTTL, context.Canceled) {
		t.Fatalf("KVTTL(canceledCtx) error = %v, want context.Canceled", errKVTTL)
	}
	if _, errKVIncrBy := store.KVIncrBy(canceledCtx, "cpa:test:k1", 1); !errors.Is(errKVIncrBy, context.Canceled) {
		t.Fatalf("KVIncrBy(canceledCtx) error = %v, want context.Canceled", errKVIncrBy)
	}
	if _, _, errKVMGet := store.KVMGet(canceledCtx, "cpa:test:k1"); !errors.Is(errKVMGet, context.Canceled) {
		t.Fatalf("KVMGet(canceledCtx) error = %v, want context.Canceled", errKVMGet)
	}
	if errKVMSet := store.KVMSet(canceledCtx, map[string][]byte{"cpa:test:k1": []byte("v")}); !errors.Is(errKVMSet, context.Canceled) {
		t.Fatalf("KVMSet(canceledCtx) error = %v, want context.Canceled", errKVMSet)
	}

	// Idempotent Close
	if errClose1 := store.Close(); errClose1 != nil {
		t.Fatalf("first store.Close() error = %v", errClose1)
	}
	if errClose2 := store.Close(); errClose2 != nil {
		t.Fatalf("second store.Close() error = %v", errClose2)
	}
}

func TestBoltStore_TTLLazyExpiryAndWriteTimeOverwrite(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	var clockMu sync.RWMutex
	currentNow := func() time.Time {
		clockMu.RLock()
		defer clockMu.RUnlock()
		return now
	}
	advanceClock := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	dbPath := filepath.Join(t.TempDir(), "ttl.db")
	store, errOpen := OpenBoltStore(dbPath, WithNowFunc(currentNow), WithSweepInterval(0))
	if errOpen != nil {
		t.Fatalf("OpenBoltStore() error = %v", errOpen)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	ctx := context.Background()

	rawExists := func(key string) bool {
		var exists bool
		_ = store.db.View(func(tx *bbolt.Tx) error {
			b := tx.Bucket(boltKVBucket)
			if b != nil && b.Get([]byte(key)) != nil {
				exists = true
			}
			return nil
		})
		return exists
	}

	// 1. EX and PX expiry on KVGet & KVTTL
	if _, errKVSet := store.KVSet(ctx, "ex-key", []byte("ex-val"), KVSetOptions{EX: 10 * time.Second}); errKVSet != nil {
		t.Fatalf("KVSet(ex-key) error = %v", errKVSet)
	}
	if _, errKVSet := store.KVSet(ctx, "px-key", []byte("px-val"), KVSetOptions{PX: 1500 * time.Millisecond}); errKVSet != nil {
		t.Fatalf("KVSet(px-key) error = %v", errKVSet)
	}
	if _, errKVSet := store.KVSet(ctx, "persist-key", []byte("p-val"), KVSetOptions{}); errKVSet != nil {
		t.Fatalf("KVSet(persist-key) error = %v", errKVSet)
	}

	ttlPersist, okPersist, errTTLPersist := store.KVTTL(ctx, "persist-key")
	if errTTLPersist != nil || !okPersist || ttlPersist != 0 {
		t.Fatalf("KVTTL(persist-key) = %v, %v, %v; want 0, true, nil", ttlPersist, okPersist, errTTLPersist)
	}

	advanceClock(1 * time.Second)
	ttlPX, okPX, errTTLPX := store.KVTTL(ctx, "px-key")
	if errTTLPX != nil || !okPX || ttlPX != 500*time.Millisecond {
		t.Fatalf("KVTTL(px-key) after 1s = %v, %v, %v; want 500ms, true, nil", ttlPX, okPX, errTTLPX)
	}

	advanceClock(500 * time.Millisecond)
	if val, found, errKVGet := store.KVGet(ctx, "px-key"); errKVGet != nil || found || val != nil {
		t.Fatalf("KVGet(px-key) at expiry = %v, %v, %v; want nil, false, nil", val, found, errKVGet)
	}
	if ttl, found, errKVTTL := store.KVTTL(ctx, "px-key"); errKVTTL != nil || found || ttl != 0 {
		t.Fatalf("KVTTL(px-key) at expiry = %v, %v, %v; want 0, false, nil", ttl, found, errKVTTL)
	}

	advanceClock(9 * time.Second) // total 10.5s
	if val, found, errKVGet := store.KVGet(ctx, "ex-key"); errKVGet != nil || found || val != nil {
		t.Fatalf("KVGet(ex-key) after expiry = %v, %v, %v; want nil, false, nil", val, found, errKVGet)
	}
	if ttl, found, errKVTTL := store.KVTTL(ctx, "ex-key"); errKVTTL != nil || found || ttl != 0 {
		t.Fatalf("KVTTL(ex-key) after expiry = %v, %v, %v; want 0, false, nil", ttl, found, errKVTTL)
	}

	// 2. Expired key physically exists -> KVSet(..., NX: true) returns true and writes new value
	if !rawExists("ex-key") {
		t.Fatal("expected expired ex-key to still physically exist before write")
	}
	nxWritten, errNX := store.KVSet(ctx, "ex-key", []byte("nx-new"), KVSetOptions{NX: true, EX: 5 * time.Second})
	if errNX != nil || !nxWritten {
		t.Fatalf("KVSet(expired, NX: true) = %v, %v; want true, nil", nxWritten, errNX)
	}
	if got, found, errKVGet := store.KVGet(ctx, "ex-key"); errKVGet != nil || !found || string(got) != "nx-new" {
		t.Fatalf("KVGet(ex-key) after NX = %q, %v, %v; want nx-new, true, nil", got, found, errKVGet)
	}

	// 3. Expired key physically exists -> KVSet(..., XX: true) returns false and deletes physical record
	if !rawExists("px-key") {
		t.Fatal("expected expired px-key to still physically exist before XX write")
	}
	xxWritten, errXX := store.KVSet(ctx, "px-key", []byte("xx-new"), KVSetOptions{XX: true})
	if errXX != nil || xxWritten {
		t.Fatalf("KVSet(expired, XX: true) = %v, %v; want false, nil", xxWritten, errXX)
	}
	if rawExists("px-key") {
		t.Fatal("expected KVSet(expired, XX: true) to delete physical expired record")
	}

	// 4. Expired key physically exists -> KVCompareAndSwap(..., expectedExists: false) returns true
	advanceClock(6 * time.Second) // ex-key (5s TTL) is now expired
	if !rawExists("ex-key") {
		t.Fatal("expected ex-key to physically exist before CAS")
	}
	casSwapped, errCAS := store.KVCompareAndSwap(ctx, "ex-key", nil, false, []byte("cas-val"), 4*time.Second)
	if errCAS != nil || !casSwapped {
		t.Fatalf("KVCompareAndSwap(expired, expectedExists=false) = %v, %v; want true, nil", casSwapped, errCAS)
	}
	if got, found, errKVGet := store.KVGet(ctx, "ex-key"); errKVGet != nil || !found || string(got) != "cas-val" {
		t.Fatalf("KVGet(ex-key) after CAS = %q, %v, %v; want cas-val, true, nil", got, found, errKVGet)
	}

	// 5. Expired key physically exists -> KVIncrBy starts from 0 and sets expiresAt == 0
	if _, errKVSet := store.KVSet(ctx, "incr-ttl", []byte("100"), KVSetOptions{EX: 3 * time.Second}); errKVSet != nil {
		t.Fatalf("KVSet(incr-ttl) error = %v", errKVSet)
	}
	advanceClock(4 * time.Second) // incr-ttl is now expired
	nextVal, errIncr := store.KVIncrBy(ctx, "incr-ttl", 7)
	if errIncr != nil || nextVal != 7 {
		t.Fatalf("KVIncrBy(expired, 7) = %d, %v; want 7, nil", nextVal, errIncr)
	}
	ttlIncr, foundIncr, errTTLIncr := store.KVTTL(ctx, "incr-ttl")
	if errTTLIncr != nil || !foundIncr || ttlIncr != 0 {
		t.Fatalf("KVTTL(incr-ttl) after expired KVIncrBy = %v, %v, %v; want 0 (no expiry), true, nil", ttlIncr, foundIncr, errTTLIncr)
	}

	// 6. KVExpire semantics:
	// - unexpired key -> returns true and extends TTL
	// - expired/non-existent key -> returns false (and deletes physical expired record)
	// - ttl <= 0 -> immediately deletes key and returns true
	if _, errKVSet := store.KVSet(ctx, "exp-target", []byte("v"), KVSetOptions{EX: 5 * time.Second}); errKVSet != nil {
		t.Fatalf("KVSet(exp-target) error = %v", errKVSet)
	}
	if ok, errKVExpire := store.KVExpire(ctx, "exp-target", 20*time.Second); errKVExpire != nil || !ok {
		t.Fatalf("KVExpire(unexpired, 20s) = %v, %v; want true, nil", ok, errKVExpire)
	}
	if ttl, found, errKVTTL := store.KVTTL(ctx, "exp-target"); errKVTTL != nil || !found || ttl != 20*time.Second {
		t.Fatalf("KVTTL(exp-target) = %v, %v, %v; want 20s, true, nil", ttl, found, errKVTTL)
	}

	if _, errKVSet := store.KVSet(ctx, "exp-stale", []byte("v"), KVSetOptions{EX: 2 * time.Second}); errKVSet != nil {
		t.Fatalf("KVSet(exp-stale) error = %v", errKVSet)
	}
	advanceClock(3 * time.Second)
	if ok, errKVExpire := store.KVExpire(ctx, "exp-stale", 10*time.Second); errKVExpire != nil || ok {
		t.Fatalf("KVExpire(expired) = %v, %v; want false, nil", ok, errKVExpire)
	}
	if rawExists("exp-stale") {
		t.Fatal("expected KVExpire(expired) to delete physical expired record")
	}
	if ok, errKVExpire := store.KVExpire(ctx, "non-existent", 10*time.Second); errKVExpire != nil || ok {
		t.Fatalf("KVExpire(non-existent) = %v, %v; want false, nil", ok, errKVExpire)
	}

	if ok, errKVExpire := store.KVExpire(ctx, "exp-target", 0); errKVExpire != nil || !ok {
		t.Fatalf("KVExpire(ttl=0) = %v, %v; want true, nil", ok, errKVExpire)
	}
	if _, found, errKVGet := store.KVGet(ctx, "exp-target"); errKVGet != nil || found {
		t.Fatalf("KVGet(exp-target) after KVExpire(0) = %v, %v; want false, nil", found, errKVGet)
	}
	if rawExists("exp-target") {
		t.Fatal("expected KVExpire(ttl<=0) to delete physical record")
	}
}

func TestBoltStore_BackgroundSweeperPreservesConcurrentlyRefreshedKeys(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	var clockMu sync.RWMutex
	currentNow := func() time.Time {
		clockMu.RLock()
		defer clockMu.RUnlock()
		return now
	}
	advanceClock := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	dbPath := filepath.Join(t.TempDir(), "sweeper.db")
	store, errOpen := OpenBoltStore(
		dbPath,
		WithNowFunc(currentNow),
		WithSweepBatchSize(2),
		WithSweepInterval(0),
	)
	if errOpen != nil {
		t.Fatalf("OpenBoltStore() error = %v", errOpen)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	ctx := context.Background()

	// Write 4 short-TTL keys and 2 long/persistent keys
	for _, k := range []string{"k1", "k2", "k3", "k4"} {
		if _, errKVSet := store.KVSet(ctx, k, []byte("val-"+k), KVSetOptions{EX: 5 * time.Second}); errKVSet != nil {
			t.Fatalf("KVSet(%s) error = %v", k, errKVSet)
		}
	}
	if _, errKVSet := store.KVSet(ctx, "k5-valid", []byte("val-k5"), KVSetOptions{EX: 60 * time.Second}); errKVSet != nil {
		t.Fatalf("KVSet(k5-valid) error = %v", errKVSet)
	}
	if _, errKVSet := store.KVSet(ctx, "k6-persist", []byte("val-k6"), KVSetOptions{}); errKVSet != nil {
		t.Fatalf("KVSet(k6-persist) error = %v", errKVSet)
	}

	// Advance clock by 10s so k1..k4 are expired, while k5-valid and k6-persist remain valid
	advanceClock(10 * time.Second)

	// Simulate concurrent refresh of k2 between scan (db.View) and delete (db.Update)
	var refreshedOnce sync.Once
	var batchesScanned int
	store.beforeSweepDelete = func(batch [][]byte) {
		batchesScanned++
		for _, keyBytes := range batch {
			if bytes.Equal(keyBytes, []byte("k2")) {
				refreshedOnce.Do(func() {
					_, _ = store.KVSet(ctx, "k2", []byte("val-k2-refreshed"), KVSetOptions{EX: 30 * time.Second})
				})
			}
		}
	}

	if errSweep := store.sweepExpired(); errSweep != nil {
		t.Fatalf("sweepExpired() error = %v", errSweep)
	}
	if batchesScanned < 2 {
		t.Fatalf("batchesScanned = %d, want >= 2 (multi-batch sweep with batchSize=2)", batchesScanned)
	}

	// Verify k1, k3, k4 are physically deleted from bbolt bucket
	errCheck := store.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(boltKVBucket)
		for _, deletedKey := range []string{"k1", "k3", "k4"} {
			if b.Get([]byte(deletedKey)) != nil {
				t.Errorf("expected expired key %q to be physically deleted by sweeper", deletedKey)
			}
		}
		for _, keptKey := range []string{"k2", "k5-valid", "k6-persist"} {
			if b.Get([]byte(keptKey)) == nil {
				t.Errorf("expected key %q to be preserved in bucket", keptKey)
			}
		}
		return nil
	})
	if errCheck != nil {
		t.Fatalf("db.View check error = %v", errCheck)
	}

	// Verify k2 has the refreshed value
	gotK2, foundK2, errK2 := store.KVGet(ctx, "k2")
	if errK2 != nil || !foundK2 || string(gotK2) != "val-k2-refreshed" {
		t.Fatalf("KVGet(k2) = %q, %v, %v; want val-k2-refreshed, true, nil", gotK2, foundK2, errK2)
	}
}

func TestBoltStore_InTransactionTimeSamplingAndTTLOverflowGuard(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	var clockMu sync.RWMutex
	var holdingWriteLock atomic.Bool
	var sampledWhileWriteLockHeld atomic.Bool

	currentNow := func() time.Time {
		if holdingWriteLock.Load() {
			sampledWhileWriteLockHeld.Store(true)
		}
		clockMu.RLock()
		defer clockMu.RUnlock()
		return now
	}
	advanceClock := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	dbPath := filepath.Join(t.TempDir(), "tx-time-and-overflow.db")
	store, errOpen := OpenBoltStore(dbPath, WithNowFunc(currentNow), WithSweepInterval(0))
	if errOpen != nil {
		t.Fatalf("OpenBoltStore() error = %v", errOpen)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	ctx := context.Background()

	// 1. Write a key with 5s TTL at t0.
	if _, errKVSet := store.KVSet(ctx, "contended-key", []byte("old-val"), KVSetOptions{EX: 5 * time.Second}); errKVSet != nil {
		t.Fatalf("KVSet(contended-key) error = %v", errKVSet)
	}

	// 2. Hold db.Update lock in tx1 and invoke KVSetNX.
	// When s.update invokes store.runUpdate(txFn), all code in KVSetNX, KVSet, and s.update
	// outside the transaction callback txFn has already completed while tx1 holds the lock at +0s,
	// and txFn (which samples s.now()) has not started yet.
	txLocked := make(chan struct{})
	releaseTx := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- store.db.Update(func(tx *bbolt.Tx) error {
			holdingWriteLock.Store(true)
			close(txLocked)
			<-releaseTx
			holdingWriteLock.Store(false)
			return nil
		})
	}()

	<-txLocked
	origRunUpdate := store.runUpdate
	store.runUpdate = func(txFn func(*bbolt.Tx) error) error {
		// At this exact point, KVSetNX has already invoked s.update -> runUpdate(txFn),
		// so any pre-transaction code has ALREADY executed while tx1 holds the lock and clock is +0s.
		// Now advance the clock by 6s and release tx1 before entering bbolt's db.Update(txFn).
		advanceClock(6 * time.Second)
		close(releaseTx)
		return origRunUpdate(txFn)
	}
	contenderWritten, contenderErr := store.KVSetNX(ctx, "contended-key", []byte("fresh"), 10*time.Second)
	store.runUpdate = origRunUpdate

	if errHolder := <-holderDone; errHolder != nil {
		t.Fatalf("holder db.Update error = %v", errHolder)
	}
	if sampledWhileWriteLockHeld.Load() {
		t.Fatal("nowFunc was sampled outside db.Update while another write transaction held the lock")
	}
	if contenderErr != nil || !contenderWritten {
		t.Fatalf("contender KVSetNX = %v, %v; want true, nil (sampled post-lock time)", contenderWritten, contenderErr)
	}
	if gotFresh, foundFresh, errGetFresh := store.KVGet(ctx, "contended-key"); errGetFresh != nil || !foundFresh || string(gotFresh) != "fresh" {
		t.Fatalf("KVGet(contended-key) = %q, %v, %v; want fresh, true, nil", gotFresh, foundFresh, errGetFresh)
	}
	if ttlAfter, foundAfter, errTTLAfter := store.KVTTL(ctx, "contended-key"); errTTLAfter != nil || !foundAfter || ttlAfter != 10*time.Second {
		t.Fatalf("KVTTL(contended-key) = %v, %v, %v; want 10s, true, nil", ttlAfter, foundAfter, errTTLAfter)
	}

	// 2b. Verify time.Unix(0, 0) (UnixNano == 0) is accepted when computing positive expiresAt.
	clockMu.Lock()
	savedNow := now
	now = time.Unix(0, 0).UTC()
	clockMu.Unlock()
	if writtenEpoch, errEpoch := store.KVSet(ctx, "epoch-key", []byte("epoch-val"), KVSetOptions{EX: 5 * time.Second}); errEpoch != nil || !writtenEpoch {
		t.Fatalf("KVSet(at Unix(0,0)) = %v, %v; want true, nil", writtenEpoch, errEpoch)
	}
	if ttlEpoch, foundEpoch, errTTLEpoch := store.KVTTL(ctx, "epoch-key"); errTTLEpoch != nil || !foundEpoch || ttlEpoch != 5*time.Second {
		t.Fatalf("KVTTL(epoch-key at Unix(0,0)) = %v, %v, %v; want 5s, true, nil", ttlEpoch, foundEpoch, errTTLEpoch)
	}
	clockMu.Lock()
	now = savedNow
	clockMu.Unlock()

	// 3. TTL arithmetic overflow guard: huge TTLs must return an error and never become permanent (expiresAt <= 0).
	if _, errOverflowEX := store.KVSet(ctx, "overflow-ex", []byte("v"), KVSetOptions{EX: time.Duration(math.MaxInt64)}); errOverflowEX == nil {
		t.Fatal("KVSet(EX=MaxInt64) error = nil, want overflow error")
	}
	maxWholeSecondsTTL := time.Duration((math.MaxInt64 / int64(time.Second)) * int64(time.Second))
	if _, errOverflowAddEX := store.KVSet(ctx, "overflow-add-ex", []byte("v"), KVSetOptions{EX: maxWholeSecondsTTL}); errOverflowAddEX == nil {
		t.Fatal("KVSet(now+EX > MaxInt64) error = nil, want overflow error")
	}
	if _, errOverflowPX := store.KVSet(ctx, "overflow-px", []byte("v"), KVSetOptions{PX: time.Duration(math.MaxInt64)}); errOverflowPX == nil {
		t.Fatal("KVSet(PX=MaxInt64) error = nil, want overflow error")
	}
	if _, errOverflowCAS := store.KVCompareAndSwap(ctx, "overflow-cas", nil, false, []byte("v"), time.Duration(math.MaxInt64)); errOverflowCAS == nil {
		t.Fatal("KVCompareAndSwap(ttl=MaxInt64) error = nil, want overflow error")
	}
	if _, errOverflowAddCAS := store.KVCompareAndSwap(ctx, "overflow-add-cas", nil, false, []byte("v"), maxWholeSecondsTTL); errOverflowAddCAS == nil {
		t.Fatal("KVCompareAndSwap(now+ttl > MaxInt64) error = nil, want overflow error")
	}
	if _, errKVSet := store.KVSet(ctx, "overflow-exp", []byte("v"), KVSetOptions{EX: 10 * time.Second}); errKVSet != nil {
		t.Fatalf("KVSet(overflow-exp) error = %v", errKVSet)
	}
	if _, errOverflowExpire := store.KVExpire(ctx, "overflow-exp", time.Duration(math.MaxInt64)); errOverflowExpire == nil {
		t.Fatal("KVExpire(ttl=MaxInt64) error = nil, want overflow error")
	}
}
