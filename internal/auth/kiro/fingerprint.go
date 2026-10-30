package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"
)

type Fingerprint struct {
	OIDCSDKVersion    string
	RuntimeSDKVersion string
	OSType            string
	OSVersion         string
	NodeVersion       string
	KiroVersion       string
	KiroHash          string
}

type FingerprintManager struct {
	mu           sync.RWMutex
	fingerprints map[string]*Fingerprint
}

var (
	globalFingerprintManager     *FingerprintManager
	globalFingerprintManagerOnce sync.Once
)

func GlobalFingerprintManager() *FingerprintManager {
	globalFingerprintManagerOnce.Do(func() {
		globalFingerprintManager = &FingerprintManager{
			fingerprints: make(map[string]*Fingerprint),
		}
	})
	return globalFingerprintManager
}

func (m *FingerprintManager) GetFingerprint(key string) *Fingerprint {
	m.mu.RLock()
	if fp, exists := m.fingerprints[key]; exists {
		m.mu.RUnlock()
		return fp
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	if fp, exists := m.fingerprints[key]; exists {
		return fp
	}

	h := sha256.Sum256([]byte(key))
	hashStr := hex.EncodeToString(h[:])

	fp := &Fingerprint{
		OIDCSDKVersion:    "3.738.0",
		RuntimeSDKVersion: "1.0.0",
		OSType:            "linux",
		OSVersion:         "6.6.0",
		NodeVersion:       "20.11.0",
		KiroVersion:       "0.6.2",
		KiroHash:          hashStr,
	}
	m.fingerprints[key] = fp
	return fp
}

func GetAccountKey(clientID, refreshToken string) string {
	if clientID != "" {
		return clientID
	}
	if refreshToken != "" {
		h := sha256.Sum256([]byte(refreshToken))
		return hex.EncodeToString(h[:16])
	}
	return uuid.New().String()
}

func SetOIDCHeaders(req *http.Request) {
	fp := GlobalFingerprintManager().GetFingerprint("oidc-session")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/%s KiroIDE", fp.OIDCSDKVersion))
	req.Header.Set("User-Agent", fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/%s#%s m/E KiroIDE",
		fp.OIDCSDKVersion, fp.OSType, fp.OSVersion, fp.NodeVersion, "sso-oidc", fp.OIDCSDKVersion))
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Header.Set("amz-sdk-request", "attempt=1; max=4")
}
