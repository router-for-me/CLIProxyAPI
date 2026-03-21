package localrouting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type RouteStore struct {
	stateDir   string
	routesPath string
	lockPath   string
	mu         sync.Mutex
}

type routeFile struct {
	Routes map[string]RouteInfo `json:"routes"`
}

func ResolveStateDir(raw string, edgePort int) string {
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		return trimmed
	}
	if home, errHome := os.UserHomeDir(); errHome == nil && strings.TrimSpace(home) != "" && edgePort >= 1024 {
		return filepath.Join(home, ".cliproxyapi", "portless")
	}
	return filepath.Join(os.TempDir(), "cliproxyapi-portless")
}

func NewRouteStore(stateDir string, edgePort int) *RouteStore {
	resolved := ResolveStateDir(stateDir, edgePort)
	return &RouteStore{
		stateDir:   resolved,
		routesPath: filepath.Join(resolved, "routes.json"),
		lockPath:   filepath.Join(resolved, "routes.lock"),
	}
}

func (s *RouteStore) StateDir() string {
	if s == nil {
		return ""
	}
	return s.stateDir
}

func (s *RouteStore) List() ([]RouteInfo, error) {
	if s == nil {
		return nil, fmt.Errorf("route store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []RouteInfo
	err := s.withLock(func() error {
		rf, errRead := s.readRoutesLocked()
		if errRead != nil {
			return errRead
		}
		now := time.Now().UTC()
		changed := false
		for host, route := range rf.Routes {
			if route.PID > 0 && !processAlive(route.PID) {
				delete(rf.Routes, host)
				changed = true
				continue
			}
			if route.UpdatedAt.IsZero() {
				route.UpdatedAt = now
				rf.Routes[host] = route
				changed = true
			}
		}
		if changed {
			if errWrite := s.writeRoutesLocked(rf); errWrite != nil {
				return errWrite
			}
		}
		out = make([]RouteInfo, 0, len(rf.Routes))
		for _, route := range rf.Routes {
			out = append(out, route)
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].Host < out[j].Host
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *RouteStore) Register(route RouteInfo, force bool) (RouteInfo, error) {
	if s == nil {
		return RouteInfo{}, fmt.Errorf("route store is nil")
	}
	if route.Host == "" || route.TargetHost == "" || route.TargetPort <= 0 {
		return RouteInfo{}, fmt.Errorf("invalid route registration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	route.Host = strings.ToLower(strings.TrimSpace(route.Host))
	route.Name = SanitizeLabel(route.Name)
	if route.BaseName == "" {
		route.BaseName = route.Name
	}
	now := time.Now().UTC()

	var final RouteInfo
	err := s.withLock(func() error {
		rf, errRead := s.readRoutesLocked()
		if errRead != nil {
			return errRead
		}
		if existing, ok := rf.Routes[route.Host]; ok {
			if existing.PID > 0 && existing.PID != route.PID && processAlive(existing.PID) && !force {
				return fmt.Errorf("route %s is owned by pid %d", route.Host, existing.PID)
			}
			route.CreatedAt = existing.CreatedAt
			if route.CreatedAt.IsZero() {
				route.CreatedAt = now
			}
		} else if route.CreatedAt.IsZero() {
			route.CreatedAt = now
		}
		route.UpdatedAt = now
		rf.Routes[route.Host] = route
		if errWrite := s.writeRoutesLocked(rf); errWrite != nil {
			return errWrite
		}
		final = route
		return nil
	})
	if err != nil {
		return RouteInfo{}, err
	}
	return final, nil
}

func (s *RouteStore) Unregister(host string, pid int) error {
	if s == nil {
		return nil
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.withLock(func() error {
		rf, errRead := s.readRoutesLocked()
		if errRead != nil {
			return errRead
		}
		existing, ok := rf.Routes[host]
		if !ok {
			return nil
		}
		if pid > 0 && existing.PID > 0 && existing.PID != pid && processAlive(existing.PID) {
			return nil
		}
		delete(rf.Routes, host)
		return s.writeRoutesLocked(rf)
	})
}

func (s *RouteStore) withLock(fn func() error) error {
	if errMk := os.MkdirAll(s.stateDir, 0o755); errMk != nil {
		return fmt.Errorf("create state dir: %w", errMk)
	}
	unlock, errLock := s.acquireFileLock(10 * time.Second)
	if errLock != nil {
		return errLock
	}
	defer unlock()
	return fn()
}

func (s *RouteStore) readRoutesLocked() (*routeFile, error) {
	rf := &routeFile{Routes: map[string]RouteInfo{}}
	payload, errRead := os.ReadFile(s.routesPath)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return rf, nil
		}
		return nil, fmt.Errorf("read routes: %w", errRead)
	}
	if len(payload) == 0 {
		return rf, nil
	}
	if errUnmarshal := json.Unmarshal(payload, rf); errUnmarshal != nil {
		return nil, fmt.Errorf("parse routes: %w", errUnmarshal)
	}
	if rf.Routes == nil {
		rf.Routes = map[string]RouteInfo{}
	}
	return rf, nil
}

func (s *RouteStore) writeRoutesLocked(rf *routeFile) error {
	if rf == nil {
		rf = &routeFile{Routes: map[string]RouteInfo{}}
	}
	if rf.Routes == nil {
		rf.Routes = map[string]RouteInfo{}
	}
	payload, errMarshal := json.MarshalIndent(rf, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal routes: %w", errMarshal)
	}
	tmpPath := s.routesPath + ".tmp"
	if errWrite := os.WriteFile(tmpPath, payload, 0o644); errWrite != nil {
		return fmt.Errorf("write temp routes: %w", errWrite)
	}
	if errRename := os.Rename(tmpPath, s.routesPath); errRename != nil {
		return fmt.Errorf("replace routes: %w", errRename)
	}
	return nil
}

func (s *RouteStore) acquireFileLock(timeout time.Duration) (func(), error) {
	start := time.Now()
	for {
		f, errOpen := os.OpenFile(s.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errOpen == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() {
				_ = os.Remove(s.lockPath)
			}, nil
		}
		if !os.IsExist(errOpen) {
			return nil, fmt.Errorf("acquire lock: %w", errOpen)
		}
		info, errStat := os.Stat(s.lockPath)
		if errStat == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(s.lockPath)
			continue
		}
		if time.Since(start) >= timeout {
			return nil, fmt.Errorf("acquire lock timeout: %s", s.lockPath)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
