package live

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// mintClientSecret issues a client secret through the HTTP handler and returns its token.
func mintClientSecret(t *testing.T, handler *Handler, principal, provider, label string) string {
	t.Helper()
	router := gin.New()
	router.POST("/v1/realtime/client_secrets", func(c *gin.Context) {
		if principal != "" {
			c.Set("userApiKey", principal)
		}
		if provider != "" {
			c.Set("accessProvider", provider)
		}
		if label != "" {
			c.Set("userApiKeyLabel", label)
		}
		c.Next()
	}, handler.CreateClientSecret)

	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(`{"session":{"type":"realtime","model":"gpt-realtime"}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if principal != "" && strings.Contains(recorder.Body.String(), principal) {
		t.Fatalf("response leaked the issuer key: %s", recorder.Body.String())
	}
	if label != "" && strings.Contains(recorder.Body.String(), strings.TrimSpace(label)) {
		t.Fatalf("response leaked the issuer label: %s", recorder.Body.String())
	}
	var response struct {
		Value string `json:"value"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	return response.Value
}

func TestCreateClientSecretCarriesTrimmedIssuerLabel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{clientSecrets: newClientSecretStore()}
	token := mintClientSecret(t, handler, "issuer-key", "static", "  alice  ")

	authRequest := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", nil)
	authRequest.Header.Set("Authorization", "Bearer "+token)
	authorization, matched, errAuthenticate := handler.AuthenticateClientSecret(authRequest)
	if errAuthenticate != nil || !matched {
		t.Fatalf("AuthenticateClientSecret() matched=%t error=%v", matched, errAuthenticate)
	}
	if authorization.IssuerLabel != "alice" {
		t.Fatalf("IssuerLabel = %q, want %q", authorization.IssuerLabel, "alice")
	}
	if authorization.IssuerPrincipal != "issuer-key" || authorization.IssuerProvider != "static" {
		t.Fatalf("issuer = %q/%q", authorization.IssuerPrincipal, authorization.IssuerProvider)
	}
	if !strings.HasPrefix(authorization.Principal, "sess_") {
		t.Fatalf("Principal = %q, want a sess_ identifier", authorization.Principal)
	}
}

func TestCreateClientSecretWithoutLabelLeavesIssuerLabelEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{clientSecrets: newClientSecretStore()}
	token := mintClientSecret(t, handler, "issuer-key", "static", "")

	authRequest := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", nil)
	authRequest.Header.Set("Authorization", "Bearer "+token)
	authorization, _, errAuthenticate := handler.AuthenticateClientSecret(authRequest)
	if errAuthenticate != nil {
		t.Fatalf("AuthenticateClientSecret() error = %v", errAuthenticate)
	}
	if authorization.IssuerLabel != "" {
		t.Fatalf("IssuerLabel = %q, want empty", authorization.IssuerLabel)
	}
}

func TestClientSecretStoreIssuerCapacityIgnoresLabel(t *testing.T) {
	store := newClientSecretStore()
	session := json.RawMessage(`{"type":"realtime","model":"gpt-live-1-codex"}`)
	for i := 0; i < clientSecretMaxEntriesPerIssuer; i++ {
		label := "alice"
		if i%2 == 1 {
			label = "bob"
		}
		if _, _, _, errCreate := store.create(session, time.Minute, "issuer", "static", label); errCreate != nil {
			t.Fatalf("create(%d) error = %v", i, errCreate)
		}
	}
	// Varying the label must not unlock extra capacity for the same principal and provider.
	if _, _, _, errCreate := store.create(session, time.Minute, "issuer", "static", "carol"); !errors.Is(errCreate, errClientSecretCapacity) {
		t.Fatalf("create() error = %v, want %v", errCreate, errClientSecretCapacity)
	}
	// A distinct principal keeps its own bucket even when the label matches.
	if _, _, _, errCreate := store.create(session, time.Minute, "other-issuer", "static", "alice"); errCreate != nil {
		t.Fatalf("create() for distinct issuer error = %v", errCreate)
	}
}
