package kiro

import (
	"net/http"
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
	if fp.SDKVersion == "" {
		t.Error("expected non-empty SDKVersion")
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
	if fp.AcceptLanguage == "" {
		t.Error("expected non-empty AcceptLanguage")
	}
	if fp.ScreenResolution == "" {
		t.Error("expected non-empty ScreenResolution")
	}
	if fp.ColorDepth == 0 {
		t.Error("expected non-zero ColorDepth")
	}
	if fp.HardwareConcurrency == 0 {
		t.Error("expected non-zero HardwareConcurrency")
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

func TestRemoveFingerprint(t *testing.T) {
	fm := NewFingerprintManager()
	fm.GetFingerprint("token1")
	if fm.Count() != 1 {
		t.Fatalf("expected count 1, got %d", fm.Count())
	}

	fm.RemoveFingerprint("token1")
	if fm.Count() != 0 {
		t.Errorf("expected count 0, got %d", fm.Count())
	}
}

func TestRemoveFingerprint_NonExistent(t *testing.T) {
	fm := NewFingerprintManager()
	fm.RemoveFingerprint("nonexistent")
	if fm.Count() != 0 {
		t.Errorf("expected count 0, got %d", fm.Count())
	}
}

func TestCount(t *testing.T) {
	fm := NewFingerprintManager()
	if fm.Count() != 0 {
		t.Errorf("expected count 0, got %d", fm.Count())
	}

	fm.GetFingerprint("token1")
	fm.GetFingerprint("token2")
	fm.GetFingerprint("token3")

	if fm.Count() != 3 {
		t.Errorf("expected count 3, got %d", fm.Count())
	}
}

func TestApplyToRequest(t *testing.T) {
	fm := NewFingerprintManager()
	fp := fm.GetFingerprint("token1")

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	fp.ApplyToRequest(req)

	if req.Header.Get("X-Kiro-SDK-Version") != fp.SDKVersion {
		t.Error("X-Kiro-SDK-Version header mismatch")
	}
	if req.Header.Get("X-Kiro-OS-Type") != fp.OSType {
		t.Error("X-Kiro-OS-Type header mismatch")
	}
	if req.Header.Get("X-Kiro-OS-Version") != fp.OSVersion {
		t.Error("X-Kiro-OS-Version header mismatch")
	}
	if req.Header.Get("X-Kiro-Node-Version") != fp.NodeVersion {
		t.Error("X-Kiro-Node-Version header mismatch")
	}
	if req.Header.Get("X-Kiro-Version") != fp.KiroVersion {
		t.Error("X-Kiro-Version header mismatch")
	}
	if req.Header.Get("X-Kiro-Hash") != fp.KiroHash {
		t.Error("X-Kiro-Hash header mismatch")
	}
	if req.Header.Get("Accept-Language") != fp.AcceptLanguage {
		t.Error("Accept-Language header mismatch")
	}
	if req.Header.Get("X-Screen-Resolution") != fp.ScreenResolution {
		t.Error("X-Screen-Resolution header mismatch")
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

func TestFingerprintManager_ConcurrentAccess(t *testing.T) {
	fm := NewFingerprintManager()
	const numGoroutines = 100
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				tokenKey := "token" + string(rune('a'+id%26))
				switch j % 4 {
				case 0:
					fm.GetFingerprint(tokenKey)
				case 1:
					fm.Count()
				case 2:
					fp := fm.GetFingerprint(tokenKey)
					req, _ := http.NewRequest("GET", "http://example.com", nil)
					fp.ApplyToRequest(req)
				case 3:
					fm.RemoveFingerprint(tokenKey)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestKiroHashUniqueness(t *testing.T) {
	fm := NewFingerprintManager()
	hashes := make(map[string]bool)

	for i := 0; i < 100; i++ {
		fp := fm.GetFingerprint("token" + string(rune(i)))
		if hashes[fp.KiroHash] {
			t.Errorf("duplicate KiroHash detected: %s", fp.KiroHash)
		}
		hashes[fp.KiroHash] = true
	}
}

func TestKiroHashFormat(t *testing.T) {
	fm := NewFingerprintManager()
	fp := fm.GetFingerprint("token1")

	if len(fp.KiroHash) != 64 {
		t.Errorf("expected KiroHash length 64 (SHA256 hex), got %d", len(fp.KiroHash))
	}

	for _, c := range fp.KiroHash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("invalid hex character in KiroHash: %c", c)
		}
	}
}
