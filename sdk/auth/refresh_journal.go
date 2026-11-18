package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// RefreshJournalFor snapshots the authoritative directory for this refresh. A
// FileTokenStore may use explicit credential paths without configuring baseDir.
func (s *FileTokenStore) RefreshJournalFor(auth *coreauth.Auth) (codex.RefreshJournal, error) {
	if s == nil {
		return nil, fmt.Errorf("auth filestore: nil refresh journal store")
	}
	dir := s.baseDirSnapshot()
	if dir == "" {
		path, errPath := s.resolveAuthPath(auth)
		if errPath != nil {
			return nil, errPath
		}
		canonical, errCanonical := filepath.EvalSymlinks(path)
		if errCanonical == nil {
			path = canonical
		} else if !errors.Is(errCanonical, os.ErrNotExist) {
			return nil, errCanonical
		}
		dir = filepath.Dir(path)
	}
	absolute, errAbs := filepath.Abs(dir)
	if errAbs != nil {
		return nil, errAbs
	}
	return &codex.FileRefreshJournal{Dir: absolute}, nil
}
