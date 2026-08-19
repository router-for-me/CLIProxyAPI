package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Stable identity seeds for fingerprint-profile=claude-code-cli on non-OAuth credentials.
// Real OAuth credentials keep their stored account/device pool; this only fills gaps
// so ApplyClaudeCredentialMetadata can run as the single identity algorithm.
var claudeCLIIdentityNamespace = uuid.MustParse("6ba7b812-9dad-11d1-80b4-00c04fd430c8")

func stableClaudeCLIDeviceID(seed string) string {
	sum := sha256.Sum256([]byte("cpa-claude-code-cli-device|" + seed))
	return hex.EncodeToString(sum[:])
}

func stableClaudeCLIAccountUUID(seed string) string {
	return uuid.NewSHA1(claudeCLIIdentityNamespace, []byte("cpa-claude-code-cli-account|"+seed)).String()
}

// ClaudeCLIAuthIdentitySeed returns a stable credential identity that does not
// rotate with delegated-provider access tokens.
func ClaudeCLIAuthIdentitySeed(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if id := strings.TrimSpace(auth.ID); id != "" {
			return "auth-id|" + id
		}
		if index := strings.TrimSpace(auth.Index); index != "" {
			return "auth-index|" + index
		}
		if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
			return "auth-file|" + fileName
		}
	}
	return ""
}

// PrepareClaudeCLIFingerprintAuth returns the auth object that should receive
// ApplyClaudeCredentialMetadata. Synthesized API-key / delegated-provider
// identity is written to a clone so the shared credential metadata map is not
// mutated on the request path.
func PrepareClaudeCLIFingerprintAuth(auth *cliproxyauth.Auth, seed string, synthesizeMissing bool) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("auth is nil")
	}
	if !synthesizeMissing {
		return auth, nil
	}
	local := auth.Clone()
	if err := EnsureClaudeCLIFingerprintIdentity(local, seed, true); err != nil {
		return nil, err
	}
	return local, nil
}

// EnsureClaudeCLIFingerprintIdentity prepares auth.Metadata so the shared
// ApplyClaudeCredentialMetadata path can run.
//
// When synthesizeMissing is false (real OAuth), this is a no-op: missing account
// or device data must surface as credential errors.
// When synthesizeMissing is true (fingerprint-profile=claude-code-cli on API keys),
// missing account_uuid / device pool are filled with stable values derived from seed.
// Callers that hold a shared Auth must use PrepareClaudeCLIFingerprintAuth instead.
func EnsureClaudeCLIFingerprintIdentity(auth *cliproxyauth.Auth, seed string, synthesizeMissing bool) error {
	if auth == nil {
		return fmt.Errorf("auth is nil")
	}
	if !synthesizeMissing {
		return nil
	}
	seed = strings.TrimSpace(seed)
	if seed == "" {
		seed = "anonymous"
	}
	if ClaudeCredentialAccountUUID(auth) == "" {
		claudeauth.StoreMetadataString(&auth.Metadata, "account_uuid", stableClaudeCLIAccountUUID(seed))
	}
	if !claudeauth.HasCanonicalDeviceIDPool(claudeauth.ReadDeviceIDPool(&auth.Metadata)) {
		claudeauth.StoreDeviceIDPool(&auth.Metadata, []string{stableClaudeCLIDeviceID(seed)})
	}
	if _, _, errPool := claudeauth.EnsureDeviceIDPoolFor(&auth.Metadata); errPool != nil {
		return fmt.Errorf("ensure device pool: %w", errPool)
	}
	return nil
}
