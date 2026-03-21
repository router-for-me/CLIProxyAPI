package localrouting

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Runtime struct {
	store  *RouteStore
	edge   *EdgeProxy
	route  RouteInfo
	status Status
	owner  bool
	closed bool
	mu     sync.Mutex
}

var (
	activeMu     sync.RWMutex
	activeStatus Status
)

func ActiveStatus() Status {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return activeStatus
}

func setActiveStatus(status Status) {
	activeMu.Lock()
	activeStatus = status
	activeMu.Unlock()
}

func BuildConfig(enabled bool, name, tld string, edgePort int, https bool, appPort int, stateDir string, force bool, displayOAuth bool) Config {
	if edgePort <= 0 {
		edgePort = DefaultEdgePort
	}
	if tld == "" {
		tld = "localhost"
	}
	return Config{
		Enabled:      enabled,
		Name:         name,
		TLD:          tld,
		EdgePort:     edgePort,
		HTTPS:        https,
		AppPort:      appPort,
		StateDir:     stateDir,
		Force:        force,
		DisplayOAuth: displayOAuth,
	}
}

func Start(cfg Config, backendHost string, backendPort int) (*Runtime, error) {
	if !cfg.Enabled {
		setActiveStatus(Status{Enabled: false, UpdatedAt: time.Now().UTC()})
		return nil, nil
	}
	if backendPort <= 0 {
		return nil, fmt.Errorf("local routing backend port is invalid")
	}
	if strings.TrimSpace(backendHost) == "" {
		backendHost = "127.0.0.1"
	}
	name := InferRouteName(cfg.Name)
	tld := NormalizeTLD(cfg.TLD)
	host := BuildHost(name, tld)
	store := NewRouteStore(cfg.StateDir, cfg.EdgePort)
	edge := NewEdgeProxy(store, cfg.EdgePort, cfg.HTTPS)
	owner, errStart := edge.StartOrReuse()
	if errStart != nil {
		return nil, errStart
	}
	route, errRegister := store.Register(RouteInfo{
		Name:       name,
		BaseName:   name,
		Host:       host,
		TargetHost: backendHost,
		TargetPort: backendPort,
		PID:        os.Getpid(),
		Command:    strings.Join(os.Args, " "),
	}, cfg.Force)
	if errRegister != nil {
		if owner {
			_ = edge.Stop(context.Background())
		}
		return nil, errRegister
	}
	status := Status{
		Enabled:      true,
		URL:          BuildURL(cfg.HTTPS, host, cfg.EdgePort),
		Name:         name,
		Host:         host,
		TLD:          tld,
		EdgePort:     cfg.EdgePort,
		BackendHost:  backendHost,
		BackendPort:  backendPort,
		HTTPS:        cfg.HTTPS,
		StateDir:     store.StateDir(),
		UpdatedAt:    time.Now().UTC(),
		EdgeOwnerPID: 0,
	}
	if cfg.HTTPS {
		status.CACertPath = edge.certs.CAPath()
		status.TrustCommand = TrustInstallCommand(status.CACertPath)
	}
	if owner {
		status.EdgeOwnerPID = os.Getpid()
	}
	setActiveStatus(status)
	return &Runtime{store: store, edge: edge, route: route, status: status, owner: owner}, nil
}

func (r *Runtime) Status() Status {
	if r == nil {
		return ActiveStatus()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *Runtime) ListRoutes() ([]RouteInfo, error) {
	if r == nil || r.store == nil {
		status := ActiveStatus()
		store := NewRouteStore(status.StateDir, status.EdgePort)
		return store.List()
	}
	return r.store.List()
}

func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	store := r.store
	edge := r.edge
	route := r.route
	owner := r.owner
	r.mu.Unlock()

	if store != nil {
		_ = store.Unregister(route.Host, os.Getpid())
	}
	if owner && edge != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		_ = edge.Stop(ctx)
	}
	setActiveStatus(Status{Enabled: false, UpdatedAt: time.Now().UTC()})
	return nil
}

func InferRouteName(override string) string {
	if trimmed := SanitizeLabel(override); strings.TrimSpace(override) != "" {
		return trimmed
	}
	cwd, errCwd := os.Getwd()
	base := "cliproxyapi"
	if errCwd == nil {
		base = SanitizeLabel(filepath.Base(cwd))
	}
	prefix := inferWorktreePrefix()
	if prefix == "" {
		return base
	}
	return SanitizeLabel(prefix + "-" + base)
}

func inferWorktreePrefix() string {
	gitDir := strings.TrimSpace(runGit("rev-parse", "--git-dir"))
	commonDir := strings.TrimSpace(runGit("rev-parse", "--git-common-dir"))
	if gitDir == "" || commonDir == "" {
		return ""
	}
	absGitDir, errGitDir := filepath.Abs(gitDir)
	absCommonDir, errCommon := filepath.Abs(commonDir)
	if errGitDir != nil || errCommon != nil {
		return ""
	}
	if absGitDir == absCommonDir {
		return ""
	}
	if !strings.Contains(absGitDir, string(filepath.Separator)+"worktrees"+string(filepath.Separator)) {
		return ""
	}
	branch := strings.TrimSpace(runGit("rev-parse", "--abbrev-ref", "HEAD"))
	if branch == "" || branch == "HEAD" {
		return ""
	}
	return SanitizeLabel(branch)
}

func runGit(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func LoadStatusFromConfig(cfg Config) Status {
	status := ActiveStatus()
	if status.Enabled {
		return status
	}
	edgePort := cfg.EdgePort
	if edgePort <= 0 {
		edgePort = DefaultEdgePort
	}
	stateDir := ResolveStateDir(cfg.StateDir, edgePort)
	host := BuildHost(InferRouteName(cfg.Name), cfg.TLD)
	return Status{
		Enabled:     cfg.Enabled,
		Name:        InferRouteName(cfg.Name),
		Host:        host,
		TLD:         NormalizeTLD(cfg.TLD),
		EdgePort:    edgePort,
		HTTPS:       cfg.HTTPS,
		StateDir:    stateDir,
		URL:         BuildURL(cfg.HTTPS, host, edgePort),
		BackendHost: "127.0.0.1",
		BackendPort: cfg.AppPort,
		UpdatedAt:   time.Now().UTC(),
		CACertPath:  filepath.Join(stateDir, "certs", "ca.pem"),
		TrustCommand: func() string {
			if !cfg.HTTPS {
				return ""
			}
			return TrustInstallCommand(filepath.Join(stateDir, "certs", "ca.pem"))
		}(),
	}
}

func ReadEdgePID(stateDir string) int {
	payload, errRead := os.ReadFile(filepath.Join(stateDir, "edge.pid"))
	if errRead != nil {
		return 0
	}
	pid, errAtoi := strconv.Atoi(strings.TrimSpace(string(payload)))
	if errAtoi != nil || pid <= 0 {
		return 0
	}
	if !processAlive(pid) {
		return 0
	}
	return pid
}
