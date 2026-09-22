package managementauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"

	AdminUsername = "admin"
	BootstrapFile = "management-admin-initial-password.txt"

	SessionTTL = 12 * time.Hour
)

const (
	storeFileMode     os.FileMode = 0o600
	maxFailedAttempts             = 5
	banDuration                   = 30 * time.Minute
	attemptIdleTTL                = 2 * time.Hour
	maxAttemptEntries             = 4096
	maxSessionEntries             = 10000
	minPasswordLength             = 1
	maxPasswordLength             = 72
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrInvalid      = errors.New("invalid request")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrRateLimited  = errors.New("rate limited")

	usernameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	dummyHash  = []byte("$2a$10$YnxG5n6hHfBgwC9G0G6Kp.LkPjCWUFQy4s7b3Qqg0Ap8Yq5U/Fs0e")
)

type User struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
}

type Store struct {
	mu       sync.Mutex
	path     string
	now      func() time.Time
	users    map[string]accountRecord
	sessions map[string]sessionRecord // keyed by token hash
	attempts map[string]attemptRecord // keyed by client IP
}

type diskStore struct {
	Version int                      `json:"version"`
	Users   map[string]accountRecord `json:"users"`
}

type accountRecord struct {
	Username        string `json:"username"`
	Role            string `json:"role"`
	Disabled        bool   `json:"disabled"`
	PasswordHash    string `json:"password_hash"`
	PasswordVersion uint64 `json:"password_version"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

type sessionRecord struct {
	Username        string
	ExpiresAt       time.Time
	CreatedAt       time.Time
	PasswordVersion uint64
}

type attemptRecord struct {
	Failures     int
	BlockedUntil time.Time
	LastActivity time.Time
}

type UpdateUserRequest struct {
	Disabled *bool
	Password *string
}

func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("management accounts path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: abs, now: time.Now, users: map[string]accountRecord{}, sessions: map[string]sessionRecord{}, attempts: map[string]attemptRecord{}}
	if b, err := os.ReadFile(abs); err == nil {
		var ds diskStore
		if err := json.Unmarshal(b, &ds); err != nil {
			return nil, fmt.Errorf("read management accounts: %w", err)
		}
		for k, u := range ds.Users {
			u.Username = strings.TrimSpace(u.Username)
			if u.Username == "" {
				u.Username = k
			}
			if err := validateUsername(u.Username); err != nil {
				return nil, fmt.Errorf("invalid management account %q", k)
			}
			if u.Role != RoleAdmin && u.Role != RoleUser {
				return nil, fmt.Errorf("invalid management account role for %q", u.Username)
			}
			if u.PasswordHash == "" {
				return nil, fmt.Errorf("missing management account password for %q", u.Username)
			}
			if u.PasswordVersion == 0 {
				u.PasswordVersion = 1
			}
			s.users[u.Username] = u
		}
		if _, ok := s.users[AdminUsername]; !ok {
			return nil, fmt.Errorf("management admin account is missing")
		}
		_ = os.Chmod(abs, storeFileMode)
		return s, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	password, err := randomSecret(32)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	s.users[AdminUsername] = accountRecord{Username: AdminUsername, Role: RoleAdmin, PasswordHash: string(hash), PasswordVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	if err := writeBootstrap(filepath.Join(filepath.Dir(abs), BootstrapFile), password); err != nil {
		_ = os.Remove(abs)
		return nil, err
	}
	return s, nil
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func BootstrapPath(accountStorePath string) string {
	if strings.TrimSpace(accountStorePath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(accountStorePath), BootstrapFile)
}

func (s *Store) Authenticate(session string) (User, bool) {
	if s == nil || session == "" {
		return User{}, false
	}
	h := tokenHash(session)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredSessionsLocked(now)
	rec, ok := s.sessions[h]
	if !ok || now.After(rec.ExpiresAt) {
		delete(s.sessions, h)
		return User{}, false
	}
	u, ok := s.users[rec.Username]
	if !ok || u.Disabled || rec.PasswordVersion != u.PasswordVersion {
		delete(s.sessions, h)
		return User{}, false
	}
	return publicUser(u), true
}

func (s *Store) Login(username, password, clientIP string) (token string, expires time.Time, user User, err error) {
	if s == nil {
		return "", time.Time{}, User{}, ErrUnauthorized
	}
	clientIP = normalizeIP(clientIP)
	now := s.now()
	s.mu.Lock()
	if s.isBlockedLocked(clientIP, now) {
		s.mu.Unlock()
		return "", time.Time{}, User{}, ErrRateLimited
	}
	rec, ok := s.users[strings.TrimSpace(username)]
	storedHash := dummyHash
	if ok {
		storedHash = []byte(rec.PasswordHash)
	}
	s.mu.Unlock()

	bcryptErr := bcrypt.CompareHashAndPassword(storedHash, []byte(password))
	if bcryptErr != nil || !ok || rec.Disabled {
		s.mu.Lock()
		s.recordFailureLocked(clientIP, s.now())
		s.mu.Unlock()
		return "", time.Time{}, User{}, ErrUnauthorized
	}

	tok, err := randomSecret(32)
	if err != nil {
		return "", time.Time{}, User{}, err
	}
	exp := s.now().Add(SessionTTL).UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.users[rec.Username]
	if !ok || current.Disabled || subtle.ConstantTimeCompare([]byte(current.PasswordHash), []byte(rec.PasswordHash)) != 1 {
		s.recordFailureLocked(clientIP, s.now())
		return "", time.Time{}, User{}, ErrUnauthorized
	}
	s.resetFailuresLocked(clientIP)
	s.purgeExpiredSessionsLocked(s.now())
	s.capSessionsLocked()
	s.sessions[tokenHash(tok)] = sessionRecord{Username: current.Username, ExpiresAt: exp, CreatedAt: s.now(), PasswordVersion: current.PasswordVersion}
	return tok, exp, publicUser(current), nil
}

func (s *Store) Logout(session string) bool {
	if s == nil || session == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := tokenHash(session)
	_, ok := s.sessions[h]
	delete(s.sessions, h)
	return ok
}

func (s *Store) ListUsers(actor User) ([]User, error) {
	if actor.Role != RoleAdmin {
		return nil, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	users := make([]User, 0, len(s.users))
	for _, u := range s.users {
		users = append(users, publicUser(u))
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	return users, nil
}

func (s *Store) CreateUser(actor User, username, password string) (User, error) {
	if s == nil {
		return User{}, ErrForbidden
	}
	if actor.Role != RoleAdmin {
		return User{}, ErrForbidden
	}
	username = strings.TrimSpace(username)
	if err := validateUsername(username); err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[username]; exists {
		return User{}, ErrConflict
	}
	rec := accountRecord{Username: username, Role: RoleUser, PasswordHash: string(hash), PasswordVersion: 1, CreatedAt: now, UpdatedAt: now}
	s.users[username] = rec
	if err := s.saveLocked(); err != nil {
		delete(s.users, username)
		return User{}, err
	}
	return publicUser(rec), nil
}

func (s *Store) UpdateUser(actor User, username string, req UpdateUserRequest) (User, error) {
	if s == nil {
		return User{}, ErrForbidden
	}
	if actor.Role != RoleAdmin {
		return User{}, ErrForbidden
	}
	username = strings.TrimSpace(username)
	if username == AdminUsername && req.Disabled != nil && *req.Disabled {
		return User{}, ErrForbidden
	}
	var newHash string
	if req.Password != nil {
		if err := validatePassword(*req.Password); err != nil {
			return User{}, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		if err != nil {
			return User{}, err
		}
		newHash = string(hash)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.users[username]
	if !ok {
		return User{}, ErrNotFound
	}
	old := rec
	if req.Disabled != nil {
		rec.Disabled = *req.Disabled
	}
	passwordChanged := false
	if newHash != "" {
		rec.PasswordHash = newHash
		rec.PasswordVersion++
		passwordChanged = true
	}
	if req.Disabled != nil && *req.Disabled {
		rec.PasswordVersion++
	}
	if rec.PasswordVersion == 0 {
		rec.PasswordVersion = 1
	}
	rec.UpdatedAt = s.now().UTC().Format(time.RFC3339Nano)
	s.users[username] = rec
	if passwordChanged || (req.Disabled != nil && *req.Disabled) {
		s.revokeUserSessionsLocked(username)
	}
	if err := s.saveLocked(); err != nil {
		s.users[username] = old
		return User{}, err
	}
	return publicUser(rec), nil
}

func (s *Store) ChangePassword(actor User, currentPassword, newPassword string) error {
	if s == nil || actor.Username == "" {
		return ErrUnauthorized
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.mu.Lock()
	rec, ok := s.users[actor.Username]
	storedHash := ""
	if ok {
		storedHash = rec.PasswordHash
	}
	s.mu.Unlock()
	if !ok || rec.Disabled || bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(currentPassword)) != nil {
		return ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok = s.users[actor.Username]
	if !ok || rec.Disabled || bcrypt.CompareHashAndPassword([]byte(rec.PasswordHash), []byte(currentPassword)) != nil {
		return ErrUnauthorized
	}
	old := rec
	rec.PasswordHash = string(hash)
	rec.PasswordVersion++
	rec.UpdatedAt = s.now().UTC().Format(time.RFC3339Nano)
	s.users[actor.Username] = rec
	s.revokeUserSessionsLocked(actor.Username)
	if err := s.saveLocked(); err != nil {
		s.users[actor.Username] = old
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	ds := diskStore{Version: 1, Users: s.users}
	b, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".management-accounts-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(storeFileMode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return os.Chmod(s.path, storeFileMode)
}

func writeBootstrap(path, password string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, storeFileMode)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(password + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, storeFileMode)
}

func publicUser(rec accountRecord) User {
	return User{Username: rec.Username, Role: rec.Role, Disabled: rec.Disabled}
}

func validateUsername(username string) error {
	if !usernameRE.MatchString(username) {
		return ErrInvalid
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < minPasswordLength || len(password) > maxPasswordLength {
		return ErrInvalid
	}
	return nil
}

func randomSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func normalizeIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "unknown"
	}
	return ip
}

func (s *Store) isBlockedLocked(ip string, now time.Time) bool {
	s.purgeAttemptsLocked(now)
	a := s.attempts[ip]
	if !a.BlockedUntil.IsZero() && now.Before(a.BlockedUntil) {
		a.LastActivity = now
		s.attempts[ip] = a
		return true
	}
	if !a.BlockedUntil.IsZero() && !now.Before(a.BlockedUntil) {
		delete(s.attempts, ip)
	}
	return false
}

func (s *Store) recordFailureLocked(ip string, now time.Time) {
	s.purgeAttemptsLocked(now)
	a := s.attempts[ip]
	a.Failures++
	a.LastActivity = now
	if a.Failures >= maxFailedAttempts {
		a.Failures = 0
		a.BlockedUntil = now.Add(banDuration)
	}
	s.attempts[ip] = a
	s.capAttemptsLocked()
}

func (s *Store) resetFailuresLocked(ip string) { delete(s.attempts, ip) }

func (s *Store) purgeAttemptsLocked(now time.Time) {
	for ip, a := range s.attempts {
		if !a.BlockedUntil.IsZero() && now.Before(a.BlockedUntil) {
			continue
		}
		if a.LastActivity.IsZero() || now.Sub(a.LastActivity) > attemptIdleTTL {
			delete(s.attempts, ip)
		}
	}
}

func (s *Store) capAttemptsLocked() {
	for len(s.attempts) > maxAttemptEntries {
		var oldestIP string
		var oldest time.Time
		for ip, a := range s.attempts {
			if oldestIP == "" || a.LastActivity.Before(oldest) {
				oldestIP, oldest = ip, a.LastActivity
			}
		}
		delete(s.attempts, oldestIP)
	}
}

func (s *Store) purgeExpiredSessionsLocked(now time.Time) {
	for h, sess := range s.sessions {
		if !now.Before(sess.ExpiresAt) {
			delete(s.sessions, h)
		}
	}
}

func (s *Store) capSessionsLocked() {
	for len(s.sessions) >= maxSessionEntries {
		var oldestHash string
		var oldest time.Time
		for h, sess := range s.sessions {
			if oldestHash == "" || sess.CreatedAt.Before(oldest) {
				oldestHash, oldest = h, sess.CreatedAt
			}
		}
		delete(s.sessions, oldestHash)
	}
}

func (s *Store) revokeUserSessionsLocked(username string) {
	for h, sess := range s.sessions {
		if sess.Username == username {
			delete(s.sessions, h)
		}
	}
}
