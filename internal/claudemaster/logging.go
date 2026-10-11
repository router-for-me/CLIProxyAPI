package claudemaster

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// claude-master's own log. It is deliberately separate from the upstream SDK's logrus output, which
// stays discarded: that output can carry credential paths and upstream response text.
//
// WHAT IS EVER LOGGED. Profile names (your own labels), hashed conversation tags, model names, status
// codes, durations, counts, quota fractions, client certificate names and a hashed client-account key.
// NEVER: tokens, request or response bodies, URLs, account identifiers (emails, UUIDs), or upstream
// error text. redactAttr is a second line of defence behind that discipline, not a substitute for it.
//
// LEVELS. info is the operating level and says when something CHANGES: a conversation moved to another
// account, a profile was rate limited or became available, a quota band was crossed, a login was
// refreshed or rejected, the API-key backup was used, a client was refused; plus a quota snapshot every
// few minutes. debug adds every routing decision and request. warn and error are for faults.

// A PRIVATE logrus logger: the global one stays discarded (see above), and this one writes only what this
// package chooses to say. logger is the small message-and-pairs shape every call site uses.
type logger struct{ l *log.Logger }

var discardLogger = &logger{}

var activeLogger atomic.Pointer[logger]

func lg() *logger {
	if g := activeLogger.Load(); g != nil {
		return g
	}
	return discardLogger
}

func (g *logger) log(level log.Level, msg string, kv []any) {
	if g == nil || g.l == nil || !g.l.IsLevelEnabled(level) {
		return
	}
	fields := make(log.Fields, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			key = fmt.Sprint(kv[i])
		}
		fields[key] = redactField(key, kv[i+1])
	}
	g.l.WithFields(fields).Log(level, msg)
}

func (g *logger) Debug(msg string, kv ...any) { g.log(log.DebugLevel, msg, kv) }
func (g *logger) Info(msg string, kv ...any)  { g.log(log.InfoLevel, msg, kv) }
func (g *logger) Warn(msg string, kv ...any)  { g.log(log.WarnLevel, msg, kv) }
func (g *logger) Error(msg string, kv ...any) { g.log(log.ErrorLevel, msg, kv) }

// utcHook stamps every line in UTC, whatever the box's zone.
type utcHook struct{}

func (utcHook) Levels() []log.Level { return log.AllLevels }
func (utcHook) Fire(e *log.Entry) error {
	e.Time = e.Time.UTC()
	return nil
}

const logTimeFormat = "2006-01-02T15:04:05.000Z07:00"

// LogOptions configures claude-master's own logging.
type LogOptions struct {
	Level    string    // debug, info, warn, error or off
	File     string    // append to this file (rotated); empty means Out
	Format   string    // text (default) or json
	Out      io.Writer // used when File is empty
	MaxBytes int64     // rotate when the file would exceed this (default 10 MiB)
	Keep     int       // rotated files to keep (default 5)
}

const (
	defaultLogMaxBytes = 10 << 20
	defaultLogKeep     = 5
)

// ConfigureLogging installs the logger and returns a function that flushes and closes it.
func ConfigureLogging(opts LogOptions) (func(), error) {
	level, err := ParseLogLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	if level == nil {
		activeLogger.Store(nil)
		return func() {}, nil
	}
	var out io.Writer = opts.Out
	closeFn := func() {}
	if opts.File != "" {
		maxBytes, keep := opts.MaxBytes, opts.Keep
		if maxBytes <= 0 {
			maxBytes = defaultLogMaxBytes
		}
		if keep <= 0 {
			keep = defaultLogKeep
		}
		file, err := openRotatingFile(opts.File, maxBytes, keep)
		if err != nil {
			return nil, err
		}
		out, closeFn = file, func() { _ = file.Close() }
	}
	if out == nil {
		return nil, errors.New("logging needs a file or an output")
	}
	l := log.New()
	l.SetOutput(out)
	l.SetLevel(*level)
	l.AddHook(utcHook{})
	switch strings.ToLower(opts.Format) {
	case "", "text":
		l.SetFormatter(&log.TextFormatter{DisableColors: true, FullTimestamp: true, TimestampFormat: logTimeFormat})
	case "json":
		l.SetFormatter(&log.JSONFormatter{TimestampFormat: logTimeFormat})
	default:
		return nil, errors.New("log format must be text or json")
	}
	activeLogger.Store(&logger{l: l})
	return closeFn, nil
}

// ParseLogLevel returns nil for "off".
func ParseLogLevel(name string) (*log.Level, error) {
	var level log.Level
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "off", "none":
		return nil, nil
	case "debug":
		level = log.DebugLevel
	case "", "info":
		level = log.InfoLevel
	case "warn", "warning":
		level = log.WarnLevel
	case "error":
		level = log.ErrorLevel
	default:
		return nil, errors.New("log level must be debug, info, warn, error or off")
	}
	return &level, nil
}

var (
	secretKey   = regexp.MustCompile(`(?i)(token|secret|authorization|password|bearer|api[_-]?key|cookie|email|credential)`)
	secretValue = regexp.MustCompile(`(sk-ant-[A-Za-z0-9_-]{6,}|eyJ[A-Za-z0-9_-]{10,}|(?i:bearer)\s+[A-Za-z0-9._~+/=-]{8,})`)
)

// redactField drops values of fields named like secrets and masks values that look like one.
func redactField(key string, v any) any {
	if secretKey.MatchString(key) {
		return "[redacted]"
	}
	if str, ok := v.(string); ok && secretValue.MatchString(str) {
		return secretValue.ReplaceAllString(str, "[redacted]")
	}
	return v
}

// ---------------------------------------------------------------- rotation

// rotatingFile appends to path and rotates it by size: path becomes path.1, path.1 becomes path.2, and so
// on up to keep files; the oldest is removed. 0600, like every other private file here.
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	file     *os.File
	size     int64
}

func openRotatingFile(path string, maxBytes int64, keep int) (*rotatingFile, error) {
	r := &rotatingFile{path: path, maxBytes: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, fmt.Errorf("cannot open the log file: %w", err)
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	// The mode given to OpenFile applies only to a file it creates. An existing log (made by hand, by an
	// older version, or copied) may be 0644: make it private before anything is appended to it.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.file, r.size = f, info.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			// Keep logging into the current file rather than lose the line.
			n, werr := r.file.Write(p)
			r.size += int64(n)
			return n, werr
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotate() error {
	if err := r.file.Close(); err != nil {
		return err
	}
	r.file = nil
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		_ = r.open()
		return err
	}
	for i := 1; i <= r.keep; i++ { // rotated copies are as private as the live file, whatever they were before
		_ = os.Chmod(fmt.Sprintf("%s.%d", r.path, i), 0o600)
	}
	return r.open()
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// ---------------------------------------------------------------- tags

// sessionTag is a short stable tag for a conversation: enough to follow one in the log, never the id.
func sessionTag(sessionID string) string {
	if sessionID == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(sessionID))
	return "s-" + hex.EncodeToString(sum[:4])
}

// ---------------------------------------------------------------- rate-limited lines

// every logs at most once per interval for a key, so a stuck state is one line a minute, not a flood.
type every struct {
	mu   sync.Mutex
	last map[string]time.Time
	gap  time.Duration
	now  func() time.Time
}

func newEvery(gap time.Duration) *every {
	return &every{last: make(map[string]time.Time), gap: gap, now: time.Now}
}

func (e *every) allow(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if last, ok := e.last[key]; ok && now.Sub(last) < e.gap {
		return false
	}
	e.last[key] = now
	return true
}
