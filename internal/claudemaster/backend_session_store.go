package claudemaster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type backendSessionRoute struct {
	Origin    string `json:"origin"`
	Revision  uint64 `json:"revision"`
	Committed bool   `json:"committed"`
}

// Each locked profile keeps a private replica. Session IDs and credentials are
// never written verbatim. Files are read through after cache eviction, without
// a TTL that could turn a resumed opaque payload into a different account.
type backendSessionStore struct {
	dirs         []string
	backupAuthID string
	backupOrigin string
	authOrigins  map[string]string
	writeRoute   func(string, string, []byte) error
}

func newBackendSessionStore(credentials []BackendCredential, auths []*coreauth.Auth, backupAuthID, backupAPIKey string) (*backendSessionStore, error) {
	store := &backendSessionStore{backupAuthID: backupAuthID, authOrigins: make(map[string]string)}
	if backupAuthID != "" {
		store.backupOrigin = "api:" + backendSessionHash(backupAPIKey)
	}
	for _, auth := range auths {
		if auth != nil {
			store.authOrigins[auth.ID] = store.origin(auth)
		}
	}
	for _, credential := range credentials {
		dir := filepath.Clean(credential.AuthDir) + ".routes"
		if err := ownedDirectory(dir, true, true); err != nil {
			return nil, errors.New("cannot open private conversation routing directory")
		}
		if err := syncDirectory(filepath.Dir(dir)); err != nil {
			return nil, errors.New("cannot sync conversation routing directory parent")
		}
		store.dirs = append(store.dirs, dir)
	}
	return store, nil
}

func backendSessionHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *backendSessionStore) origin(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.ID == s.backupAuthID {
		return s.backupOrigin
	}
	if account := helps.ClaudeCredentialAccountUUID(auth); account != "" {
		return "oauth:" + backendSessionHash(auth.Provider+"\x00"+account)
	}
	return ""
}

func (s *backendSessionStore) lookup(sessionID string) (backendSessionRoute, bool, error) {
	route, found, _, _, err := s.lookupReplicas(sessionID)
	return route, found, err
}

func (s *backendSessionStore) lookupReplicas(sessionID string) (backendSessionRoute, bool, bool, bool, error) {
	var newest backendSessionRoute
	found := false
	routes := make([]backendSessionRoute, 0, len(s.dirs))
	for _, dir := range s.dirs {
		if err := ownedDirectory(dir, false, true); err != nil {
			return backendSessionRoute{}, false, false, false, errors.New("conversation routing directory is unsafe")
		}
		path := filepath.Join(dir, backendSessionHash(sessionID)+".json")
		info, errStat := os.Lstat(path)
		if os.IsNotExist(errStat) {
			continue
		}
		if errStat != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
			return backendSessionRoute{}, false, false, false, errors.New("conversation routing record is unsafe")
		}
		file, errOpen := openPrivateFile(path)
		if errOpen != nil {
			return backendSessionRoute{}, false, false, false, errors.New("cannot read private conversation routing record")
		}
		decoder := json.NewDecoder(io.LimitReader(file, 4097))
		decoder.DisallowUnknownFields()
		var route backendSessionRoute
		errDecode := decoder.Decode(&route)
		errTrailing := decoder.Decode(new(any))
		errClose := file.Close()
		if errDecode != nil || errTrailing != io.EOF || errClose != nil || route.Revision == 0 || !validBackendSessionOrigin(route.Origin) {
			return backendSessionRoute{}, false, false, false, errors.New("conversation routing record is invalid")
		}
		if !found || route.Revision > newest.Revision {
			newest, found = route, true
		}
		routes = append(routes, route)
	}
	// A committed replica now attests that this exact upstream origin accepted
	// the request, not that every replica write completed. A subset can trust
	// that evidence; older/pending replicas can be repaired without guessing.
	for _, route := range routes {
		if route.Revision == newest.Revision && route.Origin == newest.Origin && route.Committed {
			newest.Committed = true
		}
	}
	coherent := len(routes) == len(s.dirs)
	for _, route := range routes {
		if route.Revision == newest.Revision && route.Origin != newest.Origin {
			return backendSessionRoute{}, false, false, false, errors.New("conversation routing replicas disagree")
		}
		coherent = coherent && route == newest
	}
	return newest, found, coherent, found && !newest.Committed, nil
}

func validBackendSessionOrigin(origin string) bool {
	_, hash, ok := strings.Cut(origin, ":")
	if !ok || !(strings.HasPrefix(origin, "oauth:") || strings.HasPrefix(origin, "api:")) || len(hash) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(hash)
	return err == nil && len(decoded) == sha256.Size
}

func (s *backendSessionStore) bind(sessionID string, auth *coreauth.Auth) error {
	return s.bindWithProof(sessionID, auth, false)
}

func (s *backendSessionStore) bindWithProof(sessionID string, auth *coreauth.Auth, originProven bool) error {
	origin := s.origin(auth)
	if origin == "" {
		return errors.New("cannot establish conversation origin without a Claude account identity")
	}
	s.authOrigins[auth.ID] = origin
	previous, found, coherent, _, errLookup := s.lookupReplicas(sessionID)
	if errLookup != nil {
		return errLookup
	}
	if found && previous.Origin == origin {
		if coherent {
			return nil
		}
		return s.writeReplicas(sessionID, previous)
	}
	revision := previous.Revision
	if !found || previous.Origin != origin {
		if revision == math.MaxUint64 {
			return errors.New("conversation routing revision is exhausted")
		}
		revision++
	}
	if revision == 0 {
		return errors.New("conversation routing revision is exhausted")
	}
	// Selection is only an intent. An interrupted or rejected upstream request
	// must not make a subset-pool restart trust a new conversation origin.
	return s.writeReplicas(sessionID, backendSessionRoute{Origin: origin, Revision: revision, Committed: originProven})
}

func (s *backendSessionStore) commit(sessionID string, expected backendSessionRoute) error {
	current, found, coherent, _, errLookup := s.lookupReplicas(sessionID)
	if errLookup != nil {
		return errLookup
	}
	// A late response from an older attempt cannot commit a newer handoff,
	// even if the conversation has since returned to the same account.
	if !found || current.Origin != expected.Origin || current.Revision != expected.Revision {
		return nil
	}
	if current.Committed && coherent {
		return nil
	}
	current.Committed = true
	return s.writeReplicas(sessionID, current)
}

func (s *backendSessionStore) writeReplicas(sessionID string, route backendSessionRoute) error {
	raw, errMarshal := json.Marshal(route)
	if errMarshal != nil {
		return errors.New("cannot serialize conversation routing record")
	}
	for _, dir := range s.dirs {
		write := s.writeRoute
		if write == nil {
			write = writeBackendSessionRoute
		}
		if err := write(dir, backendSessionHash(sessionID)+".json", raw); err != nil {
			return err
		}
	}
	return nil
}

func writeBackendSessionRoute(dir, name string, raw []byte) error {
	if err := ownedDirectory(dir, false, true); err != nil {
		return errors.New("conversation routing directory is no longer safe")
	}
	file, errCreate := os.CreateTemp(dir, ".route-*")
	if errCreate != nil {
		return errors.New("cannot stage conversation routing record")
	}
	tempPath := file.Name()
	defer func() { _ = file.Close(); _ = os.Remove(tempPath) }()
	if n, errWrite := file.Write(raw); errWrite != nil || n != len(raw) {
		return errors.New("cannot write conversation routing record")
	}
	if errSync := file.Sync(); errSync != nil {
		return errors.New("cannot sync conversation routing record")
	}
	if errClose := file.Close(); errClose != nil {
		return errors.New("cannot close conversation routing record")
	}
	if errRename := os.Rename(tempPath, filepath.Join(dir, name)); errRename != nil {
		return errors.New("cannot commit conversation routing record")
	}
	directory, errOpen := os.Open(dir)
	if errOpen != nil {
		return errors.New("cannot open conversation routing directory for sync")
	}
	errSync := directory.Sync()
	errClose := directory.Close()
	if errSync != nil || errClose != nil {
		return errors.New("cannot sync conversation routing directory")
	}
	return nil
}
