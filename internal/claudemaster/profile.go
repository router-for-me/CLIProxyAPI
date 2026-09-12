package claudemaster

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/sys/unix"
)

var profileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Profile identifies one independent inference login. It never contains credential values.
type Profile struct {
	Name     string
	Provider string
	AuthID   string
	Dir      string
	AuthDir  string
}

type profileMetadata struct {
	Provider string `json:"provider"`
	AuthID   string `json:"auth_id"`
}

// ProfileLock serializes login, execution, and SDK token refresh for one profile.
// Keep it open until the backend and native child have both stopped.
type ProfileLock struct {
	dir  string
	name string
	file *os.File
}

func OpenProfile(name string, create bool) (*ProfileLock, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("cannot locate your home directory")
	}
	for _, dir := range []string{filepath.Join(home, ".local"), filepath.Join(home, ".local", "share")} {
		if err := ownedDirectory(dir, create, false); err != nil {
			return nil, err
		}
	}
	base := filepath.Join(home, ".local", "share", "claude-master")
	if err := ownedDirectory(base, create, true); err != nil {
		return nil, err
	}
	return openProfileAt(filepath.Join(base, "profiles"), name, create)
}

func openProfileAt(root, name string, create bool) (*ProfileLock, error) {
	if !profileName.MatchString(name) {
		return nil, errors.New("profile name must be 1-64 letters, digits, underscores, or hyphens, starting with a letter or digit")
	}
	if err := ownedDirectory(root, create, true); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, name)
	if err := ownedDirectory(dir, create, true); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(dir, "profile.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, errors.New("cannot open profile lock")
	}
	f := os.NewFile(uintptr(fd), "profile.lock")
	if err := privateFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("profile is already in use; wait for its current login or run to finish")
	}
	return &ProfileLock{dir: dir, name: name, file: f}, nil
}

func (l *ProfileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	// Closing the descriptor releases the flock, without replacing its durable inode.
	if err := f.Close(); err != nil {
		return errors.New("could not close profile lock")
	}
	return nil
}

func (l *ProfileLock) Profile() (Profile, error) {
	if l == nil || l.file == nil {
		return Profile{}, errors.New("profile lock is not held")
	}
	current := filepath.Join(l.dir, "current")
	if err := ownedDirectory(current, false, true); err != nil {
		return Profile{}, errors.New("profile is not logged in or its permissions are unsafe")
	}
	f, err := openPrivateFile(filepath.Join(current, "profile.json"))
	if err != nil {
		return Profile{}, err
	}
	defer func() { _ = f.Close() }()
	decoder := json.NewDecoder(io.LimitReader(f, 4097))
	decoder.DisallowUnknownFields()
	var meta profileMetadata
	if decoder.Decode(&meta) != nil || decoder.Decode(new(any)) != io.EOF || !validProvider(meta.Provider) || !validAuthID(meta.AuthID) {
		return Profile{}, errors.New("profile metadata is invalid")
	}
	authDir := filepath.Join(current, "auth")
	if err := validateAuthDirectory(authDir, meta.AuthID); err != nil {
		return Profile{}, err
	}
	return Profile{Name: l.name, Provider: meta.Provider, AuthID: meta.AuthID, Dir: l.dir, AuthDir: authDir}, nil
}

// Login always performs a new provider OAuth flow into an empty private staging directory.
// It never reads or imports the native Claude/Codex login, and never replaces an existing profile.
func (l *ProfileLock) Login(ctx context.Context, provider string, prompt func(string) (string, error)) error {
	if l == nil || l.file == nil {
		return errors.New("profile lock is not held")
	}
	if !validProvider(provider) {
		return errors.New("provider must be claude or codex")
	}
	current := filepath.Join(l.dir, "current")
	if _, err := os.Lstat(current); !os.IsNotExist(err) {
		return errors.New("profile already exists or cannot be inspected; choose a new profile name")
	}
	stage, err := os.MkdirTemp(l.dir, ".login-")
	if err != nil {
		return errors.New("cannot prepare private login storage")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	authDir := filepath.Join(stage, "auth")
	if err := os.Mkdir(authDir, 0o700); err != nil {
		return errors.New("cannot prepare private login storage")
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	// A constrained wrapper refuses provider-derived filenames that could escape this new auth dir.
	manager := sdkauth.NewManager(&loginStore{dir: authDir, store: store}, sdkauth.NewClaudeAuthenticator(), sdkauth.NewCodexAuthenticator())
	options := &sdkauth.LoginOptions{NoBrowser: true, Prompt: prompt}
	if provider == "codex" {
		options.Metadata = map[string]string{"codex_login_mode": "device"}
	}
	cfg := &config.Config{AuthDir: authDir}
	cfg.ProxyURL = "direct"
	record, _, err := manager.Login(ctx, provider, cfg, options)
	if err != nil || record == nil {
		return errors.New("provider login failed; no profile was installed; retry the login command")
	}
	if record.Provider != provider || !validAuthID(record.FileName) {
		return errors.New("provider returned invalid profile metadata")
	}
	if err := validateAuthDirectory(authDir, record.FileName); err != nil {
		return err
	}
	credential, err := openPrivateFile(filepath.Join(authDir, record.FileName))
	if err != nil {
		return err
	}
	errSync := credential.Sync()
	_ = credential.Close()
	if errSync != nil {
		return errors.New("cannot sync the new credential")
	}
	meta := profileMetadata{Provider: provider, AuthID: record.FileName}
	data, err := json.Marshal(meta)
	if err != nil {
		return errors.New("cannot encode profile metadata")
	}
	if err := writePrivateFile(filepath.Join(stage, "profile.json"), append(data, '\n')); err != nil {
		return err
	}
	if err := syncDirectory(authDir); err != nil {
		return err
	}
	if err := syncDirectory(stage); err != nil {
		return err
	}
	// The metadata and auth file become visible together. A crash before this rename leaves only an
	// unselected private staging directory; another login can safely start a fresh attempt.
	if err := os.Rename(stage, current); err != nil {
		return errors.New("cannot commit the new profile")
	}
	if err := syncDirectory(l.dir); err != nil {
		return errors.New("profile saved but durability confirmation failed; inspect before retrying")
	}
	return nil
}

type loginStore struct {
	dir   string
	store *sdkauth.FileTokenStore
}

func (s *loginStore) Save(ctx context.Context, record *coreauth.Auth) (string, error) {
	if record == nil || !validAuthID(record.ID) || !validAuthID(record.FileName) || record.ID != record.FileName {
		return "", errors.New("invalid OAuth credential filename")
	}
	// SDK token storage implementations may create files with broader defaults: reserve 0600 first.
	path := filepath.Join(s.dir, record.FileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", errors.New("cannot reserve OAuth credential file")
	}
	if err := f.Close(); err != nil {
		return "", errors.New("cannot prepare OAuth credential file")
	}
	record.Attributes = nil
	return s.store.Save(ctx, record)
}
func (s *loginStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (s *loginStore) Delete(context.Context, string) error {
	return errors.New("login cannot delete credentials")
}

func validProvider(provider string) bool { return provider == "claude" || provider == "codex" }
func validAuthID(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.Base(id) == id && !strings.ContainsAny(id, "/\\\x00\r\n") && strings.HasSuffix(id, ".json") && len(id) <= 255
}

func validateAuthDirectory(dir, authID string) error {
	if !validAuthID(authID) {
		return errors.New("invalid credential filename")
	}
	if err := ownedDirectory(dir, false, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != authID {
		return errors.New("profile auth directory must contain exactly one credential and no nested files")
	}
	f, err := openPrivateFile(filepath.Join(dir, authID))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 || st.Size() > 1024*1024 {
		return errors.New("credential file size is invalid")
	}
	return nil
}

func ownedDirectory(path string, create, private bool) error {
	// Refuse symlinks anywhere in the path, not merely at its final component.
	abs, err := filepath.Abs(path)
	if err != nil {
		return errors.New("invalid profile directory")
	}
	for p := abs; p != filepath.Dir(p); p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			if os.IsNotExist(err) && p == abs && create {
				continue
			}
			return errors.New("profile directory is missing or cannot be inspected")
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("profile paths must be real directories without symlinks")
		}
	}
	if create {
		if err := os.Mkdir(abs, 0o700); err != nil && !os.IsExist(err) {
			return errors.New("cannot create profile directory")
		}
	}
	st, err := os.Lstat(abs)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("profile directory is missing or unsafe")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Getuid() || st.Mode().Perm()&0o022 != 0 || (private && st.Mode().Perm() != 0o700) {
		return errors.New("profile directories must be owned by you with safe permissions (private directories: 0700)")
	}
	return nil
}

func privateFile(f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return errors.New("cannot inspect private file")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || int(sys.Uid) != os.Getuid() || sys.Nlink != 1 {
		return errors.New("private files must be owned by you, regular, unlinked, and mode 0600")
	}
	return nil
}

func openPrivateFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot safely open private file")
	}
	f := os.NewFile(uintptr(fd), "private file")
	if err := privateFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func writePrivateFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("cannot create private file")
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(data); err != nil {
		return errors.New("cannot write private file")
	}
	if err := f.Sync(); err != nil {
		return errors.New("cannot sync private file")
	}
	return nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open profile directory for durability")
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return errors.New("cannot sync profile directory")
	}
	return nil
}
