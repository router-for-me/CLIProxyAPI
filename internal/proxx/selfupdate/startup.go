package selfupdate

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

// selfcheckFlag makes the binary print its version (and optionally load a config copy) and exit. The
// updater runs the downloaded binary with it before installing it.
const selfcheckFlag = "--proxx-selfcheck"

// healthDeadline bounds how long a freshly updated process may take to serve /healthz before it exits
// (counting as a failed start toward the rollback).
var healthDeadline = 5 * time.Minute

// Indirections for tests.
var (
	execFn = execBinary
	exitFn = os.Exit
)

var boot struct {
	sync.Mutex
	exe           string
	statePath     string
	pendingHealth bool
	healthy       chan struct{}
	trigger       func()
	exec          *pendingExec
}

// pendingExec describes a staged binary to install and exec once the server has shut down.
type pendingExec struct {
	newPath, exePath, prevPath string
	sha256                     string
	fromVersion, toVersion     string
	listenFD                   int
}

func startupExe() string {
	boot.Lock()
	defer boot.Unlock()
	return boot.exe
}

func statePath() string {
	boot.Lock()
	defer boot.Unlock()
	return boot.statePath
}

// SetRestartTrigger registers the function that starts the normal graceful shutdown of the running
// server (hook in internal/cmd/run.go). Without it, in-place updates are not supported.
func SetRestartTrigger(fn func()) {
	boot.Lock()
	boot.trigger = fn
	boot.Unlock()
}

func restartTriggerSet() bool {
	boot.Lock()
	defer boot.Unlock()
	return boot.trigger != nil
}

// Startup runs first in main (hook in cmd/server/main.go). It handles --proxx-selfcheck, counts start
// attempts of a freshly installed version and rolls back to the previous binary when a new version keeps
// failing to become healthy.
func Startup() {
	if len(os.Args) > 1 && os.Args[1] == selfcheckFlag {
		os.Exit(selfcheck(os.Args[2:]))
	}
	exe, err := os.Executable()
	if err == nil {
		if resolved, errEval := filepath.EvalSymlinks(exe); errEval == nil {
			exe = resolved
		}
	} else {
		exe = ""
	}
	path := statePathFromArgs(os.Args[1:])
	boot.Lock()
	boot.exe = exe
	boot.statePath = path
	boot.healthy = make(chan struct{})
	boot.Unlock()

	st, errLoad := loadState(path)
	if errLoad != nil {
		log.WithError(errLoad).Warn("proxx update: cannot read update state")
	} else if st.PendingVersion != "" {
		handlePendingStart(path, st)
	}
	if testCrashOnStart == "1" {
		_, _ = fmt.Fprintln(os.Stderr, "proxx update: test build crashing on start")
		os.Exit(3)
	}
}

func handlePendingStart(path string, st *updateState) {
	if st.PendingVersion != buildinfo.Version {
		// Something else was started (e.g. the previous binary restored by hand); stop tracking.
		st.Last = &lastResult{From: st.FromVersion, To: st.PendingVersion, At: nowRFC3339(),
			Error: fmt.Sprintf("started version %s instead of %s", buildinfo.Version, st.PendingVersion)}
		clearPending(st)
		if err := saveState(path, st); err != nil {
			log.WithError(err).Warn("proxx update: cannot write update state")
		}
		return
	}
	st.Attempts++
	if st.Attempts >= maxStartAttempts {
		rollback(path, st)
		return
	}
	if err := saveState(path, st); err != nil {
		log.WithError(err).Warn("proxx update: cannot write update state")
	}
	boot.Lock()
	boot.pendingHealth = true
	healthy := boot.healthy
	boot.Unlock()
	deadline, exit := healthDeadline, exitFn
	go func() {
		select {
		case <-healthy:
		case <-time.After(deadline):
			log.Errorf("proxx update: version %s did not become healthy within %s; exiting so the supervisor restarts it", buildinfo.Version, deadline)
			exit(1)
		}
	}()
}

func clearPending(st *updateState) {
	st.PendingVersion = ""
	st.FromVersion = ""
	st.PrevPath = ""
	st.ExePath = ""
	st.Attempts = 0
	st.StartedAt = ""
}

// rollback restores the previous binary and execs it. It only returns by exiting the process.
func rollback(path string, st *updateState) {
	exe := st.ExePath
	if exe == "" {
		exe = startupExe()
	}
	msg := fmt.Sprintf("version %s did not become healthy after %d start attempts", st.PendingVersion, st.Attempts-1)
	log.Errorf("proxx update: %s; rolling back to %s", msg, st.FromVersion)
	errRename := os.Rename(st.PrevPath, exe)
	st.Last = &lastResult{From: st.FromVersion, To: st.PendingVersion, At: nowRFC3339(), OK: false, RolledBack: errRename == nil, Error: msg}
	if errRename != nil {
		st.Last.Error = msg + "; rollback failed: " + errRename.Error()
	}
	clearPending(st)
	if err := saveState(path, st); err != nil {
		log.WithError(err).Warn("proxx update: cannot write update state")
	}
	if errRename != nil {
		log.WithError(errRename).Error("proxx update: cannot restore the previous binary")
		exitFn(1)
	}
	if err := execFn(exe, os.Args, os.Environ()); err != nil {
		log.WithError(err).Error("proxx update: cannot exec the previous binary; exiting so the supervisor restarts it")
	}
	exitFn(1)
}

// startHealthProbe confirms a freshly updated server is serving by requesting its own /healthz.
func startHealthProbe(addr net.Addr) {
	boot.Lock()
	pending := boot.pendingHealth
	boot.pendingHealth = false
	boot.Unlock()
	if !pending {
		return
	}
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return
	}
	host := "127.0.0.1"
	if tcp.IP != nil && !tcp.IP.IsUnspecified() {
		host = tcp.IP.String()
	}
	hostPort := net.JoinHostPort(host, strconv.Itoa(tcp.Port))
	go probeHealth(hostPort)
}

func probeHealth(hostPort string) {
	client := &http.Client{
		Timeout:   5 * time.Second,                                                         // local loopback probe only
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // probing our own loopback listener
	}
	for {
		for _, scheme := range []string{"http", "https"} {
			resp, err := client.Get(scheme + "://" + hostPort + "/healthz")
			if err != nil {
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				markHealthy()
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func markHealthy() {
	boot.Lock()
	select {
	case <-boot.healthy:
	default:
		close(boot.healthy)
	}
	boot.Unlock()
	path := statePath()
	st, err := loadState(path)
	if err != nil || st.PendingVersion == "" {
		return
	}
	st.Last = &lastResult{From: st.FromVersion, To: st.PendingVersion, At: nowRFC3339(), OK: true}
	clearPending(st)
	if errSave := saveState(path, st); errSave != nil {
		log.WithError(errSave).Warn("proxx update: cannot write update state")
		return
	}
	log.Infof("proxx update: now running %s (updated from %s)", buildinfo.Version, st.Last.From)
}

// requestRestart records the staged binary to exec and starts the graceful shutdown.
func requestRestart(p *pendingExec) error {
	boot.Lock()
	trigger := boot.trigger
	if trigger == nil {
		boot.Unlock()
		return fmt.Errorf("no restart trigger registered")
	}
	boot.exec = p
	boot.Unlock()
	trigger()
	return nil
}

// Finish runs after the server has shut down (hook in cmd/server/main.go). When an update is staged
// it installs the new binary and execs it in place (same PID, inherited listening socket). It returns
// the exit code to use when there is nothing to exec or the exec failed.
func Finish(code int) int {
	boot.Lock()
	p := boot.exec
	boot.exec = nil
	path := boot.statePath
	boot.Unlock()
	if p == nil {
		return code
	}
	err := installAndExec(path, p)
	// Only reached on failure.
	log.WithError(err).Error("proxx update: restart failed; exiting so the supervisor restarts proxx")
	st, errLoad := loadState(path)
	if errLoad != nil {
		st = &updateState{}
	}
	clearPending(st)
	st.Last = &lastResult{From: p.fromVersion, To: p.toVersion, At: nowRFC3339(), Error: err.Error()}
	if errSave := saveState(path, st); errSave != nil {
		log.WithError(errSave).Warn("proxx update: cannot write update state")
	}
	return 1
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// installAndExec swaps <exe>.new into place (keeping <exe>.prev) and execs it. It returns only on failure,
// after restoring the previous binary.
func installAndExec(path string, p *pendingExec) error {
	sum, err := fileSHA256(p.newPath)
	if err != nil {
		return fmt.Errorf("staged binary: %w", err)
	}
	if sum != p.sha256 {
		return fmt.Errorf("staged binary %s changed after verification", p.newPath)
	}
	st, errLoad := loadState(path)
	if errLoad != nil {
		st = &updateState{}
	}
	st.PendingVersion = p.toVersion
	st.FromVersion = p.fromVersion
	st.ExePath = p.exePath
	st.PrevPath = p.prevPath
	st.Attempts = 0
	st.StartedAt = nowRFC3339()
	if err = saveState(path, st); err != nil {
		return fmt.Errorf("write update state: %w", err)
	}
	if err = os.Rename(p.exePath, p.prevPath); err != nil {
		return fmt.Errorf("keep previous binary: %w", err)
	}
	if err = os.Rename(p.newPath, p.exePath); err != nil {
		_ = os.Rename(p.prevPath, p.exePath)
		return fmt.Errorf("install new binary: %w", err)
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, listenFDEnv+"=") {
			env = append(env, kv)
		}
	}
	if p.listenFD >= 0 {
		if errFD := clearCloexec(p.listenFD); errFD == nil {
			env = append(env, listenFDEnv+"="+strconv.Itoa(p.listenFD))
		} else {
			log.WithError(errFD).Warn("proxx update: cannot pass the listening socket; the new process binds again")
		}
	}
	log.Infof("proxx update: exec %s (%s -> %s)", p.exePath, p.fromVersion, p.toVersion)
	errExec := execFn(p.exePath, os.Args, env)
	// Exec failed: restore the previous binary so a supervisor restart runs a known-good version.
	if errRestore := os.Rename(p.prevPath, p.exePath); errRestore != nil {
		log.WithError(errRestore).Error("proxx update: cannot restore the previous binary")
	}
	return fmt.Errorf("exec %s: %w", p.exePath, errExec)
}

// selfcheck implements --proxx-selfcheck [config]: print the version and, when a config path is given,
// load a copy of it from a temporary directory (loading may rewrite the file).
func selfcheck(args []string) int {
	fmt.Printf("proxx-selfcheck version=%s\n", buildinfo.Version)
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return 0
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "selfcheck: read config: %v\n", err)
		return 2
	}
	dir, err := os.MkdirTemp("", "proxx-selfcheck-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "selfcheck: temp dir: %v\n", err)
		return 2
	}
	defer func() { _ = os.RemoveAll(dir) }()
	copyPath := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(copyPath, data, 0o600); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "selfcheck: write config copy: %v\n", err)
		return 2
	}
	if _, err = config.LoadConfig(copyPath); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "selfcheck: load config: %v\n", err)
		return 3
	}
	fmt.Println("proxx-selfcheck config=ok")
	return 0
}

// runSelfcheck runs `<bin> --proxx-selfcheck <config>` and checks the reported version.
func runSelfcheck(ctx context.Context, bin, configPath, wantVersion string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := commandOutput(ctx, bin, selfcheckFlag, configPath)
	if err != nil {
		return fmt.Errorf("new binary failed its self-check: %w: %s", err, truncate(strings.TrimSpace(string(out)), 400))
	}
	want := "proxx-selfcheck version=" + wantVersion
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == want {
			return nil
		}
	}
	return fmt.Errorf("new binary reports a different version (want %s): %s", wantVersion, truncate(strings.TrimSpace(string(out)), 400))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
