package kiro

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/http"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Fingerprint holds multi-dimensional fingerprint data for runtime request disguise.
type Fingerprint struct {
	OIDCSDKVersion      string // 3.7xx (AWS SDK JS)
	RuntimeSDKVersion   string // 1.0.x (runtime API)
	StreamingSDKVersion string // 1.0.x (streaming API)
	OSType              string // darwin/windows/linux
	OSVersion           string
	NodeVersion         string
	KiroVersion         string
	KiroHash            string // SHA256
}

// FingerprintConfig holds external fingerprint overrides.
type FingerprintConfig struct {
	OIDCSDKVersion      string
	RuntimeSDKVersion   string
	StreamingSDKVersion string
	OSType              string
	OSVersion           string
	NodeVersion         string
	KiroVersion         string
	KiroHash            string
}

// FingerprintManager manages per-account fingerprint generation and caching.
type FingerprintManager struct {
	mu           sync.RWMutex
	fingerprints map[string]*Fingerprint // tokenKey -> fingerprint
	rng          *rand.Rand
	config       *FingerprintConfig // External config (Optional)
}

var (
	// SDK versions
	oidcSDKVersions = []string{
		"3.980.0", "3.975.0", "3.972.0", "3.808.0",
		"3.738.0", "3.737.0", "3.736.0", "3.735.0",
	}
	// SDKVersions for getUsageLimits/ListAvailableModels/GetProfile (runtime API)
	runtimeSDKVersions = []string{"1.0.0"}
	// SDKVersions for generateAssistantResponse (streaming API)
	streamingSDKVersions = []string{"1.0.27"}
	// Valid OS types
	osTypes = []string{"darwin", "windows", "linux"}
	// OS versions
	osVersions = map[string][]string{
		"darwin":  {"25.2.0", "25.1.0", "25.0.0", "24.5.0", "24.4.0", "24.3.0"},
		"windows": {"10.0.26200", "10.0.26100", "10.0.22631", "10.0.22621", "10.0.19045"},
		"linux":   {"6.12.0", "6.11.0", "6.8.0", "6.6.0", "6.5.0", "6.1.0"},
	}
	// Node versions
	nodeVersions = []string{
		"22.21.1", "22.21.0", "22.20.0", "22.19.0", "22.18.0",
		"20.18.0", "20.17.0", "20.16.0",
	}
	// Kiro IDE versions
	kiroVersions = []string{
		"0.10.32", "0.10.16", "0.10.10",
		"0.9.47", "0.9.40", "0.9.2",
		"0.8.206", "0.8.140", "0.8.135", "0.8.86",
	}
	// Global singleton
	globalFingerprintManager     *FingerprintManager
	globalFingerprintManagerOnce sync.Once
)

func GlobalFingerprintManager() *FingerprintManager {
	globalFingerprintManagerOnce.Do(func() {
		globalFingerprintManager = NewFingerprintManager()
	})
	return globalFingerprintManager
}

func SetGlobalFingerprintConfig(cfg *FingerprintConfig) {
	GlobalFingerprintManager().SetConfig(cfg)
}

// SetConfig applies the config and clears the fingerprint cache.
func (fm *FingerprintManager) SetConfig(cfg *FingerprintConfig) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.config = cfg
	// Clear cached fingerprints so they regenerate with the new config
	fm.fingerprints = make(map[string]*Fingerprint)
}

func NewFingerprintManager() *FingerprintManager {
	return &FingerprintManager{
		fingerprints: make(map[string]*Fingerprint),
		rng:          rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// GetFingerprint returns the fingerprint for tokenKey, creating one if it doesn't exist.
func (fm *FingerprintManager) GetFingerprint(tokenKey string) *Fingerprint {
	fm.mu.RLock()
	if fp, exists := fm.fingerprints[tokenKey]; exists {
		fm.mu.RUnlock()
		return fp
	}
	fm.mu.RUnlock()

	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fp, exists := fm.fingerprints[tokenKey]; exists {
		return fp
	}

	fp := fm.generateFingerprint(tokenKey)
	fm.fingerprints[tokenKey] = fp
	return fp
}

func (fm *FingerprintManager) generateFingerprint(tokenKey string) *Fingerprint {
	if fm.config != nil {
		return fm.generateFromConfig(tokenKey)
	}
	return fm.generateRandom(tokenKey)
}

// generateFromConfig uses config values, falling back to random for empty fields.
func (fm *FingerprintManager) generateFromConfig(tokenKey string) *Fingerprint {
	cfg := fm.config

	// Helper: config value or random selection
	configOrRandom := func(configVal string, choices []string) string {
		if configVal != "" {
			return configVal
		}
		return choices[fm.rng.Intn(len(choices))]
	}

	osType := cfg.OSType
	if osType == "" {
		osType = runtime.GOOS
		if !slices.Contains(osTypes, osType) {
			osType = osTypes[fm.rng.Intn(len(osTypes))]
		}
	}

	osVersion := cfg.OSVersion
	if osVersion == "" {
		if versions, ok := osVersions[osType]; ok {
			osVersion = versions[fm.rng.Intn(len(versions))]
		}
	}

	kiroHash := cfg.KiroHash
	if kiroHash == "" {
		hash := sha256.Sum256([]byte(tokenKey))
		kiroHash = hex.EncodeToString(hash[:])
	}

	return &Fingerprint{
		OIDCSDKVersion:      configOrRandom(cfg.OIDCSDKVersion, oidcSDKVersions),
		RuntimeSDKVersion:   configOrRandom(cfg.RuntimeSDKVersion, runtimeSDKVersions),
		StreamingSDKVersion: configOrRandom(cfg.StreamingSDKVersion, streamingSDKVersions),
		OSType:              osType,
		OSVersion:           osVersion,
		NodeVersion:         configOrRandom(cfg.NodeVersion, nodeVersions),
		KiroVersion:         configOrRandom(cfg.KiroVersion, kiroVersions),
		KiroHash:            kiroHash,
	}
}

// generateRandom generates a deterministic fingerprint seeded by accountKey hash.
func (fm *FingerprintManager) generateRandom(accountKey string) *Fingerprint {
	// Use accountKey hash as seed for deterministic random selection
	hash := sha256.Sum256([]byte(accountKey))
	seed := int64(binary.BigEndian.Uint64(hash[:8]))
	rng := rand.New(rand.NewSource(seed))

	osType := runtime.GOOS
	if !slices.Contains(osTypes, osType) {
		osType = osTypes[rng.Intn(len(osTypes))]
	}
	osVersion := osVersions[osType][rng.Intn(len(osVersions[osType]))]

	return &Fingerprint{
		OIDCSDKVersion:      oidcSDKVersions[rng.Intn(len(oidcSDKVersions))],
		RuntimeSDKVersion:   runtimeSDKVersions[rng.Intn(len(runtimeSDKVersions))],
		StreamingSDKVersion: streamingSDKVersions[rng.Intn(len(streamingSDKVersions))],
		OSType:              osType,
		OSVersion:           osVersion,
		NodeVersion:         nodeVersions[rng.Intn(len(nodeVersions))],
		KiroVersion:         kiroVersions[rng.Intn(len(kiroVersions))],
		KiroHash:            hex.EncodeToString(hash[:]),
	}
}

// GenerateAccountKey returns a 16-char hex key derived from SHA256(seed).
func GenerateAccountKey(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(hash[:8])
}

// GetAccountKey derives an account key from clientID > refreshToken > random UUID.
func GetAccountKey(clientID, refreshToken string) string {
	// 1. Prefer ClientID
	if clientID != "" {
		return GenerateAccountKey(clientID)
	}

	// 2. Fallback to RefreshToken
	if refreshToken != "" {
		return GenerateAccountKey(refreshToken)
	}

	// 3. Random fallback
	return GenerateAccountKey(uuid.New().String())
}

// BuildUserAgent format: aws-sdk-js/{SDKVersion} ua/2.1 os/{OSType}#{OSVersion} lang/js md/nodejs#{NodeVersion} api/codewhispererstreaming#{SDKVersion} m/E KiroIDE-{KiroVersion}-{KiroHash}
func (fp *Fingerprint) BuildUserAgent() string {
	return fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/codewhispererstreaming#%s m/E KiroIDE-%s-%s",
		fp.StreamingSDKVersion,
		fp.OSType,
		fp.OSVersion,
		fp.NodeVersion,
		fp.StreamingSDKVersion,
		fp.KiroVersion,
		fp.KiroHash,
	)
}

// BuildAmzUserAgent format: aws-sdk-js/{SDKVersion} KiroIDE-{KiroVersion}-{KiroHash}
func (fp *Fingerprint) BuildAmzUserAgent() string {
	return fmt.Sprintf(
		"aws-sdk-js/%s KiroIDE-%s-%s",
		fp.StreamingSDKVersion,
		fp.KiroVersion,
		fp.KiroHash,
	)
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

func setRuntimeHeaders(req *http.Request, accessToken string, accountKey string) {
	fp := GlobalFingerprintManager().GetFingerprint(accountKey)
	machineID := fp.KiroHash
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s-%s",
		fp.RuntimeSDKVersion, fp.KiroVersion, machineID))
	req.Header.Set("User-Agent", fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/codewhispererruntime#%s m/N,E KiroIDE-%s-%s",
		fp.RuntimeSDKVersion, fp.OSType, fp.OSVersion, fp.NodeVersion, fp.RuntimeSDKVersion,
		fp.KiroVersion, machineID))
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Header.Set("amz-sdk-request", "attempt=1; max=1")
}
