package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

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
	list    []*coreauth.Auth
	listErr error
}

func (s overlayListStore) List(ctx context.Context) ([]*coreauth.Auth, error) {
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
