package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

const authRevisionBytes = 16

func newAuthRevision() (string, error) {
	raw := make([]byte, authRevisionBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth revision: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Revision returns the opaque process-local revision for this auth snapshot.
func (a *Auth) Revision() string {
	if a == nil {
		return ""
	}
	return a.revision
}

// CloneWithoutRevision returns a snapshot suitable for durable-state comparison.
func (a *Auth) CloneWithoutRevision() *Auth {
	clone := a.Clone()
	if clone != nil {
		clone.revision = ""
	}
	return clone
}
