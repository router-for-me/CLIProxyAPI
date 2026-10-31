package auth

import "path/filepath"

type committedAuthFileSnapshot struct {
	path string
	data string
}

// BindCommittedAuthFile retains remotely committed bytes when the local mirror
// could not be published. Passing empty data clears a previous save's snapshot.
func (a *Auth) BindCommittedAuthFile(path string, data []byte) {
	if a == nil {
		return
	}
	a.committedAuthFile = nil
	if len(data) > 0 {
		a.committedAuthFile = &committedAuthFileSnapshot{path: filepath.Clean(path), data: string(data)}
	}
}

// CommittedAuthFile returns the committed credential for this destination without
// rereading a local mirror that still contains the previous credential.
func (a *Auth) CommittedAuthFile(path string) []byte {
	if a == nil || a.committedAuthFile == nil || a.committedAuthFile.path != filepath.Clean(path) {
		return nil
	}
	return []byte(a.committedAuthFile.data)
}
