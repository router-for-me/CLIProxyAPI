package kiro

import (
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestNewFingerprintManager(t *testing.T) {
	fm := NewFingerprintManager()
	if fm == nil {
		t.Fatal("expected non-nil FingerprintManager")
	}
	if fm.fingerprints == nil {
		t.Error("expected non-nil fingerprints map")
	}
	if fm.rng == nil {
		t.Error("expected non-nil rng")
	}
}

func TestGetFingerprint_NewToken(t *testing.T) {
	fm := NewFingerprintManager()
	fp := fm.GetFingerprint("token1")

	if fp == nil {
		t.Fatal("expected non-nil Fingerprint")
	}
	if fp.OIDCSDKVersion == "" {
		t.Error("expected non-empty OIDCSDKVersion")
	}
	if fp.RuntimeSDKVersion == "" {
		t.Error("expected non-empty RuntimeSDKVersion")
	}
	if fp.StreamingSDKVersion == "" {
		t.Error("expected non-empty StreamingSDKVersion")
	}
	if fp.OSType == "" {
		t.Error("expected non-empty OSType")
	}
	if fp.OSVersion == "" {
		t.Error("expected non-empty OSVersion")
	}
	if fp.NodeVersion == "" {
		t.Error("expected non-empty NodeVersion")
	}
	if fp.KiroVersion == "" {
		t.Error("expected non-empty KiroVersion")
	}
	if fp.KiroHash == "" {
		t.Error("expected non-empty KiroHash")
	}
}

func TestGetFingerprint_SameTokenReturnsSameFingerprint(t *testing.T) {
	fm := NewFingerprintManager()
	fp1 := fm.GetFingerprint("token1")
	fp2 := fm.GetFingerprint("token1")

	if fp1 != fp2 {
		t.Error("expected same fingerprint for same token")
	}
}

func TestGetFingerprint_DifferentTokens(t *testing.T) {
	fm := NewFingerprintManager()
	fp1 := fm.GetFingerprint("token1")
	fp2 := fm.GetFingerprint("token2")

	if fp1 == fp2 {
		t.Error("expected different fingerprints for different tokens")
	}
}

func TestBuildUserAgent(t *testing.T) {
	fm := NewFingerprintManager()
	fp := fm.GetFingerprint("token1")

	ua := fp.BuildUserAgent()
	if ua == "" {
		t.Error("expected non-empty User-Agent")
	}

	amzUA := fp.BuildAmzUserAgent()
	if amzUA == "" {
		t.Error("expected non-empty X-Amz-User-Agent")
	}
}

func TestGetFingerprint_OSVersionMatchesOSType(t *testing.T) {
	fm := NewFingerprintManager()

	for i := 0; i < 20; i++ {
		fp := fm.GetFingerprint("token" + string(rune('a'+i)))
		validVersions := osVersions[fp.OSType]
		found := false
		for _, v := range validVersions {
			if v == fp.OSVersion {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("OS version %s not valid for OS type %s", fp.OSVersion, fp.OSType)
		}
	}
}

func TestGenerateFromConfig_OSTypeFromRuntimeGOOS(t *testing.T) {
	fm := NewFingerprintManager()

	// Set config with empty OSType to trigger runtime.GOOS fallback
	fm.SetConfig(&FingerprintConfig{
		OIDCSDKVersion: "3.738.0", // Set other fields to use config path
	})

	fp := fm.GetFingerprint("test-token")

	// Expected OS type based on runtime.GOOS mapping
	var expectedOS string
	switch runtime.GOOS {
	case "darwin":
		expectedOS = "darwin"
	case "windows":
		expectedOS = "windows"
	default:
		expectedOS = "linux"
	}

	if fp.OSType != expectedOS {
		t.Errorf("expected OSType '%s' from runtime.GOOS '%s', got '%s'",
			expectedOS, runtime.GOOS, fp.OSType)
	}
}

func TestFingerprintManager_ConcurrentAccess(t *testing.T) {
	fm := NewFingerprintManager()
	const numGoroutines = 100
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := range numGoroutines {
		go func(id int) {
			defer wg.Done()
			for j := range numOperations {
				tokenKey := "token" + string(rune('a'+id%26))
				switch j % 2 {
				case 0:
					fm.GetFingerprint(tokenKey)
				case 1:
					fp := fm.GetFingerprint(tokenKey)
					_ = fp.BuildUserAgent()
					_ = fp.BuildAmzUserAgent()
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestKiroHashStability(t *testing.T) {
	fm := NewFingerprintManager()

	// Same token should always return same hash
	fp1 := fm.GetFingerprint("token1")
	fp2 := fm.GetFingerprint("token1")
	if fp1.KiroHash != fp2.KiroHash {
		t.Errorf("same token should have same hash: %s vs %s", fp1.KiroHash, fp2.KiroHash)
	}

	// Different tokens should have different hashes
	fp3 := fm.GetFingerprint("token2")
	if fp1.KiroHash == fp3.KiroHash {
		t.Errorf("different tokens should have different hashes")
	}
}

func TestKiroHashFormat(t *testing.T) {
	fm := NewFingerprintManager()
	fp := fm.GetFingerprint("token1")

	if len(fp.KiroHash) != 64 {
		t.Errorf("expected KiroHash length 64 (SHA256 hex), got %d", len(fp.KiroHash))
	}

	for _, c := range fp.KiroHash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("invalid hex character in KiroHash: %c", c)
		}
	}
}

func TestGlobalFingerprintManager(t *testing.T) {
	fm1 := GlobalFingerprintManager()
	fm2 := GlobalFingerprintManager()

	if fm1 == nil {
		t.Fatal("expected non-nil GlobalFingerprintManager")
	}
	if fm1 != fm2 {
		t.Error("expected GlobalFingerprintManager to return same instance")
	}
}

func TestSetOIDCHeaders(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	SetOIDCHeaders(req)

	if req.Header.Get("Content-Type") != "application/json" {
		t.Error("expected Content-Type header to be set")
	}

	amzUA := req.Header.Get("x-amz-user-agent")
	if amzUA == "" {
		t.Error("expected x-amz-user-agent header to be set")
	}
	if !strings.Contains(amzUA, "aws-sdk-js/") {
		t.Errorf("x-amz-user-agent should contain aws-sdk-js: %s", amzUA)
	}
	if !strings.Contains(amzUA, "KiroIDE") {
		t.Errorf("x-amz-user-agent should contain KiroIDE: %s", amzUA)
	}

	ua := req.Header.Get("User-Agent")
	if ua == "" {
		t.Error("expected User-Agent header to be set")
	}
	if !strings.Contains(ua, "api/sso-oidc") {
		t.Errorf("User-Agent should contain api name: %s", ua)
	}

	if req.Header.Get("amz-sdk-invocation-id") == "" {
		t.Error("expected amz-sdk-invocation-id header to be set")
	}
	if req.Header.Get("amz-sdk-request") != "attempt=1; max=4" {
		t.Errorf("unexpected amz-sdk-request header: %s", req.Header.Get("amz-sdk-request"))
	}
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     string
		path         string
		queryParams  map[string]string
		want         string
		wantContains []string
	}{
		{
			name:        "no query params",
			endpoint:    "https://api.example.com",
			path:        "getUsageLimits",
			queryParams: nil,
			want:        "https://api.example.com/getUsageLimits",
		},
		{
			name:        "empty query params",
			endpoint:    "https://api.example.com",
			path:        "getUsageLimits",
			queryParams: map[string]string{},
			want:        "https://api.example.com/getUsageLimits",
		},
		{
			name:     "single query param",
			endpoint: "https://api.example.com",
			path:     "getUsageLimits",
			queryParams: map[string]string{
				"origin": "AI_EDITOR",
			},
			want: "https://api.example.com/getUsageLimits?origin=AI_EDITOR",
		},
		{
			name:     "multiple query params",
			endpoint: "https://api.example.com",
			path:     "getUsageLimits",
			queryParams: map[string]string{
				"origin":       "AI_EDITOR",
				"resourceType": "AGENTIC_REQUEST",
				"profileArn":   "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEF",
			},
			wantContains: []string{
				"https://api.example.com/getUsageLimits?",
				"origin=AI_EDITOR",
				"profileArn=arn%3Aaws%3Acodewhisperer%3Aus-east-1%3A123456789012%3Aprofile%2FABCDEF",
				"resourceType=AGENTIC_REQUEST",
			},
		},
		{
			name:     "omit empty params",
			endpoint: "https://api.example.com",
			path:     "getUsageLimits",
			queryParams: map[string]string{
				"origin":     "AI_EDITOR",
				"profileArn": "",
			},
			want: "https://api.example.com/getUsageLimits?origin=AI_EDITOR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildURL(tt.endpoint, tt.path, tt.queryParams)
			if tt.want != "" {
				if got != tt.want {
					t.Errorf("buildURL() = %v, want %v", got, tt.want)
				}
			}
			if tt.wantContains != nil {
				for _, substr := range tt.wantContains {
					if !strings.Contains(got, substr) {
						t.Errorf("buildURL() = %v, want to contain %v", got, substr)
					}
				}
			}
		})
	}
}

func TestBuildUserAgentFormat(t *testing.T) {
	fm := NewFingerprintManager()
	fp := fm.GetFingerprint("token1")

	ua := fp.BuildUserAgent()
	requiredParts := []string{
		"aws-sdk-js/",
		"ua/2.1",
		"os/",
		"lang/js",
		"md/nodejs#",
		"api/codewhispererstreaming#",
		"m/E",
		"KiroIDE-",
	}
	for _, part := range requiredParts {
		if !strings.Contains(ua, part) {
			t.Errorf("User-Agent missing required part %q: %s", part, ua)
		}
	}
}

func TestBuildAmzUserAgentFormat(t *testing.T) {
	fm := NewFingerprintManager()
	fp := fm.GetFingerprint("token1")

	amzUA := fp.BuildAmzUserAgent()
	requiredParts := []string{
		"aws-sdk-js/",
		"KiroIDE-",
	}
	for _, part := range requiredParts {
		if !strings.Contains(amzUA, part) {
			t.Errorf("X-Amz-User-Agent missing required part %q: %s", part, amzUA)
		}
	}

	// Amz-User-Agent should be shorter than User-Agent
	ua := fp.BuildUserAgent()
	if len(amzUA) >= len(ua) {
		t.Error("X-Amz-User-Agent should be shorter than User-Agent")
	}
}

func TestSetRuntimeHeaders(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	accessToken := "test-access-token-1234567890"
	clientID := "test-client-id-12345"
	accountKey := GenerateAccountKey(clientID)
	fp := GlobalFingerprintManager().GetFingerprint(accountKey)
	machineID := fp.KiroHash

	setRuntimeHeaders(req, accessToken, accountKey)

	// Check Authorization header
	if req.Header.Get("Authorization") != "Bearer "+accessToken {
		t.Errorf("expected Authorization header 'Bearer %s', got '%s'", accessToken, req.Header.Get("Authorization"))
	}

	// Check x-amz-user-agent header
	amzUA := req.Header.Get("x-amz-user-agent")
	if amzUA == "" {
		t.Error("expected x-amz-user-agent header to be set")
	}
	if !strings.Contains(amzUA, "aws-sdk-js/") {
		t.Errorf("x-amz-user-agent should contain aws-sdk-js: %s", amzUA)
	}
	if !strings.Contains(amzUA, "KiroIDE-") {
		t.Errorf("x-amz-user-agent should contain KiroIDE: %s", amzUA)
	}
	if !strings.Contains(amzUA, machineID) {
		t.Errorf("x-amz-user-agent should contain machineID: %s", amzUA)
	}

	// Check User-Agent header
	ua := req.Header.Get("User-Agent")
	if ua == "" {
		t.Error("expected User-Agent header to be set")
	}
	if !strings.Contains(ua, "api/codewhispererruntime#") {
		t.Errorf("User-Agent should contain api/codewhispererruntime: %s", ua)
	}
	if !strings.Contains(ua, "m/N,E") {
		t.Errorf("User-Agent should contain m/N,E: %s", ua)
	}

	// Check amz-sdk-invocation-id (should be a UUID)
	invocationID := req.Header.Get("amz-sdk-invocation-id")
	if invocationID == "" {
		t.Error("expected amz-sdk-invocation-id header to be set")
	}
	if len(invocationID) != 36 {
		t.Errorf("expected amz-sdk-invocation-id to be UUID (36 chars), got %d", len(invocationID))
	}

	// Check amz-sdk-request
	if req.Header.Get("amz-sdk-request") != "attempt=1; max=1" {
		t.Errorf("unexpected amz-sdk-request header: %s", req.Header.Get("amz-sdk-request"))
	}
}

func TestSDKVersionsAreValid(t *testing.T) {
	// Verify all OIDC SDK versions match expected format (3.xxx.x)
	for _, v := range oidcSDKVersions {
		if !strings.HasPrefix(v, "3.") {
			t.Errorf("OIDC SDK version should start with 3.: %s", v)
		}
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			t.Errorf("OIDC SDK version should have 3 parts: %s", v)
		}
	}

	for _, v := range runtimeSDKVersions {
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			t.Errorf("Runtime SDK version should have 3 parts: %s", v)
		}
	}

	for _, v := range streamingSDKVersions {
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			t.Errorf("Streaming SDK version should have 3 parts: %s", v)
		}
	}
}

func TestKiroVersionsAreValid(t *testing.T) {
	// Verify all Kiro versions match expected format (0.x.xxx)
	for _, v := range kiroVersions {
		if !strings.HasPrefix(v, "0.") {
			t.Errorf("Kiro version should start with 0.: %s", v)
		}
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			t.Errorf("Kiro version should have 3 parts: %s", v)
		}
	}
}

func TestNodeVersionsAreValid(t *testing.T) {
	// Verify all Node versions match expected format (xx.xx.x)
	for _, v := range nodeVersions {
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			t.Errorf("Node version should have 3 parts: %s", v)
		}
		// Should be Node 20.x or 22.x
		if !strings.HasPrefix(v, "20.") && !strings.HasPrefix(v, "22.") {
			t.Errorf("Node version should be 20.x or 22.x LTS: %s", v)
		}
	}
}

func TestFingerprintManager_SetConfig(t *testing.T) {
	fm := NewFingerprintManager()

	// Without config, should generate random fingerprint
	fp1 := fm.GetFingerprint("token1")
	if fp1 == nil {
		t.Fatal("expected non-nil fingerprint")
	}

	// Set config with all fields
	cfg := &FingerprintConfig{
		OIDCSDKVersion:      "3.999.0",
		RuntimeSDKVersion:   "9.9.9",
		StreamingSDKVersion: "8.8.8",
		OSType:              "darwin",
		OSVersion:           "99.0.0",
		NodeVersion:         "99.99.99",
		KiroVersion:         "9.9.999",
		KiroHash:            "customhash123",
	}
	fm.SetConfig(cfg)

	// After setting config, should use config values
	fp2 := fm.GetFingerprint("token2")
	if fp2.OIDCSDKVersion != "3.999.0" {
		t.Errorf("expected OIDCSDKVersion '3.999.0', got '%s'", fp2.OIDCSDKVersion)
	}
	if fp2.RuntimeSDKVersion != "9.9.9" {
		t.Errorf("expected RuntimeSDKVersion '9.9.9', got '%s'", fp2.RuntimeSDKVersion)
	}
	if fp2.StreamingSDKVersion != "8.8.8" {
		t.Errorf("expected StreamingSDKVersion '8.8.8', got '%s'", fp2.StreamingSDKVersion)
	}
	if fp2.OSType != "darwin" {
		t.Errorf("expected OSType 'darwin', got '%s'", fp2.OSType)
	}
	if fp2.OSVersion != "99.0.0" {
		t.Errorf("expected OSVersion '99.0.0', got '%s'", fp2.OSVersion)
	}
	if fp2.NodeVersion != "99.99.99" {
		t.Errorf("expected NodeVersion '99.99.99', got '%s'", fp2.NodeVersion)
	}
	if fp2.KiroVersion != "9.9.999" {
		t.Errorf("expected KiroVersion '9.9.999', got '%s'", fp2.KiroVersion)
	}
	if fp2.KiroHash != "customhash123" {
		t.Errorf("expected KiroHash 'customhash123', got '%s'", fp2.KiroHash)
	}
}

func TestFingerprintManager_SetConfig_PartialFields(t *testing.T) {
	fm := NewFingerprintManager()

	// Set config with only some fields
	cfg := &FingerprintConfig{
		KiroVersion: "1.2.345",
		KiroHash:    "myhash",
		// Other fields empty - should use random
	}
	fm.SetConfig(cfg)

	fp := fm.GetFingerprint("token1")

	// Configured fields should use config values
	if fp.KiroVersion != "1.2.345" {
		t.Errorf("expected KiroVersion '1.2.345', got '%s'", fp.KiroVersion)
	}
	if fp.KiroHash != "myhash" {
		t.Errorf("expected KiroHash 'myhash', got '%s'", fp.KiroHash)
	}

	// Empty fields should be randomly selected (non-empty)
	if fp.OIDCSDKVersion == "" {
		t.Error("expected non-empty OIDCSDKVersion")
	}
	if fp.OSType == "" {
		t.Error("expected non-empty OSType")
	}
	if fp.NodeVersion == "" {
		t.Error("expected non-empty NodeVersion")
	}
}

func TestFingerprintManager_SetConfig_ClearsCache(t *testing.T) {
	fm := NewFingerprintManager()

	// Get fingerprint before config
	fp1 := fm.GetFingerprint("token1")
	originalHash := fp1.KiroHash

	// Set config
	cfg := &FingerprintConfig{
		KiroHash: "newcustomhash",
	}
	fm.SetConfig(cfg)

	// Same token should now return different fingerprint (cache cleared)
	fp2 := fm.GetFingerprint("token1")
	if fp2.KiroHash == originalHash {
		t.Error("expected cache to be cleared after SetConfig")
	}
	if fp2.KiroHash != "newcustomhash" {
		t.Errorf("expected KiroHash 'newcustomhash', got '%s'", fp2.KiroHash)
	}
}

func TestGenerateAccountKey(t *testing.T) {
	tests := []struct {
		name  string
		seed  string
		check func(t *testing.T, result string)
	}{
		{
			name: "Empty seed",
			seed: "",
			check: func(t *testing.T, result string) {
				if result == "" {
					t.Error("expected non-empty result for empty seed")
				}
				if len(result) != 16 {
					t.Errorf("expected 16 char hex string, got %d chars", len(result))
				}
			},
		},
		{
			name: "Simple seed",
			seed: "test-client-id",
			check: func(t *testing.T, result string) {
				if len(result) != 16 {
					t.Errorf("expected 16 char hex string, got %d chars", len(result))
				}
				// Verify it's valid hex
				for _, c := range result {
					if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
						t.Errorf("invalid hex character: %c", c)
					}
				}
			},
		},
		{
			name: "Same seed produces same result",
			seed: "deterministic-seed",
			check: func(t *testing.T, result string) {
				result2 := GenerateAccountKey("deterministic-seed")
				if result != result2 {
					t.Errorf("same seed should produce same result: %s vs %s", result, result2)
				}
			},
		},
		{
			name: "Different seeds produce different results",
			seed: "seed-one",
			check: func(t *testing.T, result string) {
				result2 := GenerateAccountKey("seed-two")
				if result == result2 {
					t.Errorf("different seeds should produce different results: %s vs %s", result, result2)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateAccountKey(tt.seed)
			tt.check(t, result)
		})
	}
}

func TestGetAccountKey(t *testing.T) {
	tests := []struct {
		name         string
		clientID     string
		refreshToken string
		check        func(t *testing.T, result string)
	}{
		{
			name:         "Priority 1: clientID when both provided",
			clientID:     "client-id-123",
			refreshToken: "refresh-token-456",
			check: func(t *testing.T, result string) {
				expected := GenerateAccountKey("client-id-123")
				if result != expected {
					t.Errorf("expected clientID-based key %s, got %s", expected, result)
				}
			},
		},
		{
			name:         "Priority 2: refreshToken when clientID is empty",
			clientID:     "",
			refreshToken: "refresh-token-789",
			check: func(t *testing.T, result string) {
				expected := GenerateAccountKey("refresh-token-789")
				if result != expected {
					t.Errorf("expected refreshToken-based key %s, got %s", expected, result)
				}
			},
		},
		{
			name:         "Priority 3: random when both empty",
			clientID:     "",
			refreshToken: "",
			check: func(t *testing.T, result string) {
				if len(result) != 16 {
					t.Errorf("expected 16 char key, got %d chars", len(result))
				}
				// Should be different each time (random UUID)
				result2 := GetAccountKey("", "")
				if result == result2 {
					t.Log("warning: random keys are the same (possible but unlikely)")
				}
			},
		},
		{
			name:         "clientID only",
			clientID:     "solo-client-id",
			refreshToken: "",
			check: func(t *testing.T, result string) {
				expected := GenerateAccountKey("solo-client-id")
				if result != expected {
					t.Errorf("expected clientID-based key %s, got %s", expected, result)
				}
			},
		},
		{
			name:         "refreshToken only",
			clientID:     "",
			refreshToken: "solo-refresh-token",
			check: func(t *testing.T, result string) {
				expected := GenerateAccountKey("solo-refresh-token")
				if result != expected {
					t.Errorf("expected refreshToken-based key %s, got %s", expected, result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetAccountKey(tt.clientID, tt.refreshToken)
			tt.check(t, result)
		})
	}
}

func TestGetAccountKey_Deterministic(t *testing.T) {
	// Verify that GetAccountKey produces deterministic results for same inputs
	clientID := "test-client-id-abc"
	refreshToken := "test-refresh-token-xyz"

	// Call multiple times with same inputs
	results := make([]string, 10)
	for i := range 10 {
		results[i] = GetAccountKey(clientID, refreshToken)
	}

	// All results should be identical
	for i := 1; i < 10; i++ {
		if results[i] != results[0] {
			t.Errorf("GetAccountKey should be deterministic: got %s and %s", results[0], results[i])
		}
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	// Verify that fingerprints are deterministic based on accountKey
	fm := NewFingerprintManager()

	accountKey := GenerateAccountKey("test-client-id")

	// Get fingerprint multiple times
	fp1 := fm.GetFingerprint(accountKey)
	fp2 := fm.GetFingerprint(accountKey)

	// Should be the same pointer (cached)
	if fp1 != fp2 {
		t.Error("expected same fingerprint pointer for same key")
	}

	// Create new manager and verify same values
	fm2 := NewFingerprintManager()
	fp3 := fm2.GetFingerprint(accountKey)

	// Values should be identical (deterministic generation)
	if fp1.KiroHash != fp3.KiroHash {
		t.Errorf("KiroHash should be deterministic: %s vs %s", fp1.KiroHash, fp3.KiroHash)
	}
	if fp1.OSType != fp3.OSType {
		t.Errorf("OSType should be deterministic: %s vs %s", fp1.OSType, fp3.OSType)
	}
	if fp1.OSVersion != fp3.OSVersion {
		t.Errorf("OSVersion should be deterministic: %s vs %s", fp1.OSVersion, fp3.OSVersion)
	}
	if fp1.KiroVersion != fp3.KiroVersion {
		t.Errorf("KiroVersion should be deterministic: %s vs %s", fp1.KiroVersion, fp3.KiroVersion)
	}
	if fp1.NodeVersion != fp3.NodeVersion {
		t.Errorf("NodeVersion should be deterministic: %s vs %s", fp1.NodeVersion, fp3.NodeVersion)
	}
}
