package management

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const testCodexAccountID = "acct-viisoso-1"

func TestSaveCodexOAuthRecordKeepsExistingFreeFilename(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	freeName := codexFileName("user@example.com", "free", testCodexAccountID)
	writeCodexJSON(t, authDir, freeName, map[string]any{
		"type":          "codex",
		"email":         "user@example.com",
		"account_id":    testCodexAccountID,
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"note":          "程辉",
		"priority":      float64(60),
		"disabled":      true,
		"proxy_url":     "socks5://10.80.1.61:2083",
	})

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, ""); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	got := jsonFileNames(t, authDir)
	if !stringSlicesEqual(got, []string{freeName}) {
		t.Fatalf("auth files = %v, want [%s]", got, freeName)
	}

	saved := readJSONMap(t, filepath.Join(authDir, freeName))
	if saved["access_token"] != "new-access" {
		t.Errorf("access_token = %v, want new-access", saved["access_token"])
	}
	if saved["note"] != "程辉" {
		t.Errorf("note = %v, want 程辉", saved["note"])
	}
	if saved["priority"] != float64(60) {
		t.Errorf("priority = %v, want 60", saved["priority"])
	}
	if saved["proxy_url"] != "socks5://10.80.1.61:2083" {
		t.Errorf("proxy_url = %v, want socks5://10.80.1.61:2083", saved["proxy_url"])
	}
	if saved["disabled"] != false {
		t.Errorf("disabled = %v, want false", saved["disabled"])
	}
}

func TestSaveCodexOAuthRecordOverwritesExistingProFile(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	proName := codexFileName("user@example.com", "pro", testCodexAccountID)
	writeCodexJSON(t, authDir, proName, map[string]any{
		"type":          "codex",
		"email":         "user@example.com",
		"account_id":    testCodexAccountID,
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"note":          "keep-me",
		"disabled":      false,
	})

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "newer-access", "newer-refresh")
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, ""); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	got := jsonFileNames(t, authDir)
	if !stringSlicesEqual(got, []string{proName}) {
		t.Fatalf("auth files = %v, want [%s]", got, proName)
	}
	saved := readJSONMap(t, filepath.Join(authDir, proName))
	if saved["access_token"] != "newer-access" {
		t.Errorf("access_token = %v, want newer-access", saved["access_token"])
	}
	if saved["note"] != "keep-me" {
		t.Errorf("note = %v, want keep-me", saved["note"])
	}
}

func TestSaveCodexOAuthRecordIgnoresReplaceWhenAccountIDDiffers(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	otherName := codexFileName("other@example.com", "free", "acct-other")
	writeCodexJSON(t, authDir, otherName, map[string]any{
		"type":       "codex",
		"email":      "other@example.com",
		"account_id": "acct-other",
		"note":       "do-not-touch",
		"disabled":   true,
	})

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, otherName); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	got := jsonFileNames(t, authDir)
	newName := codexFileName("user@example.com", "pro", testCodexAccountID)
	want := []string{otherName, newName}
	sort.Strings(want)
	if !stringSlicesEqual(got, want) {
		t.Fatalf("auth files = %v, want %v", got, want)
	}
	other := readJSONMap(t, filepath.Join(authDir, otherName))
	if other["note"] != "do-not-touch" || other["disabled"] != true {
		t.Errorf("replace target was modified: %#v", other)
	}
}

func TestSaveCodexOAuthRecordCreatesWhenNoExistingAccount(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, ""); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	got := jsonFileNames(t, authDir)
	want := []string{codexFileName("user@example.com", "pro", testCodexAccountID)}
	if !stringSlicesEqual(got, want) {
		t.Fatalf("auth files = %v, want %v", got, want)
	}
}

func TestSaveCodexOAuthRecordCollapsesFreeAndProSiblings(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	freeName := codexFileName("user@example.com", "free", testCodexAccountID)
	proName := codexFileName("user@example.com", "pro", testCodexAccountID)
	writeCodexJSON(t, authDir, freeName, map[string]any{
		"type":       "codex",
		"email":      "user@example.com",
		"account_id": testCodexAccountID,
		"note":       "from-free",
		"priority":   float64(60),
		"disabled":   true,
	})
	writeCodexJSON(t, authDir, proName, map[string]any{
		"type":       "codex",
		"email":      "user@example.com",
		"account_id": testCodexAccountID,
		"note":       "from-pro",
		"disabled":   false,
	})

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, freeName); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	got := jsonFileNames(t, authDir)
	if !stringSlicesEqual(got, []string{freeName}) {
		t.Fatalf("auth files = %v, want [%s]", got, freeName)
	}
	saved := readJSONMap(t, filepath.Join(authDir, freeName))
	if saved["note"] != "from-free" {
		t.Errorf("note = %v, want from-free (replace hint)", saved["note"])
	}
	if saved["priority"] != float64(60) {
		t.Errorf("priority = %v, want 60", saved["priority"])
	}
	if saved["disabled"] != false {
		t.Errorf("disabled = %v, want false", saved["disabled"])
	}
	if saved["access_token"] != "new-access" {
		t.Errorf("access_token = %v, want new-access", saved["access_token"])
	}
}

func TestSaveCodexOAuthRecordClearsDisabledOnSameFilename(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	proName := codexFileName("user@example.com", "pro", testCodexAccountID)
	writeCodexJSON(t, authDir, proName, map[string]any{
		"type":       "codex",
		"email":      "user@example.com",
		"account_id": testCodexAccountID,
		"disabled":   true,
		"note":       "keep",
	})

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, ""); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	saved := readJSONMap(t, filepath.Join(authDir, proName))
	if saved["disabled"] != false {
		t.Errorf("disabled = %v, want false", saved["disabled"])
	}
	if saved["note"] != "keep" {
		t.Errorf("note = %v, want keep", saved["note"])
	}
}

func fakeCodexIDToken(planType string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": testCodexAccountID,
			"chatgpt_plan_type":  planType,
		},
	})
	if err != nil {
		panic(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

func newCodexRecord(email, accountID, plan, access, refresh string) *coreauth.Auth {
	fileName := codexFileName(email, plan, accountID)
	storage := &codex.CodexTokenStorage{
		Type:         "codex",
		Email:        email,
		AccountID:    accountID,
		AccessToken:  access,
		RefreshToken: refresh,
		IDToken:      "id-" + access,
		Expire:       "2026-12-31T23:59:59Z",
		LastRefresh:  "2026-08-23T12:00:00+08:00",
	}
	return &coreauth.Auth{
		ID:       fileName,
		Provider: "codex",
		FileName: fileName,
		Storage:  storage,
		Metadata: map[string]any{
			"email":      email,
			"account_id": accountID,
		},
	}
}

func codexFileName(email, plan, accountID string) string {
	sum := sha256.Sum256([]byte(accountID))
	hash := hex.EncodeToString(sum[:])[:8]
	return codex.CredentialFileName(email, plan, hash, true)
}

func writeCodexJSON(t *testing.T, dir, name string, content map[string]any) {
	t.Helper()
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		t.Fatalf("mkdir %s: %v", dir, errMkdir)
	}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	if err = os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return out
}

func jsonFileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".json" || name[0] == '.' {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestSaveCodexOAuthRecordKeepsNestedRelativePath(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	nestedName := filepath.ToSlash(filepath.Join("team", "codex-user@example.com-free.json"))
	writeCodexJSON(t, filepath.Join(authDir, "team"), "codex-user@example.com-free.json", map[string]any{
		"type":       "codex",
		"email":      "user@example.com",
		"account_id": testCodexAccountID,
		"note":       "nested",
		"disabled":   true,
	})

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, ""); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	if _, errStat := os.Stat(filepath.Join(authDir, filepath.FromSlash(nestedName))); errStat != nil {
		t.Fatalf("expected nested file %s: %v", nestedName, errStat)
	}
	rootNames := jsonFileNames(t, authDir)
	for _, name := range rootNames {
		if strings.HasSuffix(name, ".json") && !strings.Contains(name, string(filepath.Separator)) && name != "team" {
			// jsonFileNames only lists top-level json files
			t.Fatalf("unexpected root auth file %s", name)
		}
	}
	saved := readJSONMap(t, filepath.Join(authDir, filepath.FromSlash(nestedName)))
	if saved["access_token"] != "new-access" {
		t.Errorf("access_token = %v, want new-access", saved["access_token"])
	}
	if saved["note"] != "nested" {
		t.Errorf("note = %v, want nested", saved["note"])
	}
}

func TestSaveCodexOAuthRecordConcurrentReplaceKeepsOneFile(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	freeName := codexFileName("user@example.com", "free", testCodexAccountID)
	proName := codexFileName("user@example.com", "pro", testCodexAccountID)
	writeCodexJSON(t, authDir, freeName, map[string]any{
		"type":       "codex",
		"email":      "user@example.com",
		"account_id": testCodexAccountID,
		"note":       "free-row",
	})
	writeCodexJSON(t, authDir, proName, map[string]any{
		"type":       "codex",
		"email":      "user@example.com",
		"account_id": testCodexAccountID,
		"note":       "pro-row",
	})

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errSave := h.saveCodexOAuthRecord(context.Background(), newCodexRecord("user@example.com", testCodexAccountID, "pro", "from-free", "refresh-free"), freeName)
		errCh <- errSave
	}()
	go func() {
		defer wg.Done()
		_, errSave := h.saveCodexOAuthRecord(context.Background(), newCodexRecord("user@example.com", testCodexAccountID, "pro", "from-pro", "refresh-pro"), proName)
		errCh <- errSave
	}()
	wg.Wait()
	close(errCh)
	for errSave := range errCh {
		if errSave != nil {
			t.Fatalf("saveCodexOAuthRecord: %v", errSave)
		}
	}

	got := jsonFileNames(t, authDir)
	if len(got) != 1 {
		t.Fatalf("auth files = %v, want exactly one remaining file", got)
	}
	saved := readJSONMap(t, filepath.Join(authDir, got[0]))
	if saved["access_token"] != "from-free" && saved["access_token"] != "from-pro" {
		t.Errorf("access_token = %v, want one of the concurrent saves", saved["access_token"])
	}
}

func TestSaveCodexOAuthRecordRemovesNestedSibling(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	keepRel := filepath.ToSlash(filepath.Join("team", "codex-keep.json"))
	dropRel := filepath.ToSlash(filepath.Join("team", "codex-drop.json"))
	writeCodexJSON(t, filepath.Join(authDir, "team"), "codex-keep.json", map[string]any{
		"type":       "codex",
		"account_id": testCodexAccountID,
		"note":       "keep-nested",
		"disabled":   true,
	})
	writeCodexJSON(t, filepath.Join(authDir, "team"), "codex-drop.json", map[string]any{
		"type":       "codex",
		"account_id": testCodexAccountID,
		"note":       "drop-nested",
	})

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, keepRel); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}
	if _, errStat := os.Stat(filepath.Join(authDir, filepath.FromSlash(keepRel))); errStat != nil {
		t.Fatalf("keep file missing: %v", errStat)
	}
	if _, errStat := os.Stat(filepath.Join(authDir, filepath.FromSlash(dropRel))); !os.IsNotExist(errStat) {
		t.Fatalf("drop sibling still present: %v", errStat)
	}
	saved := readJSONMap(t, filepath.Join(authDir, filepath.FromSlash(keepRel)))
	if saved["note"] != "keep-nested" {
		t.Errorf("note = %v, want keep-nested", saved["note"])
	}
	if saved["access_token"] != "new-access" {
		t.Errorf("access_token = %v, want new-access", saved["access_token"])
	}
}

func TestSaveCodexOAuthRecordUpdatesRuntimeAuthMetadata(t *testing.T) {
	authDir := t.TempDir()
	nestedRel := filepath.ToSlash(filepath.Join("team", "codex-user.json"))
	nestedPath := filepath.Join(authDir, filepath.FromSlash(nestedRel))
	writeCodexJSON(t, filepath.Dir(nestedPath), filepath.Base(nestedPath), map[string]any{
		"type":         "codex",
		"account_id":   testCodexAccountID,
		"access_token": "old-access",
		"note":         "nested",
	})
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       nestedRel,
		FileName: nestedRel,
		Provider: "codex",
		Attributes: map[string]string{
			"path":      nestedPath,
			"plan_type": "free",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"account_id":   testCodexAccountID,
			"access_token": "old-access",
		},
	}); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if storage, ok := record.Storage.(*codex.CodexTokenStorage); ok {
		storage.IDToken = fakeCodexIDToken("pro")
	}
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, nestedRel); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	got, ok := manager.GetByID(nestedRel)
	if !ok || got == nil {
		t.Fatal("runtime auth missing after replace")
	}
	if record.Metadata["access_token"] != "new-access" {
		t.Errorf("persist record access_token = %v, want new-access", record.Metadata["access_token"])
	}
	if got.Metadata["access_token"] != "new-access" {
		t.Errorf("runtime access_token = %v, want new-access", got.Metadata["access_token"])
	}
	if got.Attributes["plan_type"] != "pro" {
		t.Errorf("runtime plan_type = %q, want pro", got.Attributes["plan_type"])
	}
	if got.Disabled {
		t.Error("runtime auth still disabled")
	}
}

func TestSaveCodexOAuthRecordClearsRuntimeCooldownOnReplace(t *testing.T) {
	authDir := t.TempDir()
	nestedRel := filepath.ToSlash(filepath.Join("team", "codex-user.json"))
	nestedPath := filepath.Join(authDir, filepath.FromSlash(nestedRel))
	writeCodexJSON(t, filepath.Dir(nestedPath), filepath.Base(nestedPath), map[string]any{
		"type":         "codex",
		"account_id":   testCodexAccountID,
		"access_token": "old-access",
	})
	recoverAt := time.Now().Add(time.Hour)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:             nestedRel,
		FileName:       nestedRel,
		Provider:       "codex",
		Status:         coreauth.StatusError,
		Unavailable:    true,
		NextRetryAfter: recoverAt,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: recoverAt,
		},
		ModelStates: map[string]*coreauth.ModelState{
			"gpt-5": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: recoverAt,
				LastError:      &coreauth.Error{Message: "quota"},
				Quota: coreauth.QuotaState{
					Exceeded:      true,
					Reason:        "credential_quota",
					NextRecoverAt: recoverAt,
				},
			},
		},
		Attributes: map[string]string{
			"path":      nestedPath,
			"plan_type": "free",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"account_id":   testCodexAccountID,
			"access_token": "old-access",
		},
	}); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if storage, ok := record.Storage.(*codex.CodexTokenStorage); ok {
		storage.IDToken = fakeCodexIDToken("pro")
	}
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, nestedRel); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	got, ok := manager.GetByID(nestedRel)
	if !ok || got == nil {
		t.Fatal("runtime auth missing after replace")
	}
	if got.Status != coreauth.StatusActive {
		t.Errorf("status = %q, want %q", got.Status, coreauth.StatusActive)
	}
	if got.Unavailable {
		t.Error("runtime auth still unavailable")
	}
	if !got.NextRetryAfter.IsZero() {
		t.Errorf("NextRetryAfter = %v, want zero", got.NextRetryAfter)
	}
	if got.Quota.Exceeded || got.Quota.Reason != "" {
		t.Errorf("quota = %+v, want cleared", got.Quota)
	}
	if len(got.ModelStates) > 0 {
		t.Errorf("ModelStates = %+v, want empty", got.ModelStates)
	}
}

func TestSaveCodexOAuthRecordKeepsNestedRuntimeSettingsAfterPersistSync(t *testing.T) {
	authDir := t.TempDir()
	nestedRel := filepath.ToSlash(filepath.Join("team", "codex-user.json"))
	nestedPath := filepath.Join(authDir, filepath.FromSlash(nestedRel))
	writeCodexJSON(t, filepath.Dir(nestedPath), filepath.Base(nestedPath), map[string]any{
		"type":            "codex",
		"account_id":      testCodexAccountID,
		"access_token":    "old-access",
		"note":            "nested",
		"priority":        float64(60),
		"proxy_url":       "socks5://10.80.1.61:2083",
		"prefix":          "team",
		"excluded_models": []any{"gpt-4"},
	})
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       nestedRel,
		FileName: nestedRel,
		Provider: "codex",
		Prefix:   "team",
		ProxyURL: "socks5://10.80.1.61:2083",
		Label:    "user@example.com",
		Attributes: map[string]string{
			"path":            nestedPath,
			"plan_type":       "free",
			"priority":        "60",
			"note":            "nested",
			"excluded_models": "gpt-4",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"account_id":   testCodexAccountID,
			"access_token": "old-access",
			"note":         "nested",
			"priority":     float64(60),
			"proxy_url":    "socks5://10.80.1.61:2083",
			"prefix":       "team",
		},
	}); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.postAuthPersistHook = func(ctx context.Context, auth *coreauth.Auth) error {
		if _, ok := manager.GetByID(auth.ID); ok {
			_, errUpdate := manager.Update(ctx, auth)
			return errUpdate
		}
		_, errRegister := manager.Register(ctx, auth)
		return errRegister
	}

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if storage, ok := record.Storage.(*codex.CodexTokenStorage); ok {
		storage.IDToken = fakeCodexIDToken("pro")
	}
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, nestedRel); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	got, ok := manager.GetByID(nestedRel)
	if !ok || got == nil {
		t.Fatal("runtime auth missing after replace")
	}
	if got.Prefix != "team" {
		t.Errorf("prefix = %q, want team", got.Prefix)
	}
	if got.ProxyURL != "socks5://10.80.1.61:2083" {
		t.Errorf("proxy_url = %q", got.ProxyURL)
	}
	if got.Attributes["priority"] != "60" {
		t.Errorf("priority = %q, want 60", got.Attributes["priority"])
	}
	if got.Attributes["note"] != "nested" {
		t.Errorf("note = %q, want nested", got.Attributes["note"])
	}
	if got.Attributes["excluded_models"] != "gpt-4" {
		t.Errorf("excluded_models = %q, want gpt-4", got.Attributes["excluded_models"])
	}
	if got.Attributes["plan_type"] != "pro" {
		t.Errorf("plan_type = %q, want pro", got.Attributes["plan_type"])
	}
	if got.Metadata["access_token"] != "new-access" {
		t.Errorf("access_token = %v, want new-access", got.Metadata["access_token"])
	}
}

func TestSaveCodexOAuthRecordSkipsSaveWhenSessionCancelledDuringList(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	h.tokenStore = overlayListStore{
		Store: h.tokenStoreWithBaseDir(),
		beforeList: func() {
			close(started)
			<-release
		},
	}
	state := "codex-cancel-during-list"
	RegisterOAuthSession(state, "codex")
	t.Cleanup(func() { CancelOAuthSession(state) })

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	errCh := make(chan error, 1)
	go func() {
		_, errSave := h.saveCodexOAuthRecord(withOAuthSaveSession(context.Background(), state, "codex"), record, "")
		errCh <- errSave
	}()
	<-started
	if !CancelOAuthSession(state) {
		t.Fatal("expected pending session to cancel")
	}
	close(release)
	errSave := <-errCh
	if !errors.Is(errSave, errOAuthSessionNotPending) {
		t.Fatalf("error = %v, want %v", errSave, errOAuthSessionNotPending)
	}
	if names := jsonFileNames(t, authDir); len(names) != 0 {
		t.Fatalf("auth files = %v, want none after cancelled save", names)
	}
}

func TestSaveCodexOAuthRecordSiblingDeleteFailureIsReturned(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	freeName := codexFileName("user@example.com", "free", testCodexAccountID)
	proName := codexFileName("user@example.com", "pro", testCodexAccountID)
	writeCodexJSON(t, authDir, freeName, map[string]any{
		"type":       "codex",
		"account_id": testCodexAccountID,
		"note":       "keep",
	})
	writeCodexJSON(t, authDir, proName, map[string]any{
		"type":       "codex",
		"account_id": testCodexAccountID,
	})
	h.tokenStore = overlayListStore{
		Store:     h.tokenStoreWithBaseDir(),
		deleteErr: fmt.Errorf("remote delete failed"),
	}

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	_, errSave := h.saveCodexOAuthRecord(context.Background(), record, freeName)
	if errSave == nil || !strings.Contains(errSave.Error(), "remote delete failed") {
		t.Fatalf("error = %v, want remote delete failed", errSave)
	}
}

func TestSaveCodexOAuthRecordRemovesRuntimeWhenSiblingFileAlreadyGone(t *testing.T) {
	authDir := t.TempDir()
	keepRel := filepath.ToSlash(filepath.Join("team", "codex-keep.json"))
	dropRel := filepath.ToSlash(filepath.Join("team", "codex-drop.json"))
	dropPath := filepath.Join(authDir, filepath.FromSlash(dropRel))
	writeCodexJSON(t, filepath.Join(authDir, "team"), "codex-keep.json", map[string]any{
		"type":       "codex",
		"account_id": testCodexAccountID,
		"note":       "keep-nested",
	})
	writeCodexJSON(t, filepath.Join(authDir, "team"), "codex-drop.json", map[string]any{
		"type":       "codex",
		"account_id": testCodexAccountID,
	})
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       dropRel,
		FileName: dropRel,
		Provider: "codex",
		Attributes: map[string]string{
			"path": dropPath,
		},
	}); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = overlayListStore{
		Store:              h.tokenStoreWithBaseDir(),
		deleteErr:          fmt.Errorf("remote delete failed"),
		deleteLocalThenErr: true,
	}

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	_, errSave := h.saveCodexOAuthRecord(context.Background(), record, keepRel)
	if errSave == nil || !strings.Contains(errSave.Error(), "remote delete failed") {
		t.Fatalf("error = %v, want remote delete failed", errSave)
	}
	if _, errStat := os.Stat(dropPath); !os.IsNotExist(errStat) {
		t.Fatalf("drop sibling still present: %v", errStat)
	}
	if _, ok := manager.GetByID(dropRel); ok {
		t.Fatal("runtime auth for deleted sibling still registered")
	}
}

func TestSaveCodexOAuthRecordListErrorDoesNotCreateFile(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	h.tokenStore = overlayListStore{Store: h.tokenStoreWithBaseDir(), listErr: fmt.Errorf("store list failed")}

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, ""); errSave == nil {
		t.Fatal("expected list error, got nil")
	} else if !strings.Contains(errSave.Error(), "store list failed") {
		t.Fatalf("error = %v, want store list failed", errSave)
	}
	if names := jsonFileNames(t, authDir); len(names) != 0 {
		t.Fatalf("auth files = %v, want none after list failure", names)
	}
}

func TestSaveCodexOAuthRecordUsesStoreListNotDiskScan(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	base := h.tokenStoreWithBaseDir()
	keepName := "codex-store-keep.json"
	h.tokenStore = overlayListStore{
		Store: base,
		list: []*coreauth.Auth{{
			ID:       keepName,
			Provider: "codex",
			FileName: keepName,
			Metadata: map[string]any{
				"type":       "codex",
				"account_id": testCodexAccountID,
				"note":       "from-store",
				"priority":   float64(7),
			},
		}},
	}

	record := newCodexRecord("user@example.com", testCodexAccountID, "pro", "new-access", "new-refresh")
	if _, errSave := h.saveCodexOAuthRecord(context.Background(), record, ""); errSave != nil {
		t.Fatalf("saveCodexOAuthRecord: %v", errSave)
	}

	got := jsonFileNames(t, authDir)
	if !stringSlicesEqual(got, []string{keepName}) {
		t.Fatalf("auth files = %v, want [%s]", got, keepName)
	}
	saved := readJSONMap(t, filepath.Join(authDir, keepName))
	if saved["note"] != "from-store" {
		t.Errorf("note = %v, want from-store", saved["note"])
	}
	if saved["priority"] != float64(7) {
		t.Errorf("priority = %v, want 7", saved["priority"])
	}
	if saved["access_token"] != "new-access" {
		t.Errorf("access_token = %v, want new-access", saved["access_token"])
	}
}

type overlayListStore struct {
	coreauth.Store
	list               []*coreauth.Auth
	listErr            error
	deleteErr          error
	deleteLocalThenErr bool
	beforeList         func()
}

func (s overlayListStore) List(ctx context.Context) ([]*coreauth.Auth, error) {
	if s.beforeList != nil {
		s.beforeList()
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.list != nil {
		return s.list, nil
	}
	if s.Store == nil {
		return nil, nil
	}
	return s.Store.List(ctx)
}

func (s overlayListStore) Delete(ctx context.Context, id string) error {
	if s.deleteErr != nil {
		if s.deleteLocalThenErr && s.Store != nil {
			_ = s.Store.Delete(ctx, id)
		}
		return s.deleteErr
	}
	if s.Store == nil {
		return nil
	}
	return s.Store.Delete(ctx, id)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
