package managementauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCreatesAdminBootstrapAndPersistsBcryptOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "management-accounts.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if store == nil {
		t.Fatalf("nil store")
	}
	assertMode0600(t, path)
	bootstrap := filepath.Join(dir, BootstrapFile)
	assertMode0600(t, bootstrap)
	passwordBytes, err := os.ReadFile(bootstrap)
	if err != nil {
		t.Fatalf("read bootstrap: %v", err)
	}
	password := strings.TrimSpace(string(passwordBytes))
	if password == "" {
		t.Fatalf("empty bootstrap password")
	}
	accountBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read account store: %v", err)
	}
	if strings.Contains(string(accountBytes), password) {
		t.Fatalf("store contains bootstrap plaintext")
	}
	var disk diskStore
	if err := json.Unmarshal(accountBytes, &disk); err != nil {
		t.Fatalf("unmarshal store: %v", err)
	}
	admin := disk.Users[AdminUsername]
	if !strings.HasPrefix(admin.PasswordHash, "$2") {
		t.Fatalf("admin password is not bcrypt: %q", admin.PasswordHash)
	}
	token, expires, user, err := store.Login(AdminUsername, password, "127.0.0.1")
	if err != nil {
		t.Fatalf("login with bootstrap password: %v", err)
	}
	if token == "" || time.Until(expires) <= 11*time.Hour || user.Username != AdminUsername || user.Role != RoleAdmin || user.Disabled {
		t.Fatalf("unexpected login result token=%q expires=%s user=%+v", token, expires, user)
	}
	if _, ok := store.Authenticate(token); !ok {
		t.Fatalf("bootstrap session did not authenticate")
	}
}

func TestOpenExistingDoesNotRewriteBootstrapAndKeepsPassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "management-accounts.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	password := strings.TrimSpace(mustRead(t, filepath.Join(dir, BootstrapFile)))
	if _, _, _, err := store.Login(AdminUsername, password, "ip"); err != nil {
		t.Fatalf("initial login: %v", err)
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := strings.TrimSpace(mustRead(t, filepath.Join(dir, BootstrapFile))); got != password {
		t.Fatalf("bootstrap file changed")
	}
}

func TestUserLifecycleRevokesSessionsAndProtectsAdmin(t *testing.T) {
	store, adminPassword := newTestStore(t)
	token, _, admin, err := store.Login(AdminUsername, adminPassword, "admin-ip")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	if _, err := store.CreateUser(User{Username: "bob", Role: RoleUser}, "alice", "pw1"); err != ErrForbidden {
		t.Fatalf("non-admin CreateUser err=%v", err)
	}
	alice, err := store.CreateUser(admin, "alice", "pw1")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if alice.Role != RoleUser || alice.Disabled {
		t.Fatalf("unexpected user: %+v", alice)
	}
	aliceToken, _, aliceUser, err := store.Login("alice", "pw1", "alice-ip")
	if err != nil {
		t.Fatalf("alice login: %v", err)
	}
	if _, ok := store.Authenticate(aliceToken); !ok {
		t.Fatalf("alice session should authenticate")
	}
	if err := store.ChangePassword(aliceUser, "wrong", "pw2"); err != ErrUnauthorized {
		t.Fatalf("wrong current password err=%v", err)
	}
	if err := store.ChangePassword(aliceUser, "pw1", "pw2"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, ok := store.Authenticate(aliceToken); ok {
		t.Fatalf("old alice session survived password change")
	}
	aliceToken, _, _, err = store.Login("alice", "pw2", "alice-ip")
	if err != nil {
		t.Fatalf("alice relogin: %v", err)
	}
	if _, err := store.UpdateUser(admin, AdminUsername, UpdateUserRequest{Disabled: boolPtr(true)}); err != ErrForbidden {
		t.Fatalf("expected fixed admin disable forbidden, got %v", err)
	}
	if _, err := store.UpdateUser(admin, "alice", UpdateUserRequest{Disabled: boolPtr(true)}); err != nil {
		t.Fatalf("disable alice: %v", err)
	}
	if _, ok := store.Authenticate(aliceToken); ok {
		t.Fatalf("disabled user session survived")
	}
	if _, _, _, err := store.Login("alice", "pw2", "alice-ip"); err != ErrUnauthorized {
		t.Fatalf("disabled login err=%v", err)
	}
	if _, ok := store.Authenticate(token); !ok {
		t.Fatalf("admin token should remain valid")
	}
}

func TestLoginRateLimitGenericAndLocal(t *testing.T) {
	store, adminPassword := newTestStore(t)
	for i := 0; i < maxFailedAttempts; i++ {
		if _, _, _, err := store.Login("missing", "bad", "127.0.0.1"); err != ErrUnauthorized {
			t.Fatalf("attempt %d err=%v", i+1, err)
		}
	}
	if _, _, _, err := store.Login(AdminUsername, adminPassword, "127.0.0.1"); err != ErrRateLimited {
		t.Fatalf("expected localhost rate limit, got %v", err)
	}
	if _, _, _, err := store.Login(AdminUsername, adminPassword, "127.0.0.2"); err != nil {
		t.Fatalf("other IP should be independent: %v", err)
	}
}

func TestLogoutRevokesOnlyOpaqueToken(t *testing.T) {
	store, password := newTestStore(t)
	t1, _, _, err := store.Login(AdminUsername, password, "ip")
	if err != nil {
		t.Fatalf("login1: %v", err)
	}
	t2, _, _, err := store.Login(AdminUsername, password, "ip")
	if err != nil {
		t.Fatalf("login2: %v", err)
	}
	if !store.Logout(t1) {
		t.Fatalf("logout did not report revoked token")
	}
	if _, ok := store.Authenticate(t1); ok {
		t.Fatalf("logged out token still authenticates")
	}
	if _, ok := store.Authenticate(t2); !ok {
		t.Fatalf("second token should remain valid")
	}
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "management-accounts.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store, strings.TrimSpace(mustRead(t, filepath.Join(dir, BootstrapFile)))
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func assertMode0600(t *testing.T, path string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode=%o, want 600", path, st.Mode().Perm())
	}
}

func boolPtr(v bool) *bool { return &v }
