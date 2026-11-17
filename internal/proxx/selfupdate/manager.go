package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// States reported in the status response.
const (
	StateIdle        = "idle"
	StateChecking    = "checking"
	StateDownloading = "downloading"
	StateVerifying   = "verifying"
	StateStaged      = "staged"
	StateWaitingIdle = "waiting_idle"
	StateApplying    = "applying"
	StateFailed      = "failed"
)

// Errors returned to the API as 409.
var (
	ErrUnsupported     = errors.New("self-update is not supported here")
	ErrNothingToApply  = errors.New("no newer release to apply")
	ErrNothingToCancel = errors.New("no pending update to cancel")
	ErrRestarting      = errors.New("the restart has already started")
)

// Timing knobs (variables so unit tests can shorten them).
var (
	initialCheckDelay = 30 * time.Second
	drainFor          = time.Second
	acceptSettle      = 100 * time.Millisecond
	finalIdleWait     = 2 * time.Second
	freshMaxAge       = 3 * time.Second
	windowPoll        = 5 * time.Second
)

// Status is the GET /v8/management/proxx/update response.
type Status struct {
	APILevel          int          `json:"api_level"`
	CurrentVersion    string       `json:"current_version"`
	LatestVersion     string       `json:"latest_version"`
	UpdateAvailable   bool         `json:"update_available"`
	ReleaseURL        string       `json:"release_url"`
	ReleaseNotes      string       `json:"release_notes"`
	CheckedAt         *string      `json:"checked_at"`
	LastCheckError    string       `json:"last_check_error"`
	Supported         bool         `json:"supported"`
	UnsupportedReason string       `json:"unsupported_reason"`
	State             string       `json:"state"`
	InFlight          int64        `json:"inflight"`
	StagedVersion     string       `json:"staged_version"`
	Config            StatusConfig `json:"config"`
	LastResult        *lastResult  `json:"last_result"`
	// Fields beyond the panel contract.
	LastError string `json:"last_error"`
	PID       int    `json:"pid"`
}

// StatusConfig echoes the effective proxx.update config.
type StatusConfig struct {
	Check         bool   `json:"check"`
	IntervalHours int    `json:"interval_hours"`
	AutoApply     bool   `json:"auto_apply"`
	Window        string `json:"window"`
}

type staged struct {
	version string
	path    string
	sha256  string
}

type manager struct {
	mu sync.Mutex

	ctx        context.Context
	cfg        config.ProxxUpdateConfig
	proxyURL   string
	configPath string
	started    bool
	wake       chan struct{} // nudges the check loop and a waiting worker after a config change

	state        string
	checkedAt    time.Time
	lastCheckErr string
	lastErr      string
	latest       *release
	etag         string
	staged       *staged

	workerCancel context.CancelFunc
	workerAuto   bool
	workerVer    string
	restarting   bool
	// noAutoVersion is a release auto-apply must not retry (it failed or was cancelled).
	noAutoVersion string

	env    func() environment
	client func() *http.Client
}

var mgr = newManager()

func newManager() *manager {
	m := &manager{state: StateIdle, wake: make(chan struct{}, 1), env: defaultEnvironment}
	m.client = m.defaultClient
	return m
}

func (m *manager) defaultClient() *http.Client {
	m.mu.Lock()
	proxyURL := m.proxyURL
	m.mu.Unlock()
	client := &http.Client{}
	return util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(proxyURL)}, client)
}

// Start begins the periodic check loop (called from the proxx facade at service start).
func Start(ctx context.Context, cfg *config.Config, configPath string) {
	mgr.start(ctx, cfg, configPath)
}

// Configure applies a hot-reloaded proxx.update config.
func Configure(cfg *config.Config) { mgr.configure(cfg) }

// GetStatus returns the current status.
func GetStatus() Status { return mgr.status() }

// CheckNow checks for a release and returns the resulting status.
func CheckNow(ctx context.Context) Status {
	mgr.check(ctx)
	return mgr.status()
}

// Apply starts downloading, verifying and applying the latest release (manual: ignores the window).
func Apply(ctx context.Context) (Status, error) {
	err := mgr.apply(ctx)
	return mgr.status(), err
}

// Cancel stops a pending apply that has not started the restart yet.
func Cancel() (Status, error) {
	err := mgr.cancel()
	return mgr.status(), err
}

func (m *manager) start(ctx context.Context, cfg *config.Config, configPath string) {
	m.mu.Lock()
	m.ctx = ctx
	m.configPath = configPath
	already := m.started
	m.started = true
	m.mu.Unlock()
	m.configure(cfg)
	if already || ctx == nil {
		return
	}
	if exe := startupExe(); exe != "" {
		_ = os.Remove(exe + ".new") // stale staged binary from a previous run
	}
	go m.loop(ctx)
}

func (m *manager) configure(cfg *config.Config) {
	if cfg == nil {
		return
	}
	m.mu.Lock()
	m.cfg = cfg.Proxx.Update
	m.proxyURL = cfg.ProxyURL
	autoOff := !m.cfg.AutoApply && m.workerAuto && m.workerCancel != nil && !m.restarting
	cancel := m.workerCancel
	m.mu.Unlock()
	if autoOff {
		cancel()
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
	m.maybeAutoApply()
}

func (m *manager) loop(ctx context.Context) {
	next := time.Now().Add(initialCheckDelay)
	for {
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-m.wake:
			timer.Stop()
			continue
		case <-timer.C:
		}
		m.mu.Lock()
		enabled := m.cfg.CheckEnabled()
		interval := time.Duration(m.cfg.Interval()) * time.Hour
		m.mu.Unlock()
		if enabled {
			m.check(ctx)
		}
		next = time.Now().Add(interval)
	}
}

func (m *manager) setState(s string) {
	m.mu.Lock()
	m.state = s
	m.mu.Unlock()
}

// check fetches the latest release (with ETag) and records whether it is an update.
func (m *manager) check(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	showChecking := m.workerCancel == nil && (m.state == StateIdle || m.state == StateFailed)
	if showChecking {
		m.state = StateChecking
	}
	etag := ""
	if m.latest != nil {
		etag = m.etag
	}
	m.mu.Unlock()

	rel, newETag, notModified, err := fetchRelease(ctx, m.client(), etag)

	m.mu.Lock()
	m.checkedAt = time.Now()
	switch {
	case err != nil:
		m.lastCheckErr = err.Error()
		log.WithError(err).Warn("proxx update: release check failed")
	case notModified:
		m.lastCheckErr = ""
	default:
		m.lastCheckErr = ""
		m.latest = rel
		m.etag = newETag
		// The release changed (e.g. assets finished uploading): auto-apply may try it again.
		m.noAutoVersion = ""
	}
	if showChecking && m.state == StateChecking {
		m.state = StateIdle
	}
	m.mu.Unlock()
	m.maybeAutoApply()
}

// candidate returns the latest release when it is a strictly newer, acceptable release.
func (m *manager) candidateLocked() *release {
	rel := m.latest
	if rel == nil || rel.Draft {
		return nil
	}
	if rel.Prerelease && allowPrerelease != "true" {
		return nil
	}
	if !isNewer(rel.Version, buildinfo.Version) {
		return nil
	}
	return rel
}

func (m *manager) status() Status {
	env := m.env()
	reason, supported := checkSupported(env)
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{
		APILevel:          APILevel,
		CurrentVersion:    buildinfo.Version,
		LastCheckError:    m.lastCheckErr,
		Supported:         supported,
		UnsupportedReason: reason,
		State:             m.state,
		InFlight:          InFlight(),
		LastError:         m.lastErr,
		PID:               os.Getpid(),
		Config: StatusConfig{
			Check:         m.cfg.CheckEnabled(),
			IntervalHours: m.cfg.Interval(),
			AutoApply:     m.cfg.AutoApply,
			Window:        m.cfg.Window,
		},
	}
	if !m.checkedAt.IsZero() {
		s := m.checkedAt.UTC().Format(time.RFC3339)
		st.CheckedAt = &s
	}
	if rel := m.latest; rel != nil {
		st.LatestVersion = rel.Version
		st.ReleaseURL = rel.HTMLURL
		st.ReleaseNotes = truncate(rel.Body, maxNotes)
	}
	st.UpdateAvailable = m.candidateLocked() != nil
	if m.staged != nil {
		st.StagedVersion = m.staged.version
	}
	if path := statePath(); path != "" {
		if s, err := loadState(path); err == nil {
			st.LastResult = s.Last
		}
	}
	return st
}

// apply starts the manual apply worker.
func (m *manager) apply(ctx context.Context) error {
	if reason, ok := checkSupported(m.env()); !ok {
		return fmt.Errorf("%w: %s", ErrUnsupported, reason)
	}
	m.mu.Lock()
	running := m.workerCancel != nil
	needCheck := m.latest == nil
	m.mu.Unlock()
	if running {
		return nil
	}
	if needCheck {
		m.check(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workerCancel != nil {
		return nil
	}
	rel := m.candidateLocked()
	if rel == nil {
		return ErrNothingToApply
	}
	m.startWorkerLocked(rel, false)
	return nil
}

// maybeAutoApply starts the auto worker when auto-apply is on and a newer release is known.
func (m *manager) maybeAutoApply() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cfg.AutoApply || m.workerCancel != nil || m.restarting || m.ctx == nil {
		return
	}
	rel := m.candidateLocked()
	if rel == nil {
		return
	}
	if _, ok := checkSupported(m.env()); !ok {
		return
	}
	if rel.Version == m.noAutoVersion {
		return
	}
	m.startWorkerLocked(rel, true)
}

func (m *manager) startWorkerLocked(rel *release, auto bool) {
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.workerCancel = cancel
	m.workerAuto = auto
	m.workerVer = rel.Version
	m.lastErr = ""
	if !auto {
		m.noAutoVersion = ""
	}
	go m.runWorker(ctx, rel, auto)
}

func (m *manager) cancel() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.restarting {
		return ErrRestarting
	}
	if m.workerCancel == nil {
		return ErrNothingToCancel
	}
	m.noAutoVersion = m.workerVer
	m.workerCancel()
	return nil
}

func (m *manager) finishWorker(err error, rel *release) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workerCancel = nil
	m.workerAuto = false
	m.workerVer = ""
	if m.restarting {
		return
	}
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		if m.staged != nil {
			m.state = StateStaged
		} else {
			m.state = StateIdle
		}
	default:
		m.state = StateFailed
		m.noAutoVersion = rel.Version
		m.lastErr = fmt.Sprintf("update to %s failed: %v", rel.Version, err)
		log.Warnf("proxx update: %s", m.lastErr)
	}
}

func (m *manager) runWorker(ctx context.Context, rel *release, auto bool) {
	err := m.work(ctx, rel, auto)
	if err != nil {
		inflight.draining.Store(false)
		if ln := currentListener(); ln != nil {
			ln.setPaused(false)
		}
	}
	m.finishWorker(err, rel)
}

func (m *manager) work(ctx context.Context, rel *release, auto bool) error {
	st, err := m.stage(ctx, rel)
	if err != nil {
		return err
	}
	if err = m.waitForApplyMoment(ctx, auto); err != nil {
		return err
	}
	return m.restart(st)
}

// stage downloads, verifies, extracts and self-checks the release binary into <exe>.new.
func (m *manager) stage(ctx context.Context, rel *release) (*staged, error) {
	m.mu.Lock()
	if s := m.staged; s != nil && s.version == rel.Version {
		if sum, err := fileSHA256(s.path); err == nil && sum == s.sha256 {
			m.mu.Unlock()
			return s, nil
		}
		m.staged = nil
	}
	m.state = StateDownloading
	cfgPath := m.configPath
	m.mu.Unlock()

	exe := startupExe()
	if exe == "" {
		return nil, errors.New("cannot locate the running executable")
	}
	name, err := assetName(rel.Version, runtime.GOOS, runtime.GOARCH, noPluginBuild)
	if err != nil {
		return nil, err
	}
	archiveURL, ok := rel.Assets[name]
	if !ok {
		return nil, fmt.Errorf("release %s has no asset %s", rel.Tag, name)
	}
	sumsURL, ok := rel.Assets["checksums.txt"]
	if !ok {
		return nil, fmt.Errorf("release %s has no checksums.txt", rel.Tag)
	}
	sigURL, ok := rel.Assets["checksums.txt.sig"]
	if !ok {
		return nil, fmt.Errorf("release %s has no checksums.txt.sig (unsigned release)", rel.Tag)
	}
	client := m.client()
	sums, err := download(ctx, client, sumsURL, maxChecksums)
	if err != nil {
		return nil, fmt.Errorf("download checksums.txt: %w", err)
	}
	sig, err := download(ctx, client, sigURL, maxSignature)
	if err != nil {
		return nil, fmt.Errorf("download checksums.txt.sig: %w", err)
	}
	m.setState(StateVerifying)
	pub, err := decodePublicKey(publicKeyB64)
	if err != nil {
		return nil, err
	}
	if err = verifySignature(pub, sums, sig); err != nil {
		return nil, err
	}
	m.setState(StateDownloading)
	archive, err := download(ctx, client, archiveURL, maxArchive)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	m.setState(StateVerifying)
	if err = verifyArchive(sums, name, archive); err != nil {
		return nil, err
	}
	bin, err := extractBinary(archive)
	if err != nil {
		return nil, err
	}
	newPath := exe + ".new"
	if err = writeExecutable(newPath, bin); err != nil {
		return nil, fmt.Errorf("write %s: %w", newPath, err)
	}
	if err = runSelfcheck(ctx, newPath, cfgPath, rel.Version); err != nil {
		_ = os.Remove(newPath)
		return nil, err
	}
	sum := sha256.Sum256(bin)
	s := &staged{version: rel.Version, path: newPath, sha256: hex.EncodeToString(sum[:])}
	m.mu.Lock()
	m.staged = s
	m.state = StateStaged
	m.mu.Unlock()
	log.Infof("proxx update: %s downloaded, verified and staged", rel.Version)
	return s, nil
}

func writeExecutable(path string, data []byte) error {
	_ = os.Remove(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if errClose := f.Close(); err == nil {
		err = errClose
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func (m *manager) inWindow() bool {
	m.mu.Lock()
	ws := m.cfg.Window
	m.mu.Unlock()
	w, err := parseWindow(ws)
	if err != nil {
		return false
	}
	return w.contains(time.Now())
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// waitForApplyMoment returns once nothing is in flight (and, for auto-apply, the window is open). It
// leaves the listener paused and responses marked Connection: close, ready for the restart.
func (m *manager) waitForApplyMoment(ctx context.Context, auto bool) error {
	for {
		if auto && !m.inWindow() {
			m.setState(StateStaged)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-m.wake:
			case <-time.After(windowPoll):
			}
			continue
		}
		m.setState(StateWaitingIdle)
		if err := waitIdle(ctx, time.Second); err != nil {
			if errors.Is(err, errStillBusy) {
				continue // re-check the window
			}
			return err
		}
		// Idle. For the last moment before the switch, ask clients to close their connections.
		inflight.draining.Store(true)
		if err := sleepCtx(ctx, drainFor); err != nil {
			return err
		}
		ln := currentListener()
		if ln != nil {
			ln.setPaused(true) // new connections now wait in the kernel backlog
		}
		if err := sleepCtx(ctx, acceptSettle); err != nil {
			return err
		}
		// Wait until nothing is in flight and every connection accepted before the pause has delivered
		// its request (or stayed silent for freshMaxAge).
		errIdle := waitQuiet(ctx, ln, finalIdleWait)
		if errIdle == nil {
			return nil
		}
		inflight.draining.Store(false)
		if ln != nil {
			ln.setPaused(false)
		}
		if !errors.Is(errIdle, errStillBusy) {
			return errIdle
		}
	}
}

var errStillBusy = errors.New("still busy")

// waitIdle waits up to max for the in-flight count to reach zero.
func waitIdle(ctx context.Context, max time.Duration) error {
	deadline := time.NewTimer(max)
	defer deadline.Stop()
	for {
		sig := inflight.idleSignal()
		if inflight.active.Load() <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errStillBusy
		case <-sig:
		}
	}
}

// waitQuiet waits up to max until no request is in flight and ln has no fresh connection.
func waitQuiet(ctx context.Context, ln *gateListener, max time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		if inflight.active.Load() <= 0 && (ln == nil || ln.freshCount(freshMaxAge) == 0) {
			return nil
		}
		if time.Now().After(deadline) {
			return errStillBusy
		}
		if err := sleepCtx(ctx, 5*time.Millisecond); err != nil {
			return err
		}
	}
}

// restart hands the staged binary to Finish and starts the graceful shutdown.
func (m *manager) restart(s *staged) error {
	m.mu.Lock()
	if m.restarting {
		m.mu.Unlock()
		return ErrRestarting
	}
	m.restarting = true
	m.state = StateApplying
	m.mu.Unlock()

	exe := startupExe()
	fd, err := dupListenerFD(currentListener())
	if err != nil {
		log.WithError(err).Warn("proxx update: cannot hand over the listening socket; the new process binds again")
		fd = -1
	}
	inflight.cancelManagement()
	p := &pendingExec{
		newPath:     s.path,
		exePath:     exe,
		prevPath:    exe + ".prev",
		sha256:      s.sha256,
		fromVersion: buildinfo.Version,
		toVersion:   s.version,
		listenFD:    fd,
	}
	log.Infof("proxx update: idle; restarting into %s", s.version)
	if err = requestRestart(p); err != nil {
		if fd >= 0 {
			closeFD(fd)
		}
		m.mu.Lock()
		m.restarting = false
		m.mu.Unlock()
		return err
	}
	return nil
}
