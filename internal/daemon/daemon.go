// Package daemon manages the CLIProxyAPI background process lifecycle.
package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	childEnvironment         = "CLIPROXY_DAEMON_CHILD"
	childStateDirEnvironment = "CLIPROXY_DAEMON_STATE_DIR"
	childLogPathEnvironment  = "CLIPROXY_DAEMON_LOG_PATH"
	stateFileName            = "daemon.json"
	startLockFileName        = "daemon.start.lock"
)

var errStateNotFound = errors.New("daemon state not found")

// State describes one running background instance.
type State struct {
	PID          int       `json:"pid"`
	StartedAt    time.Time `json:"started_at"`
	Executable   string    `json:"executable"`
	WorkingDir   string    `json:"working_dir"`
	LogPath      string    `json:"log_path"`
	ControlAddr  string    `json:"control_addr"`
	ControlToken string    `json:"control_token"`
}

// StopResult reports whether a background instance was found and stopped.
type StopResult struct {
	PID        int
	WasRunning bool
}

// Manager owns the user-level runtime state for a single background instance.
type Manager struct {
	stateDir     string
	statePath    string
	startLock    string
	logPath      string
	startWait    time.Duration
	stopWait     time.Duration
	pollInterval time.Duration
}

// Registration connects a daemon child to its authenticated local stop endpoint.
type Registration struct {
	manager   *Manager
	state     State
	ctx       context.Context
	cancel    context.CancelFunc
	listener  net.Listener
	serveDone chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// DefaultManager returns the per-user background process manager.
func DefaultManager() (*Manager, error) {
	if IsChild() {
		stateDir := strings.TrimSpace(os.Getenv(childStateDirEnvironment))
		logPath := strings.TrimSpace(os.Getenv(childLogPathEnvironment))
		if stateDir != "" && logPath != "" {
			return newManager(stateDir, logPath), nil
		}
	}

	configRoot, errConfigDir := os.UserConfigDir()
	if errConfigDir != nil || strings.TrimSpace(configRoot) == "" {
		homeDir, errHomeDir := os.UserHomeDir()
		if errHomeDir != nil {
			return nil, fmt.Errorf("daemon: resolve user config directory: %w", errHomeDir)
		}
		configRoot = filepath.Join(homeDir, ".cli-proxy-api")
	} else {
		configRoot = filepath.Join(configRoot, "cli-proxy-api")
	}

	cacheRoot, errCacheDir := os.UserCacheDir()
	if errCacheDir != nil || strings.TrimSpace(cacheRoot) == "" {
		cacheRoot = configRoot
	} else {
		cacheRoot = filepath.Join(cacheRoot, "cli-proxy-api")
	}

	return newManager(configRoot, filepath.Join(cacheRoot, "daemon.log")), nil
}

func newManager(stateDir, logPath string) *Manager {
	return &Manager{
		stateDir:     filepath.Clean(stateDir),
		statePath:    filepath.Join(filepath.Clean(stateDir), stateFileName),
		startLock:    filepath.Join(filepath.Clean(stateDir), startLockFileName),
		logPath:      filepath.Clean(logPath),
		startWait:    10 * time.Second,
		stopWait:     40 * time.Second,
		pollInterval: 50 * time.Millisecond,
	}
}

// IsChild reports whether the current process was spawned by Start.
func IsChild() bool {
	return os.Getenv(childEnvironment) == "1"
}

// IsStopCommand reports whether args request the standalone stop command.
func IsStopCommand(args []string) bool {
	return len(args) > 0 && args[0] == "stop"
}

// Start launches a detached copy of the current executable and waits for its
// authenticated control endpoint to become available.
func (m *Manager) Start(args []string) (State, error) {
	if m == nil {
		return State{}, errors.New("daemon: manager is nil")
	}
	if errEnsure := m.ensureDirectories(); errEnsure != nil {
		return State{}, errEnsure
	}

	releaseLock, errLock := m.acquireStartLock()
	if errLock != nil {
		return State{}, errLock
	}
	defer releaseLock()

	if existing, errLoad := m.loadState(); errLoad == nil {
		if processRunning(existing.PID) {
			return State{}, fmt.Errorf("daemon: CLIProxyAPI is already running in background with pid %d", existing.PID)
		}
		if errRemove := os.Remove(m.statePath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return State{}, fmt.Errorf("daemon: remove stale state: %w", errRemove)
		}
	} else if !errors.Is(errLoad, errStateNotFound) {
		return State{}, errLoad
	}

	executable, errExecutable := os.Executable()
	if errExecutable != nil {
		return State{}, fmt.Errorf("daemon: resolve executable: %w", errExecutable)
	}
	workingDir, errWorkingDir := os.Getwd()
	if errWorkingDir != nil {
		return State{}, fmt.Errorf("daemon: resolve working directory: %w", errWorkingDir)
	}

	logFile, errLogFile := os.OpenFile(m.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if errLogFile != nil {
		return State{}, fmt.Errorf("daemon: open background log: %w", errLogFile)
	}
	defer func() { _ = logFile.Close() }()

	devNull, errDevNull := os.Open(os.DevNull)
	if errDevNull != nil {
		return State{}, fmt.Errorf("daemon: open null device: %w", errDevNull)
	}
	defer func() { _ = devNull.Close() }()

	childArgs := stripDaemonFlag(args)
	command := exec.Command(executable, childArgs...)
	command.Dir = workingDir
	command.Env = append(os.Environ(),
		childEnvironment+"=1",
		childStateDirEnvironment+"="+m.stateDir,
		childLogPathEnvironment+"="+m.logPath,
	)
	command.Stdin = devNull
	command.Stdout = logFile
	command.Stderr = logFile
	configureBackgroundProcess(command)
	if errStart := command.Start(); errStart != nil {
		return State{}, fmt.Errorf("daemon: start background process: %w", errStart)
	}

	exitCh := make(chan error, 1)
	go func() { exitCh <- command.Wait() }()

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(m.startWait)
	defer timer.Stop()
	for {
		select {
		case errWait := <-exitCh:
			if errWait == nil {
				errWait = errors.New("process exited before registering its control endpoint")
			}
			return State{}, fmt.Errorf("daemon: background process failed to start: %w; see %s", errWait, m.logPath)
		case <-ticker.C:
			state, errLoad := m.loadState()
			if errLoad == nil && state.PID == command.Process.Pid {
				return state, nil
			}
			if errLoad != nil && !errors.Is(errLoad, errStateNotFound) {
				return State{}, errLoad
			}
		case <-timer.C:
			return State{}, fmt.Errorf("daemon: timed out waiting for background process; see %s", m.logPath)
		}
	}
}

// RegisterChild publishes the current daemon process and starts its local stop endpoint.
func (m *Manager) RegisterChild() (*Registration, error) {
	if m == nil {
		return nil, errors.New("daemon: manager is nil")
	}
	if errEnsure := m.ensureDirectories(); errEnsure != nil {
		return nil, errEnsure
	}
	if existing, errLoad := m.loadState(); errLoad == nil {
		if existing.PID != os.Getpid() && processRunning(existing.PID) {
			return nil, fmt.Errorf("daemon: CLIProxyAPI is already running in background with pid %d", existing.PID)
		}
		if errRemove := os.Remove(m.statePath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return nil, fmt.Errorf("daemon: remove stale state: %w", errRemove)
		}
	} else if !errors.Is(errLoad, errStateNotFound) {
		return nil, errLoad
	}

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		return nil, fmt.Errorf("daemon: open local control endpoint: %w", errListen)
	}
	tokenBytes := make([]byte, 32)
	if _, errRandom := io.ReadFull(rand.Reader, tokenBytes); errRandom != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("daemon: generate control token: %w", errRandom)
	}

	executable, errExecutable := os.Executable()
	if errExecutable != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("daemon: resolve executable: %w", errExecutable)
	}
	workingDir, errWorkingDir := os.Getwd()
	if errWorkingDir != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("daemon: resolve working directory: %w", errWorkingDir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	registration := &Registration{
		manager: m,
		state: State{
			PID:          os.Getpid(),
			StartedAt:    time.Now().UTC(),
			Executable:   executable,
			WorkingDir:   workingDir,
			LogPath:      m.logPath,
			ControlAddr:  listener.Addr().String(),
			ControlToken: hex.EncodeToString(tokenBytes),
		},
		ctx:       ctx,
		cancel:    cancel,
		listener:  listener,
		serveDone: make(chan struct{}),
	}
	if errWrite := m.writeState(registration.state); errWrite != nil {
		cancel()
		_ = listener.Close()
		return nil, errWrite
	}
	go registration.serve()
	return registration, nil
}

// Stop requests graceful shutdown from the registered background process and
// waits until it removes its runtime state.
func (m *Manager) Stop() (StopResult, error) {
	if m == nil {
		return StopResult{}, errors.New("daemon: manager is nil")
	}
	state, errLoad := m.loadState()
	if errors.Is(errLoad, errStateNotFound) {
		return StopResult{}, nil
	}
	if errLoad != nil {
		return StopResult{}, errLoad
	}
	result := StopResult{PID: state.PID}
	if !processRunning(state.PID) {
		if errRemove := os.Remove(m.statePath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return result, fmt.Errorf("daemon: remove stale state: %w", errRemove)
		}
		return result, nil
	}
	if errValidate := validateControlAddress(state.ControlAddr); errValidate != nil {
		return result, errValidate
	}

	dialer := net.Dialer{Timeout: 2 * time.Second}
	connection, errDial := dialer.Dial("tcp", state.ControlAddr)
	if errDial != nil {
		return result, fmt.Errorf("daemon: contact background process %d: %w", state.PID, errDial)
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, errWrite := io.WriteString(connection, state.ControlToken+"\n"); errWrite != nil {
		return result, fmt.Errorf("daemon: send graceful stop request: %w", errWrite)
	}
	response, errRead := bufio.NewReader(io.LimitReader(connection, 16)).ReadString('\n')
	if errRead != nil {
		return result, fmt.Errorf("daemon: read graceful stop response: %w", errRead)
	}
	if strings.TrimSpace(response) != "ok" {
		return result, errors.New("daemon: background process rejected the stop request")
	}
	result.WasRunning = true

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(m.stopWait)
	defer timer.Stop()
	for {
		select {
		case <-ticker.C:
			current, errCurrent := m.loadState()
			if errors.Is(errCurrent, errStateNotFound) {
				return result, nil
			}
			if errCurrent != nil {
				return result, errCurrent
			}
			if current.PID != state.PID {
				return result, nil
			}
			if !processRunning(state.PID) {
				if errRemove := os.Remove(m.statePath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
					return result, fmt.Errorf("daemon: remove stale state: %w", errRemove)
				}
				return result, nil
			}
		case <-timer.C:
			return result, fmt.Errorf("daemon: timed out waiting for process %d to stop gracefully", state.PID)
		}
	}
}

// Context is cancelled when an authenticated stop command is received.
func (r *Registration) Context() context.Context {
	if r == nil || r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

// Close removes the runtime state owned by this registration.
func (r *Registration) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.cancel()
		if errClose := r.listener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			r.closeErr = fmt.Errorf("daemon: close control endpoint: %w", errClose)
		}
		<-r.serveDone
		if errRemove := r.manager.removeStateIfOwned(r.state); errRemove != nil && r.closeErr == nil {
			r.closeErr = errRemove
		}
	})
	return r.closeErr
}

func (r *Registration) serve() {
	defer close(r.serveDone)
	for {
		connection, errAccept := r.listener.Accept()
		if errAccept != nil {
			return
		}
		go r.handleConnection(connection)
	}
}

func (r *Registration) handleConnection(connection net.Conn) {
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	token, errRead := bufio.NewReader(io.LimitReader(connection, 256)).ReadString('\n')
	if errRead != nil {
		return
	}
	provided := strings.TrimSpace(token)
	if subtle.ConstantTimeCompare([]byte(provided), []byte(r.state.ControlToken)) != 1 {
		_, _ = io.WriteString(connection, "denied\n")
		return
	}
	_, _ = io.WriteString(connection, "ok\n")
	r.cancel()
	_ = r.listener.Close()
}

func (m *Manager) ensureDirectories() error {
	if errMkdir := os.MkdirAll(m.stateDir, 0o700); errMkdir != nil {
		return fmt.Errorf("daemon: create state directory: %w", errMkdir)
	}
	if errChmod := os.Chmod(m.stateDir, 0o700); errChmod != nil {
		return fmt.Errorf("daemon: secure state directory: %w", errChmod)
	}
	logDir := filepath.Dir(m.logPath)
	if errMkdir := os.MkdirAll(logDir, 0o700); errMkdir != nil {
		return fmt.Errorf("daemon: create log directory: %w", errMkdir)
	}
	return nil
}

func (m *Manager) acquireStartLock() (func(), error) {
	for attempt := 0; attempt < 2; attempt++ {
		lockFile, errOpen := os.OpenFile(m.startLock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errOpen == nil {
			_, errWrite := io.WriteString(lockFile, strconv.Itoa(os.Getpid()))
			errClose := lockFile.Close()
			if errWrite != nil {
				_ = os.Remove(m.startLock)
				return nil, fmt.Errorf("daemon: write start lock: %w", errWrite)
			}
			if errClose != nil {
				_ = os.Remove(m.startLock)
				return nil, fmt.Errorf("daemon: close start lock: %w", errClose)
			}
			return func() { _ = os.Remove(m.startLock) }, nil
		}
		if !errors.Is(errOpen, os.ErrExist) {
			return nil, fmt.Errorf("daemon: create start lock: %w", errOpen)
		}
		rawPID, errRead := os.ReadFile(m.startLock)
		if errRead == nil {
			lockPID, errParse := strconv.Atoi(strings.TrimSpace(string(rawPID)))
			if errParse == nil && processRunning(lockPID) {
				return nil, fmt.Errorf("daemon: another background start is already in progress with pid %d", lockPID)
			}
		}
		if errRemove := os.Remove(m.startLock); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return nil, fmt.Errorf("daemon: remove stale start lock: %w", errRemove)
		}
	}
	return nil, errors.New("daemon: failed to acquire background start lock")
}

func (m *Manager) loadState() (State, error) {
	raw, errRead := os.ReadFile(m.statePath)
	if errors.Is(errRead, os.ErrNotExist) {
		return State{}, errStateNotFound
	}
	if errRead != nil {
		return State{}, fmt.Errorf("daemon: read state: %w", errRead)
	}
	var state State
	if errUnmarshal := json.Unmarshal(raw, &state); errUnmarshal != nil {
		return State{}, fmt.Errorf("daemon: parse state: %w", errUnmarshal)
	}
	if state.PID <= 1 || strings.TrimSpace(state.ControlAddr) == "" || len(state.ControlToken) != 64 {
		return State{}, errors.New("daemon: invalid runtime state")
	}
	return state, nil
}

func (m *Manager) writeState(state State) error {
	raw, errMarshal := json.MarshalIndent(state, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("daemon: encode state: %w", errMarshal)
	}
	temporaryPath := fmt.Sprintf("%s.tmp-%d", m.statePath, os.Getpid())
	temporaryFile, errOpen := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return fmt.Errorf("daemon: create temporary state: %w", errOpen)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, errWrite := temporaryFile.Write(append(raw, '\n')); errWrite != nil {
		_ = temporaryFile.Close()
		return fmt.Errorf("daemon: write temporary state: %w", errWrite)
	}
	if errSync := temporaryFile.Sync(); errSync != nil {
		_ = temporaryFile.Close()
		return fmt.Errorf("daemon: sync temporary state: %w", errSync)
	}
	if errClose := temporaryFile.Close(); errClose != nil {
		return fmt.Errorf("daemon: close temporary state: %w", errClose)
	}
	if errRename := os.Rename(temporaryPath, m.statePath); errRename != nil {
		return fmt.Errorf("daemon: publish state: %w", errRename)
	}
	removeTemporary = false
	return nil
}

func (m *Manager) removeStateIfOwned(state State) error {
	current, errLoad := m.loadState()
	if errors.Is(errLoad, errStateNotFound) {
		return nil
	}
	if errLoad != nil {
		return errLoad
	}
	if current.PID != state.PID || subtle.ConstantTimeCompare([]byte(current.ControlToken), []byte(state.ControlToken)) != 1 {
		return nil
	}
	if errRemove := os.Remove(m.statePath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		return fmt.Errorf("daemon: remove state: %w", errRemove)
	}
	return nil
}

func validateControlAddress(address string) error {
	host, _, errSplit := net.SplitHostPort(address)
	if errSplit != nil {
		return fmt.Errorf("daemon: invalid control address: %w", errSplit)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("daemon: control address is not loopback")
	}
	return nil
}

func stripDaemonFlag(args []string) []string {
	result := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "-d", "--d", "-d=true", "--d=true":
			continue
		default:
			result = append(result, arg)
		}
	}
	return result
}
