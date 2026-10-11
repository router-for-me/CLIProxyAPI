package claudemaster

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"golang.org/x/sys/unix"
)

// sharedClaudeExecutor is the Claude executor for a profile several claude-master processes use at
// once (see profile_shared.go). Rotating a login invalidates the old access token immediately, so
// each process must pick up another's rotation before its next request, not at its own next refresh.
type sharedClaudeExecutor struct {
	*runtimeexecutor.ClaudeExecutor
	stores map[string]*backendStore                                      // runtime auth ID -> its credential file
	rotate func(context.Context, *coreauth.Auth) (*coreauth.Auth, error) // tests stand in for Anthropic
}

func newSharedClaudeExecutor(inner *runtimeexecutor.ClaudeExecutor, stores []*backendStore) *sharedClaudeExecutor {
	byID := make(map[string]*backendStore, len(stores))
	for _, store := range stores {
		byID[store.runtimeID] = store
	}
	return &sharedClaudeExecutor{ClaudeExecutor: inner, stores: byID}
}

func (e *sharedClaudeExecutor) storeFor(auth *coreauth.Auth) *backendStore {
	if auth == nil {
		return nil
	}
	return e.stores[auth.ID]
}

// current returns auth carrying the newest saved credential: one stat per request, a read only when
// the file changed.
func (e *sharedClaudeExecutor) current(auth *coreauth.Auth) *coreauth.Auth {
	store := e.storeFor(auth)
	if store == nil {
		return auth
	}
	disk, err := store.diskSnapshot()
	if err != nil || !diskAhead(disk, auth.Metadata) {
		return auth
	}
	return adoptDiskTokens(auth, disk)
}

// Refresh rotates the login under the profile's cross-process lock. If another process rotated
// first, its credential is adopted and Anthropic is not called.
func (e *sharedClaudeExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	store := e.storeFor(auth)
	if store == nil {
		return e.ClaudeExecutor.Refresh(ctx, auth)
	}
	unlock, err := lockRefresh(ctx, store.opts.AuthDir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if disk, errDisk := store.diskSnapshot(); errDisk == nil && diskAhead(disk, auth.Metadata) {
		observeRefresh(store.name(), "adopted")
		lg().Info("login refresh adopted from another claude-master process", "profile", store.name())
		return adoptDiskTokens(auth, disk), nil
	}
	return e.rotateLocked(ctx, store, auth, "scheduled")
}

// rotateLocked refreshes at Anthropic and saves before releasing the lock, so the next process to
// look finds the new credential on disk.
func (e *sharedClaudeExecutor) rotateLocked(ctx context.Context, store *backendStore, auth *coreauth.Auth, why string) (*coreauth.Auth, error) {
	rotate := e.ClaudeExecutor.Refresh
	if e.rotate != nil {
		rotate = e.rotate
	}
	refreshed, err := rotate(ctx, auth.Clone())
	if err != nil {
		observeRefresh(store.name(), "failed")
		lg().Warn("login refresh failed", "profile", store.name(), "why", why, "reason", refreshFailureReason(err))
		return nil, err
	}
	// The executor stamps whole seconds; stamp this rotation precisely so two rotations (or a
	// rotation and a stale save) in the same second are still ordered. See diskAhead.
	if refreshed.Metadata != nil {
		refreshed.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if _, err := store.saveLocked(refreshed); err != nil {
		lg().Error("a refreshed login could not be saved", "profile", store.name())
		return nil, err
	}
	observeRefresh(store.name(), "rotated_"+why)
	lg().Info("login refreshed", "profile", store.name(), "why", why, "expires_in", expiresIn(refreshed.Metadata))
	return refreshed, nil
}

// refreshFailureReason says what kind of failure it was without the upstream's words.
var refreshStatus = regexp.MustCompile(`status (\d{3})`)

func refreshFailureReason(err error) string {
	text := err.Error()
	if m := refreshStatus.FindStringSubmatch(text); m != nil {
		return "http_" + m[1]
	}
	if strings.Contains(text, "deadline") || strings.Contains(text, "timeout") {
		return "timeout"
	}
	return "error"
}

// expiresIn is how long until a credential's access token expires, or "unknown".
func expiresIn(metadata map[string]any) string {
	if t, err := time.Parse(time.RFC3339, credentialString(metadata, "expired")); err == nil {
		return time.Until(t).Round(time.Second).String()
	}
	return "unknown"
}

const unauthorizedRotateCooldown = 30 * time.Second

// recoverUnauthorized handles a 401: another process may have rotated the login (adopt it, free), or
// this one's token really lapsed (rotate it, at most once per cooldown so a 401 that a new token
// cannot fix does not turn into a refresh storm).
func (e *sharedClaudeExecutor) recoverUnauthorized(ctx context.Context, rejected *coreauth.Auth) (*coreauth.Auth, bool) {
	store := e.storeFor(rejected)
	if store == nil {
		return nil, false
	}
	unlock, err := lockRefresh(ctx, store.opts.AuthDir)
	if err != nil {
		return nil, false
	}
	defer unlock()
	rejectedToken := credentialString(rejected.Metadata, "access_token")
	if disk, errDisk := store.diskSnapshot(); errDisk == nil {
		if saved := credentialString(disk, "access_token"); saved != "" && saved != rejectedToken {
			observeRefresh(store.name(), "adopted_after_401")
			lg().Info("adopted a newer login after a 401", "profile", store.name())
			return adoptDiskTokens(rejected, disk), true
		}
	}
	if !store.allowRotation(unauthorizedRotateCooldown) {
		lg().Debug("a 401 on a current login: not refreshing again so soon", "profile", store.name())
		return nil, false
	}
	lg().Warn("Anthropic rejected a login that looked current (401): refreshing it", "profile", store.name())
	refreshed, err := e.rotateLocked(ctx, store, rejected, "after_401")
	if err != nil {
		return nil, false
	}
	return refreshed, true
}

func isUnauthorized(err error) bool {
	var status interface{ StatusCode() int }
	return errors.As(err, &status) && status.StatusCode() == http.StatusUnauthorized
}

func (e *sharedClaudeExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	used := e.current(auth)
	resp, err := e.ClaudeExecutor.Execute(ctx, used, req, opts)
	if isUnauthorized(err) {
		if next, ok := e.recoverUnauthorized(ctx, used); ok {
			return e.ClaudeExecutor.Execute(ctx, next, req, opts)
		}
	}
	return resp, err
}

func (e *sharedClaudeExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	used := e.current(auth)
	result, err := e.ClaudeExecutor.ExecuteStream(ctx, used, req, opts)
	if isUnauthorized(err) {
		if next, ok := e.recoverUnauthorized(ctx, used); ok {
			return e.ClaudeExecutor.ExecuteStream(ctx, next, req, opts)
		}
	}
	return result, err
}

func (e *sharedClaudeExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	used := e.current(auth)
	resp, err := e.ClaudeExecutor.CountTokens(ctx, used, req, opts)
	if isUnauthorized(err) {
		if next, ok := e.recoverUnauthorized(ctx, used); ok {
			return e.ClaudeExecutor.CountTokens(ctx, next, req, opts)
		}
	}
	return resp, err
}

// HttpRequest carries the newest credential but is not retried: its body may be spent, and its
// callers (the usage poll) simply ask again at the next interval.
func (e *sharedClaudeExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return e.ClaudeExecutor.HttpRequest(ctx, e.current(auth), req)
}

func (e *sharedClaudeExecutor) PrepareRequestAuth(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return e.ClaudeExecutor.PrepareRequestAuth(ctx, e.current(auth))
}

// diskView caches the saved credential by (inode, mtime, size). Every save is an atomic rename, so
// a new inode appears with each rotation and a plain read never sees a partial file.
type diskView struct {
	mu       sync.Mutex
	ino      uint64
	mtime    time.Time
	size     int64
	snapshot map[string]any
	rotated  time.Time // last time THIS process called Anthropic to rotate (for the 401 cooldown)
}

func (s *backendStore) diskSnapshot() (map[string]any, error) {
	info, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	var ino uint64
	if stat, ok := info.Sys().(*unix.Stat_t); ok {
		ino = stat.Ino
	}
	s.view.mu.Lock()
	defer s.view.mu.Unlock()
	if s.view.snapshot != nil && s.view.ino == ino && s.view.size == info.Size() && s.view.mtime.Equal(info.ModTime()) {
		return s.view.snapshot, nil
	}
	snapshot, err := readRefreshSnapshot(s.path)
	if err != nil {
		return nil, err
	}
	s.view.ino, s.view.size, s.view.mtime, s.view.snapshot = ino, info.Size(), info.ModTime(), snapshot
	return snapshot, nil
}

// allowRotation limits this process's own 401-driven rotations.
func (s *backendStore) allowRotation(cooldown time.Duration) bool {
	s.view.mu.Lock()
	defer s.view.mu.Unlock()
	if !s.view.rotated.IsZero() && time.Since(s.view.rotated) < cooldown {
		return false
	}
	s.view.rotated = time.Now()
	return true
}

// ---- shared usage cache -------------------------------------------------------------------

// usageCacheTTL is shorter than the shortest jittered poll interval, so N processes polling the same
// account (two colors during a rollout, a new color's startup load) make about one request per
// interval between them rather than N.
const usageCacheTTL = 2 * time.Minute

// sharedUsageRequest answers the subscription usage poll from a small cache file beside the profile
// when another process fetched it in the last usageCacheTTL. Only the usage endpoint is cached; the
// body holds utilization and reset times, no credentials.
func sharedUsageRequest(next ClaudeQuotaRequestFunc, stores []*backendStore) ClaudeQuotaRequestFunc {
	if next == nil {
		return nil
	}
	byID := make(map[string]*backendStore, len(stores))
	for _, store := range stores {
		byID[store.runtimeID] = store
	}
	return func(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
		var store *backendStore
		if auth != nil {
			store = byID[auth.ID]
		}
		if store == nil || req == nil || req.Method != http.MethodGet || req.URL.String() != ClaudeOAuthUsageEndpoint {
			return next(ctx, auth, req)
		}
		path := filepath.Clean(store.opts.AuthDir) + ".usage"
		if body, ok := readUsageCache(path, usageCacheTTL); ok {
			observeUsagePoll(store.name(), "cache_hit")
			return &http.Response{
				StatusCode: http.StatusOK, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
				Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)),
				ContentLength: int64(len(body)), Request: req,
			}, nil
		}
		resp, err := next(ctx, auth, req)
		if err != nil || resp == nil || resp.Body == nil || resp.StatusCode != http.StatusOK {
			return resp, err
		}
		body, errRead := io.ReadAll(io.LimitReader(resp.Body, claudeQuotaMaxBodyBytes+1))
		_ = resp.Body.Close()
		if errRead != nil {
			return nil, errRead
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if len(body) <= claudeQuotaMaxBodyBytes {
			writeUsageCache(path, body)
		}
		return resp, nil
	}
}

func readUsageCache(path string, ttl time.Duration) ([]byte, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > claudeQuotaMaxBodyBytes || time.Since(info.ModTime()) > ttl {
		return nil, false
	}
	f, err := openPrivateFile(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, claudeQuotaMaxBodyBytes+1))
	if err != nil || len(body) == 0 {
		return nil, false
	}
	return body, true
}

// writeUsageCache is best effort: a failure only costs the other processes one extra request.
func writeUsageCache(path string, body []byte) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".usage-*")
	if err != nil {
		return
	}
	name := temp.Name()
	_, errWrite := temp.Write(body)
	errClose := temp.Close()
	if errWrite != nil || errClose != nil || os.Rename(name, path) != nil {
		_ = os.Remove(name)
	}
}

// name is the profile's own name for logs and metrics.
func (s *backendStore) name() string {
	return safeProfileName(s.opts.Name, s.opts.AuthID)
}
