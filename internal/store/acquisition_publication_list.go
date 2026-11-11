package store

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func appendPendingAuthFiles(dir string, entries []*cliproxyauth.Auth, read func(string, string) (*cliproxyauth.Auth, error)) ([]*cliproxyauth.Auth, error) {
	paths, err := authpublication.Paths(dir)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(entries))
	for _, auth := range entries {
		seen[auth.Attributes[cliproxyauth.AttributePath]] = true
	}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		auth, err := read(path, dir)
		if err != nil {
			return nil, err
		}
		if auth != nil {
			entries = append(entries, auth)
		}
	}
	return entries, nil
}
