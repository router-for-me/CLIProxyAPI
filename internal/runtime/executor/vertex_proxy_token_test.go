package executor

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestVertexAccessTokenUsesCredentialProxyNotRequestProxy(t *testing.T) {
	var requestHits atomic.Int32
	var authHits atomic.Int32
	var globalHits atomic.Int32
	requestProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestHits.Add(1)
		http.Error(w, "request proxy", http.StatusBadGateway)
	}))
	defer requestProxy.Close()
	authProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHits.Add(1)
		http.Error(w, "auth proxy", http.StatusBadGateway)
	}))
	defer authProxy.Close()
	globalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalHits.Add(1)
		http.Error(w, "global proxy", http.StatusBadGateway)
	}))
	defer globalProxy.Close()

	ctx, cancel := context.WithTimeout(
		cliproxyexecutor.WithRequestProxyURL(context.Background(), requestProxy.URL),
		3*time.Second,
	)
	defer cancel()
	_, _ = vertexAccessToken(
		ctx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: globalProxy.URL}},
		&cliproxyauth.Auth{ProxyURL: authProxy.URL},
		testVertexServiceAccountJSON(t, "https://oauth2.googleapis.com/token"),
	)

	if requestHits.Load() != 0 {
		t.Fatalf("token exchange used request proxy %d times", requestHits.Load())
	}
	if authHits.Load() == 0 {
		t.Fatal("token exchange did not use the credential proxy")
	}
	if globalHits.Load() != 0 {
		t.Fatalf("token exchange used global proxy %d times", globalHits.Load())
	}
}

func testVertexServiceAccountJSON(t *testing.T, tokenURI string) []byte {
	t.Helper()
	key, errKey := rsa.GenerateKey(rand.Reader, 2048)
	if errKey != nil {
		t.Fatalf("generate key: %v", errKey)
	}
	pkcs8, errMarshal := x509.MarshalPKCS8PrivateKey(key)
	if errMarshal != nil {
		t.Fatalf("marshal key: %v", errMarshal)
	}
	raw, errJSON := json.Marshal(map[string]any{
		"type":           "service_account",
		"project_id":     "proxy-test",
		"private_key_id": "kid",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		"client_email":   "proxy-test@proxy-test.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	})
	if errJSON != nil {
		t.Fatalf("marshal service account: %v", errJSON)
	}
	return raw
}
