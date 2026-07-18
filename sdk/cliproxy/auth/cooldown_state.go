package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// CooldownStateRecord is a persisted runtime cooldown snapshot for one auth/model pair.
type CooldownStateRecord struct {
	Provider       string     `json:"provider,omitempty"`
	AuthID         string     `json:"auth_id"`
	AuthFile       string     `json:"-"`
	Model          string     `json:"model,omitempty"`
	Status         string     `json:"status,omitempty"`
	NextRetryAfter time.Time  `json:"next_retry_after"`
	Reason         string     `json:"reason,omitempty"`
	Quota          QuotaState `json:"quota,omitempty"`
	LastError      *Error     `json:"last_error,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// CooldownStateStore persists runtime cooldown state independently from auth tokens.
type CooldownStateStore interface {
	Load(context.Context) ([]CooldownStateRecord, error)
	Save(context.Context, []CooldownStateRecord) error
}

type cooldownStateFile struct {
	Version   int                   `json:"version"`
	AuthID    string                `json:"auth_id,omitempty"`
	Provider  string                `json:"provider,omitempty"`
	UpdatedAt time.Time             `json:"updated_at"`
	Records   []CooldownStateRecord `json:"records"`
}

// FileCooldownStateStore stores cooldown state as one .cds file per auth.
type FileCooldownStateStore struct {
	mu      sync.Mutex
	dir     string
	authDir string
}

// NewFileCooldownStateStore creates a file-backed cooldown state store rooted at dir.
func NewFileCooldownStateStore(dir string) *FileCooldownStateStore {
	return NewFileCooldownStateStoreWithAuthDir(dir, "")
}

// NewFileCooldownStateStoreWithAuthDir creates a store and derives per-auth .cds
// paths from auth files relative to authDir when possible.
func NewFileCooldownStateStoreWithAuthDir(dir, authDir string) *FileCooldownStateStore {
	return &FileCooldownStateStore{
		dir:     strings.TrimSpace(dir),
		authDir: strings.TrimSpace(authDir),
	}
}

// Load reads all cooldown state files. A missing directory is treated as empty state.
func (s *FileCooldownStateStore) Load(ctx context.Context) ([]CooldownStateRecord, error) {
	if s == nil || s.dir == "" {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, errCtx
	}

	records := make([]CooldownStateRecord, 0)
	errWalk := filepath.WalkDir(s.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry == nil || entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".cds") {
			return nil
		}
		fileRecords, errRead := readCooldownStateFile(ctx, path)
		if errRead != nil {
			return errRead
		}
		records = append(records, fileRecords...)
		return nil
	})
	if errWalk != nil {
		if errors.Is(errWalk, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cooldown state directory: %w", errWalk)
	}
	return records, nil
}

func readCooldownStateFile(ctx context.Context, path string) ([]CooldownStateRecord, error) {
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, errCtx
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if errors.Is(errRead, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cooldown state %s: %w", path, errRead)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var envelope cooldownStateFile
	if errUnmarshal := json.Unmarshal(data, &envelope); errUnmarshal != nil {
		return nil, fmt.Errorf("parse cooldown state %s: %w", path, errUnmarshal)
	}
	return envelope.Records, nil
}

// Save atomically writes one cooldown state file per auth and removes stale files.
func (s *FileCooldownStateStore) Save(ctx context.Context, records []CooldownStateRecord) error {
	if s == nil || s.dir == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	groups := make(map[string][]CooldownStateRecord)
	for _, record := range records {
		authID := strings.TrimSpace(record.AuthID)
		if authID == "" {
			continue
		}
		path, errPath := s.statePath(record)
		if errPath != nil {
			return errPath
		}
		groups[path] = append(groups[path], record)
	}

	if len(groups) == 0 {
		return s.removeAllStateFiles(ctx)
	}
	if errMkdir := os.MkdirAll(s.dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create cooldown state directory: %w", errMkdir)
	}

	desired := make(map[string]struct{}, len(groups))
	for path, groupedRecords := range groups {
		if errSave := writeCooldownStateGroup(ctx, path, groupedRecords); errSave != nil {
			return errSave
		}
		desired[filepath.Clean(path)] = struct{}{}
	}
	return s.removeStaleStateFiles(ctx, desired)
}

func writeCooldownStateGroup(ctx context.Context, path string, records []CooldownStateRecord) error {
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Model < records[j].Model
	})
	envelope := cooldownStateFile{
		Version:   1,
		UpdatedAt: time.Now().UTC(),
		Records:   records,
	}
	if len(records) > 0 {
		envelope.AuthID = records[0].AuthID
		envelope.Provider = records[0].Provider
	}
	data, errMarshal := json.MarshalIndent(envelope, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal cooldown state: %w", errMarshal)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create cooldown state directory: %w", errMkdir)
	}

	tmpFile, errCreate := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create cooldown state temp file: %w", errCreate)
	}
	tmp := tmpFile.Name()
	if _, errWrite := tmpFile.Write(data); errWrite != nil {
		if errClose := tmpFile.Close(); errClose != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("write cooldown state temp file: %w; close temp file: %v", errWrite, errClose)
		}
		_ = os.Remove(tmp)
		return fmt.Errorf("write cooldown state temp file: %w", errWrite)
	}
	if errClose := tmpFile.Close(); errClose != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close cooldown state temp file: %w", errClose)
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace cooldown state file: %w", errRename)
	}
	return nil
}

func (s *FileCooldownStateStore) removeAllStateFiles(ctx context.Context) error {
	return s.removeStaleStateFiles(ctx, nil)
}

func (s *FileCooldownStateStore) removeStaleStateFiles(ctx context.Context, desired map[string]struct{}) error {
	errWalk := filepath.WalkDir(s.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		if entry == nil || entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".cds") {
			return nil
		}
		if desired != nil {
			if _, ok := desired[filepath.Clean(path)]; ok {
				return nil
			}
		}
		if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return fmt.Errorf("remove stale cooldown state %s: %w", path, errRemove)
		}
		return nil
	})
	if errWalk != nil && !errors.Is(errWalk, os.ErrNotExist) {
		return fmt.Errorf("clean cooldown state directory: %w", errWalk)
	}
	return nil
}

func (s *FileCooldownStateStore) statePath(record CooldownStateRecord) (string, error) {
	rel := s.stateRelativePath(record)
	if rel == "" {
		return "", fmt.Errorf("cooldown state path: missing auth identity")
	}
	return filepath.Join(s.dir, rel), nil
}

func (s *FileCooldownStateStore) stateRelativePath(record CooldownStateRecord) string {
	authFile := strings.TrimSpace(record.AuthFile)
	if authFile != "" {
		if filepath.IsAbs(authFile) && strings.TrimSpace(s.authDir) != "" {
			if rel, errRel := filepath.Rel(s.authDir, authFile); errRel == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".." {
				return cdsPathForRel(rel)
			}
		}
		if !filepath.IsAbs(authFile) {
			return cdsPathForRel(authFile)
		}
		return sanitizeCooldownFileName(filepath.Base(authFile))
	}
	return sanitizeCooldownFileName(strings.TrimSpace(record.AuthID))
}

func cdsPathForRel(rel string) string {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return ""
	}
	dir := filepath.Dir(clean)
	base := sanitizeCooldownFileName(filepath.Base(clean))
	if base == "" {
		return ""
	}
	if dir == "." {
		return base
	}
	return filepath.Join(dir, base)
}

var cooldownFileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeCooldownFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	ext := filepath.Ext(name)
	if ext != "" {
		name = strings.TrimSuffix(name, ext)
	}
	name = cooldownFileNameUnsafe.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if name == "" {
		return ""
	}
	return name + ".cds"
}

func cooldownAuthFile(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if path := strings.TrimSpace(auth.Attributes["path"]); path != "" {
			return path
		}
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		return fileName
	}
	return ""
}
