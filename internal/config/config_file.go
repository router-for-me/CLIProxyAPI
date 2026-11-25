package config

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrConfigFileChanged indicates that a path no longer refers to the inode or
// complete contents observed by a config operation. Retrying a write with the
// same replacement would risk losing another writer's changes.
var ErrConfigFileChanged = errors.New("config file changed")

// ErrConfigFileDirectory indicates that the config path refers to a directory.
var ErrConfigFileDirectory = errors.New("config file is a directory")

// ConfigFileSnapshot is an immutable view of an existing config file. It may
// contain unvalidated YAML; use PrepareConfigFileForUploadWithOptional for upload.
type ConfigFileSnapshot struct {
	info os.FileInfo
	data []byte
}

// Bytes returns a copy of the complete file contents, safe to edit independently.
func (s *ConfigFileSnapshot) Bytes() []byte { return bytes.Clone(s.data) }

// SourceHash returns the SHA-256 of the complete file bytes in lowercase hex.
func (s *ConfigFileSnapshot) SourceHash() string { return ComputeSourceHash(s.data) }

// ReadConfigFile is a shared-locked read of the complete file, without validation.
func ReadConfigFile(path string) ([]byte, error) {
	snapshot, errRead := ReadConfigFileSnapshot(path)
	if errRead != nil {
		return nil, errRead
	}
	return snapshot.data, nil
}

// ReadConfigFileSnapshot reads the complete bytes and file identity under a shared lock.
func ReadConfigFileSnapshot(path string) (snapshot *ConfigFileSnapshot, err error) {
	return readConfigFileSnapshot(configFileLocation{path: path})
}

func readConfigFileSnapshot(location configFileLocation) (snapshot *ConfigFileSnapshot, err error) {
	err = location.withFile(configRead, func(f *lockedConfigFile) error {
		snapshot = f.snapshot
		return nil
	})
	return snapshot, err
}

// WriteConfigFileCAS replaces an existing file in place under an exclusive lock.
// It compares both the identity and ALL bytes with expected and never retries on
// conflict. The returned snapshot reflects the committed bytes; it is safe to
// use for a response ETag without reopening the file. It guards against
// orphaned plaintext but does not otherwise validate or normalize replacement.
func WriteConfigFileCAS(path string, expected *ConfigFileSnapshot, replacement []byte) (snapshot *ConfigFileSnapshot, err error) {
	return writeConfigFileCAS(configFileLocation{path: path}, expected, replacement)
}

func writeConfigFileCAS(location configFileLocation, expected *ConfigFileSnapshot, replacement []byte) (snapshot *ConfigFileSnapshot, err error) {
	if expected == nil {
		return nil, fmt.Errorf("config CAS requires an existing snapshot")
	}
	err = location.withFile(configWriteIfPossible, func(f *lockedConfigFile) error {
		if !os.SameFile(f.snapshot.info, expected.info) || !bytes.Equal(f.snapshot.data, expected.data) {
			return ErrConfigFileChanged
		}
		var errWrite error
		snapshot, errWrite = f.replace(replacement)
		return errWrite
	})
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrConfigFileDirectory) {
		return nil, fmt.Errorf("%w: %w", ErrConfigFileChanged, err)
	}
	return snapshot, err
}

// CreateConfigFile publishes a complete snapshot only if the destination does
// not exist, without exposing a partially written configuration to readers.
func CreateConfigFile(path string, data []byte) error {
	return createConfigFile(configFileLocation{path: path}, data)
}

func createConfigFile(location configFileLocation, data []byte) (err error) {
	releaseLocal := lockLocalConfigFile(true)
	defer releaseLocal()
	file, name, err := location.createTemp()
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, file.Close(), location.remove(name))
	}()
	if errLock := lockConfigFile(file, true); errLock != nil {
		return fmt.Errorf("lock new config file: %w", errLock)
	}
	defer func() { err = errors.Join(err, unlockConfigFile(file)) }()
	if errWrite := overwriteConfigFile(file, data, nil); errWrite != nil {
		return errWrite
	}
	if errLink := location.link(name); errLink != nil {
		if errors.Is(errLink, os.ErrExist) {
			return fmt.Errorf("%w: %w", ErrConfigFileChanged, errLink)
		}
		return errLink
	}
	return nil
}

// RemoveConfigFileCAS removes the path only if it still describes expected.
func RemoveConfigFileCAS(path string, expected *ConfigFileSnapshot) error {
	return removeConfigFileCAS(configFileLocation{path: path}, expected)
}

func removeConfigFileCAS(location configFileLocation, expected *ConfigFileSnapshot) error {
	if expected == nil {
		return ErrConfigFileChanged
	}
	err := location.withFile(configRemove, func(f *lockedConfigFile) error {
		if !os.SameFile(f.snapshot.info, expected.info) || !bytes.Equal(f.snapshot.data, expected.data) {
			return ErrConfigFileChanged
		}
		if errSame := f.checkPath(); errSame != nil {
			return errSame
		}
		if errRemove := location.remove(location.path); errRemove != nil {
			return errRemove
		}
		forgetEphemeralServerID(f.snapshot.info)
		return nil
	})
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrConfigFileDirectory) {
		return fmt.Errorf("%w: %w", ErrConfigFileChanged, err)
	}
	return err
}

type configFileMode uint8

const (
	configRead configFileMode = iota
	configWrite
	configRemove
	configWriteIfPossible
)

type lockedConfigFile struct {
	location   configFileLocation
	file       *os.File
	snapshot   *ConfigFileSnapshot
	writeError error
}

// WithConfigFileOpenGate serializes raw filesystem scans with in-process config
// transactions. The callback must not call a config file API itself.
func WithConfigFileOpenGate(scan func() error) error {
	release := lockLocalConfigFile(true)
	defer release()
	return scan()
}

// ReadRootedConfigFileSnapshot reads a repository-relative config without
// allowing symbolic links in any path component.
func ReadRootedConfigFileSnapshot(root *os.Root, path string) (*ConfigFileSnapshot, error) {
	return readConfigFileSnapshot(configFileLocation{root: root, path: path})
}

// ApplyRootedConfigFileCAS checks the original snapshot and applies a rooted
// create, replacement, or deletion (nil data).
func ApplyRootedConfigFileCAS(root *os.Root, path string, expected *ConfigFileSnapshot, data []byte) error {
	location := configFileLocation{root: root, path: path}
	switch {
	case expected == nil && data != nil:
		if errParents := location.parents(true); errParents != nil {
			return errParents
		}
		return createConfigFile(location, data)
	case data != nil:
		_, errWrite := writeConfigFileCAS(location, expected, data)
		return errWrite
	case expected != nil:
		return removeConfigFileCAS(location, expected)
	default:
		return nil
	}
}

type configFileLocation struct {
	root *os.Root
	path string
}

func (l configFileLocation) parents(create bool) error {
	if l.root == nil {
		return nil
	}
	if !filepath.IsLocal(l.path) {
		return fmt.Errorf("invalid rooted config path %q", l.path)
	}
	parent := filepath.Dir(l.path)
	if parent == "." {
		return nil
	}
	var current string
	for part := range strings.SplitSeq(parent, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, errStat := l.root.Lstat(current)
		if create && errors.Is(errStat, os.ErrNotExist) {
			if errMkdir := l.root.Mkdir(current, 0o700); errMkdir != nil && !errors.Is(errMkdir, os.ErrExist) {
				return errMkdir
			}
			info, errStat = l.root.Lstat(current)
		}
		if errStat != nil {
			return errStat
		}
		if !info.IsDir() {
			return fmt.Errorf("config parent %q must be a directory, not a link", current)
		}
	}
	return nil
}

func (l configFileLocation) stat() (os.FileInfo, error) {
	if l.root == nil {
		return os.Stat(l.path)
	}
	if errParents := l.parents(false); errParents != nil {
		return nil, errParents
	}
	return l.root.Lstat(l.path)
}

func (l configFileLocation) open(mode configFileMode) (*os.File, error) {
	if l.root != nil {
		flags := os.O_RDONLY
		if mode != configRead {
			flags = os.O_RDWR
		}
		return l.root.OpenFile(l.path, flags, 0)
	}
	switch mode {
	case configRead:
		return os.Open(l.path)
	case configRemove:
		return openConfigFileForRemoval(l.path)
	default:
		return os.OpenFile(l.path, os.O_RDWR, 0)
	}
}

func (l configFileLocation) createTemp() (*os.File, string, error) {
	if l.root == nil {
		file, err := os.CreateTemp(filepath.Dir(l.path), ".config-*")
		if err != nil {
			return nil, "", err
		}
		return file, file.Name(), nil
	}
	if errParents := l.parents(false); errParents != nil {
		return nil, "", errParents
	}
	name := filepath.Join(filepath.Dir(l.path), ".config-"+rand.Text())
	file, err := l.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	return file, name, err
}

func (l configFileLocation) link(name string) error {
	if l.root == nil {
		return os.Link(name, l.path)
	}
	if errParents := l.parents(false); errParents != nil {
		return errParents
	}
	return l.root.Link(name, l.path)
}

func (l configFileLocation) remove(name string) error {
	if l.root == nil {
		return os.Remove(name)
	}
	if errParents := l.parents(false); errParents != nil {
		return errParents
	}
	return l.root.Remove(name)
}

func withConfigFile(path string, mode configFileMode, use func(*lockedConfigFile) error) error {
	return (configFileLocation{path: path}).withFile(mode, use)
}

func (location configFileLocation) withFile(mode configFileMode, use func(*lockedConfigFile) error) (err error) {
	path := location.path
	// The local gate spans open through close. In particular, never open a second
	// descriptor during a transaction on platforms with process-scoped fcntl locks.
	releaseLocal := lockLocalConfigFile(mode != configRead)
	defer releaseLocal()
	info, errStat := location.stat()
	if errStat != nil {
		return errStat
	}
	if info.IsDir() {
		return &os.PathError{Op: "read config", Path: path, Err: ErrConfigFileDirectory}
	}
	if !info.Mode().IsRegular() {
		return &os.PathError{Op: "read config", Path: path, Err: errors.New("config must be a regular file")}
	}
	var file *os.File
	var writeError error
	if mode != configRead {
		file, writeError = location.open(mode)
		if writeError != nil && (mode != configWriteIfPossible || !isConfigReadOnly(writeError)) {
			return writeError
		}
	}
	if file == nil {
		file, err = location.open(configRead)
		if err != nil {
			return err
		}
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			err = errors.Join(err, errClose)
		}
	}()
	opened, errStat := file.Stat()
	if errStat != nil {
		return errStat
	}
	if !os.SameFile(info, opened) {
		return ErrConfigFileChanged
	}
	if errLock := lockConfigFile(file, mode != configRead && writeError == nil); errLock != nil {
		return fmt.Errorf("lock config file: %w", errLock)
	}
	defer func() {
		if errUnlock := unlockConfigFile(file); errUnlock != nil {
			err = errors.Join(err, fmt.Errorf("unlock config file: %w", errUnlock))
		}
	}()
	f := &lockedConfigFile{location: location, file: file, snapshot: &ConfigFileSnapshot{info: opened}, writeError: writeError}
	if errSame := f.checkPath(); errSame != nil {
		return errSame
	}
	f.snapshot.data, err = io.ReadAll(file)
	if err != nil {
		return err
	}
	if errSame := f.checkPath(); errSame != nil {
		return errSame
	}
	return use(f)
}

func (f *lockedConfigFile) checkPath() error {
	current, errStat := f.location.stat()
	if errStat != nil {
		if errors.Is(errStat, os.ErrNotExist) {
			return fmt.Errorf("%w: %w", ErrConfigFileChanged, errStat)
		}
		return errStat
	}
	if !os.SameFile(current, f.snapshot.info) {
		return ErrConfigFileChanged
	}
	return nil
}

func (f *lockedConfigFile) replace(data []byte) (*ConfigFileSnapshot, error) {
	if errSame := f.checkPath(); errSame != nil {
		return nil, errSame
	}
	if _, errSeek := f.file.Seek(0, io.SeekStart); errSeek != nil {
		return nil, errSeek
	}
	current, errRead := io.ReadAll(f.file)
	if errRead != nil {
		return nil, errRead
	}
	if !bytes.Equal(current, f.snapshot.data) {
		return nil, ErrConfigFileChanged
	}
	if bytes.Equal(current, data) {
		return f.snapshot, nil
	}
	if f.writeError != nil {
		return nil, f.writeError
	}
	if errSame := f.checkPath(); errSame != nil {
		return nil, errSame
	}
	if errValidate := ValidateConfigSave(current, data); errValidate != nil {
		return nil, errValidate
	}
	if errWrite := replaceConfigContents(f.file, current, data, f.checkPath); errWrite != nil {
		return nil, errWrite
	}
	retirePersistedServerID(f.snapshot.info, data)
	return &ConfigFileSnapshot{info: f.snapshot.info, data: bytes.Clone(data)}, nil
}

func replaceConfigContents(file *os.File, original, replacement []byte, check func() error) error {
	if errWrite := overwriteConfigFile(file, replacement, check); errWrite != nil {
		if errRestore := overwriteConfigFile(file, original, nil); errRestore != nil {
			return errors.Join(errWrite, fmt.Errorf("restore config file snapshot: %w", errRestore))
		}
		return errWrite
	}
	return nil
}

func overwriteConfigFile(file *os.File, data []byte, check func() error) error {
	if _, errWrite := file.WriteAt(data, 0); errWrite != nil {
		return errWrite
	}
	if errTruncate := file.Truncate(int64(len(data))); errTruncate != nil {
		return errTruncate
	}
	if errSync := file.Sync(); errSync != nil {
		return errSync
	}
	if check != nil {
		return check()
	}
	return nil
}

func isConfigReadOnly(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EROFS) || isPlatformConfigReadOnly(err)
}
