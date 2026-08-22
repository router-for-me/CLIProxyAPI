package usercontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type memoryRepository struct {
	Repository
	mu       sync.Mutex
	users    map[string]User
	keys     map[string]StoredAPIKey
	invites  map[string]OAuthInvite
	sessions map[string]string
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		users:    make(map[string]User),
		keys:     make(map[string]StoredAPIKey),
		invites:  make(map[string]OAuthInvite),
		sessions: make(map[string]string),
	}
}

func (r *memoryRepository) CreateUser(_ context.Context, user User) (User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[user.ID] = user
	return user, nil
}

func (r *memoryRepository) GetUser(_ context.Context, id string) (User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return user, nil
}

func (r *memoryRepository) CreateAPIKey(_ context.Context, key APIKey, hash []byte) (APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[key.ID] = StoredAPIKey{APIKey: key, SecretHash: append([]byte(nil), hash...), User: r.users[key.UserID]}
	return key, nil
}

func (r *memoryRepository) GetStoredAPIKey(_ context.Context, id string) (StoredAPIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.keys[id]
	if !ok {
		return StoredAPIKey{}, ErrNotFound
	}
	key.User = r.users[key.UserID]
	return key, nil
}

func (r *memoryRepository) CreateOAuthInvite(_ context.Context, invite OAuthInvite, hash []byte) (OAuthInvite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invites[hex.EncodeToString(hash)] = invite
	return invite, nil
}

func (r *memoryRepository) GetOAuthInviteByTokenHash(_ context.Context, hash []byte) (OAuthInvite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	invite, ok := r.invites[hex.EncodeToString(hash)]
	if !ok {
		return OAuthInvite{}, ErrNotFound
	}
	return invite, nil
}

func (r *memoryRepository) ReserveOAuthInvite(_ context.Context, hash []byte, state, provider string) (OAuthInvite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := hex.EncodeToString(hash)
	invite, ok := r.invites[key]
	if !ok || !inviteAvailable(invite, time.Now()) || !containsProvider(invite.Providers, provider) {
		return OAuthInvite{}, ErrInviteUnavailable
	}
	invite.ReservedUses++
	r.invites[key] = invite
	r.sessions[state] = key
	return invite, nil
}

func (r *memoryRepository) OAuthInviteOwnsSession(_ context.Context, hash []byte, state string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[state] == hex.EncodeToString(hash), nil
}

func (r *memoryRepository) CompleteOAuthInvite(_ context.Context, state, _, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.sessions[state]
	if !ok {
		return ErrNotFound
	}
	invite := r.invites[key]
	invite.ReservedUses--
	invite.UsedUses++
	r.invites[key] = invite
	return nil
}

func TestManagedAPIKeyAuthenticatesWithoutReturningTheSecret(t *testing.T) {
	repository := newMemoryRepository()
	service := NewService(repository)
	user, err := service.CreateUser(context.Background(), CreateUserInput{Name: "Ada", Limits: Limits{MonthlyTokens: 1000}})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	created, err := service.CreateAPIKey(context.Background(), user.ID, CreateAPIKeyInput{Name: "Laptop"})
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+created.Secret)
	result, authErr := service.Authenticate(context.Background(), request)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	if result.Principal != principalPrefix+created.ID {
		t.Fatalf("Authenticate() principal = %q", result.Principal)
	}
	if strings.Contains(result.Principal, created.Secret) {
		t.Fatal("principal exposed the API key secret")
	}

	stored := repository.keys[created.ID]
	if string(stored.SecretHash) == created.Secret {
		t.Fatal("repository stored the API key in plaintext")
	}
	parts := strings.SplitN(created.Secret, "_", 3)
	wantHash := sha256.Sum256([]byte(parts[2]))
	if string(stored.SecretHash) != string(wantHash[:]) {
		t.Fatal("repository did not receive the expected secret digest")
	}
}

func TestManagedMiddlewareEnforcesModelsAndRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newMemoryRepository()
	service := NewService(repository)
	user, err := service.CreateUser(context.Background(), CreateUserInput{
		Name: "Grace",
		Limits: Limits{
			RequestsPerMinute: 1,
			AllowedModels:     []string{"gpt-*"},
		},
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	key, err := service.CreateAPIKey(context.Background(), user.ID, CreateAPIKeyInput{})
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}

	router := gin.New()
	router.Use(service.Middleware())
	router.POST("/v1/chat/completions", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := func(model string) int {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`"}`))
		req.Header.Set("Authorization", "Bearer "+key.Secret)
		router.ServeHTTP(recorder, req)
		return recorder.Code
	}
	if got := request("claude-sonnet"); got != http.StatusForbidden {
		t.Fatalf("disallowed model status = %d, want 403", got)
	}
	if got := request("gpt-5"); got != http.StatusNoContent {
		t.Fatalf("first allowed request status = %d, want 204", got)
	}
	if got := request("gpt-5"); got != http.StatusTooManyRequests {
		t.Fatalf("rate-limited request status = %d, want 429", got)
	}
}

func TestOAuthInvitationCanOnlyConsumeAnAllowedSessionOnce(t *testing.T) {
	repository := newMemoryRepository()
	service := NewService(repository)
	created, err := service.CreateOAuthInvite(context.Background(), CreateOAuthInviteInput{
		Label:     "Community login",
		Providers: []string{"kimi"},
		MaxUses:   1,
	})
	if err != nil {
		t.Fatalf("CreateOAuthInvite() error = %v", err)
	}
	if _, err = service.ReserveOAuthInvite(context.Background(), created.Token, "state-1", "codex"); err != ErrInviteUnavailable {
		t.Fatalf("ReserveOAuthInvite(codex) error = %v, want ErrInviteUnavailable", err)
	}
	if _, err = service.ReserveOAuthInvite(context.Background(), created.Token, "state-1", "kimi"); err != nil {
		t.Fatalf("ReserveOAuthInvite(kimi) error = %v", err)
	}
	owns, err := service.OAuthInviteOwnsSession(context.Background(), created.Token, "state-1")
	if err != nil || !owns {
		t.Fatalf("OAuthInviteOwnsSession() = %v, %v", owns, err)
	}
	if err = service.CompleteOAuthInvite(context.Background(), "state-1", "kimi.json", ""); err != nil {
		t.Fatalf("CompleteOAuthInvite() error = %v", err)
	}
	if _, err = service.GetOAuthInvite(context.Background(), created.Token); err != ErrInviteUnavailable {
		t.Fatalf("GetOAuthInvite() after use error = %v, want ErrInviteUnavailable", err)
	}
}
