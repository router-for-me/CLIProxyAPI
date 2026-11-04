package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestResolveSIWCBindPath(t *testing.T) {
	authDir := t.TempDir()
	fileName := "codex-user@example.com-plus.json"
	fullPath := filepath.Join(authDir, fileName)
	if err := os.WriteFile(fullPath, []byte(`{"type":"codex","access_token":"outer-1","refresh_token":"outer-ref"}`), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	realPath, errReal := filepath.EvalSymlinks(fullPath)
	if errReal != nil {
		t.Fatal(errReal)
	}
	cfg := &config.Config{AuthDir: authDir}

	// 1. Bare filename resolved against cfg.AuthDir
	resolvedBare, errBare := ResolveSIWCBindPath(cfg, fileName)
	if errBare != nil {
		t.Fatalf("ResolveSIWCBindPath(bare) error: %v", errBare)
	}
	if resolvedBare != filepath.Clean(realPath) {
		t.Fatalf("resolvedBare = %q, want %q", resolvedBare, filepath.Clean(realPath))
	}

	// 2. Absolute path used directly
	resolvedAbs, errAbs := ResolveSIWCBindPath(cfg, fullPath)
	if errAbs != nil {
		t.Fatalf("ResolveSIWCBindPath(abs) error: %v", errAbs)
	}
	if resolvedAbs != filepath.Clean(realPath) {
		t.Fatalf("resolvedAbs = %q, want %q", resolvedAbs, filepath.Clean(realPath))
	}

	// 3. Missing file returns error
	if _, errMissing := ResolveSIWCBindPath(cfg, "non-existent.json"); errMissing == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestBindSIWCCredentialToFile(t *testing.T) {
	authDir := t.TempDir()
	fullPath := filepath.Join(authDir, "codex-bind.json")
	initial := `{"type":"codex","access_token":"outer-tok","refresh_token":"outer-ref","email":"u@example.com","plan_type":"plus"}`
	if err := os.WriteFile(fullPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	deviceID1, errDev1 := ensureSIWCDeviceIDFromFile(fullPath)
	if errDev1 != nil {
		t.Fatalf("ensureSIWCDeviceIDFromFile error: %v", errDev1)
	}
	if !codex.IsValidUUID(deviceID1) {
		t.Fatalf("deviceID1 = %q, want valid UUID", deviceID1)
	}

	siwcData := &codex.CodexSIWCTokenData{
		AccessToken:  "siwc-tok-1",
		RefreshToken: "siwc-ref-1",
		IDToken:      "siwc-id-1",
		ClientID:     "oaiapp_bound_1",
		Scope:        codex.SIWCScope,
		Scopes:       []string{"openid", "chatgpt.tokens.use.direct"},
		Expire:       "2026-10-12T10:00:00Z",
		LastRefresh:  "2026-10-12T09:03:00Z",
		DeviceID:     deviceID1,
	}
	if errSave := saveSIWCToCodexCredentialFile(fullPath, siwcData); errSave != nil {
		t.Fatalf("saveSIWCToCodexCredentialFile error: %v", errSave)
	}

	raw, errRead := os.ReadFile(fullPath)
	if errRead != nil {
		t.Fatalf("ReadFile error: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(raw, &saved); errUnmarshal != nil {
		t.Fatalf("Unmarshal error: %v", errUnmarshal)
	}
	if saved["access_token"] != "outer-tok" || saved["refresh_token"] != "outer-ref" || saved["email"] != "u@example.com" {
		t.Fatalf("outer fields not preserved: %#v", saved)
	}
	if saved["use_siwc"] != true {
		t.Fatalf("use_siwc = %v, want true", saved["use_siwc"])
	}
	siwcMap, ok := saved["siwc"].(map[string]any)
	if !ok {
		t.Fatalf("siwc map missing: %#v", saved["siwc"])
	}
	if siwcMap["access_token"] != "siwc-tok-1" || siwcMap["client_id"] != "oaiapp_bound_1" || siwcMap["device_id"] != deviceID1 {
		t.Fatalf("siwcMap unexpected: %#v", siwcMap)
	}

	// Re-binding must reuse the existing device_id from the file's siwc section
	deviceID2, errDev2 := ensureSIWCDeviceIDFromFile(fullPath)
	if errDev2 != nil {
		t.Fatalf("ensureSIWCDeviceIDFromFile second call error: %v", errDev2)
	}
	if deviceID2 != deviceID1 {
		t.Fatalf("deviceID2 = %q, want reused %q", deviceID2, deviceID1)
	}
}

func TestResolveSIWCBindPathRejectsInvalidTargets(t *testing.T) {
	authDir := t.TempDir()
	outsideDir := t.TempDir()
	validJSON := `{"type":"codex","access_token":"access","refresh_token":"refresh"}`
	outside := filepath.Join(outsideDir, "outside.json")
	if errWrite := os.WriteFile(outside, []byte(validJSON), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg := &config.Config{AuthDir: authDir}
	if _, errResolve := ResolveSIWCBindPath(cfg, outside); errResolve == nil {
		t.Fatal("accepted absolute path outside auth-dir")
	}
	link := filepath.Join(authDir, "escape.json")
	if errSymlink := os.Symlink(outside, link); errSymlink != nil {
		t.Fatal(errSymlink)
	}
	if _, errResolve := ResolveSIWCBindPath(cfg, link); errResolve == nil {
		t.Fatal("accepted symlink escaping auth-dir")
	}
	if _, errResolve := ResolveSIWCBindPath(cfg, "../outside.json"); errResolve == nil {
		t.Fatal("accepted relative traversal")
	}
	if _, errResolve := ResolveSIWCBindPath(nil, outside); errResolve == nil {
		t.Fatal("accepted target without configured auth-dir")
	}
	for _, payload := range []string{
		`{"hello":"world"}`, `null`, `[]`,
		`{"access_token":"access","refresh_token":"refresh"}`,
		`{"type":"claude","access_token":"access","refresh_token":"refresh"}`,
		`{"type":"codex"}`, `{"type":"codex","access_token":"access"}`,
		`{"type":"codex","access_token":123,"refresh_token":"refresh"}`,
		`{"type":"codex","access_token":"access","refresh_token":" "}`,
	} {
		t.Run(payload, func(t *testing.T) {
			path := filepath.Join(authDir, "invalid.json")
			if errWrite := os.WriteFile(path, []byte(payload), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if _, errResolve := ResolveSIWCBindPath(cfg, path); errResolve == nil {
				t.Fatal("accepted invalid Codex credential")
			}
			if errSave := saveSIWCToCodexCredentialFile(path, &codex.CodexSIWCTokenData{AccessToken: "siwc"}); errSave == nil {
				t.Fatal("bind save accepted credential replaced during OAuth")
			}
		})
	}
}

func TestResolveSIWCBindPathUsesRealAuthDirectory(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if errMkdir := os.Mkdir(realDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	alias := filepath.Join(dir, "alias")
	if errSymlink := os.Symlink(realDir, alias); errSymlink != nil {
		t.Fatal(errSymlink)
	}
	path := filepath.Join(realDir, "valid.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex","access_token":"access","refresh_token":"refresh"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	expected, errReal := filepath.EvalSymlinks(path)
	if errReal != nil {
		t.Fatal(errReal)
	}
	for _, target := range []string{"valid.json", filepath.Join(alias, "valid.json")} {
		resolved, errResolve := ResolveSIWCBindPath(&config.Config{AuthDir: alias}, target)
		if errResolve != nil || resolved != expected {
			t.Fatalf("resolved = %q, error = %v, want %q", resolved, errResolve, expected)
		}
	}
}
