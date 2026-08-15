// Package auth provides authentication functionality for various AI service providers.
// It includes interfaces and implementations for token storage and authentication methods.
package auth

// TokenStorage defines the interface for storing authentication tokens.
// Implementations of this interface should provide methods to persist
// authentication tokens to a file system location.
type TokenStorage interface {
	// SaveTokenToFile persists authentication tokens to the specified file path.
	//
	// Parameters:
	//   - authFilePath: The file path where the authentication tokens should be saved
	//
	// Returns:
	//   - error: An error if the save operation fails, nil otherwise
	SaveTokenToFile(authFilePath string) error
}

// CredentialFingerprintMaterial exposes credential fields to the runtime only
// for one-way revision fingerprinting. Values must never be logged or persisted
// outside their existing token storage.
type CredentialFingerprintMaterial struct {
	APIKey       string
	AccessToken  string
	RefreshToken string
	IDToken      string
	Opaque       string
}

// CredentialFingerprintSource is an optional token-storage capability used to
// compare storage-backed and metadata-backed revisions consistently.
type CredentialFingerprintSource interface {
	CredentialFingerprintMaterial() CredentialFingerprintMaterial
}

// CredentialSnapshotSource lets mutable token storage return its serialized
// data, host metadata, and fingerprint material from one generation.
type CredentialSnapshotSource interface {
	CredentialSnapshot() ([]byte, map[string]any, CredentialFingerprintMaterial)
}

// CredentialPersistenceSnapshotSource lets mutable token storage detach an
// independent copy before persistence runs outside the auth-manager lock.
type CredentialPersistenceSnapshotSource interface {
	CredentialPersistenceSnapshot() TokenStorage
}
