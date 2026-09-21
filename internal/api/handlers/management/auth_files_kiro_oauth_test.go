package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestWriteKiroSocialLoopbackSuccessReopensIDE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/success", writeKiroSocialLoopbackSuccess)

	req := httptest.NewRequest(http.MethodGet, "/success?code=sensitive-code&state=sensitive-state", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("expected HTML content type, got %q", got)
	}

	body := w.Body.String()
	if !strings.Contains(body, `window.location.assign("`+kiro.SocialRedirectURI+`")`) {
		t.Fatalf("expected automatic Kiro redirect, got %s", body)
	}
	if !strings.Contains(body, `href="`+kiro.SocialRedirectURI+`"`) {
		t.Fatalf("expected manual Kiro link, got %s", body)
	}
	if strings.Count(body, kiro.SocialRedirectURI) != 2 {
		t.Fatalf("expected the Kiro redirect URI exactly twice, got %s", body)
	}
	if strings.Contains(body, "sensitive-code") || strings.Contains(body, "sensitive-state") {
		t.Fatalf("callback credentials must not be forwarded to Kiro, got %s", body)
	}
}

func TestHandleKiroSocialLoopbackCallbackDoesNotReopenIDEOnError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}}
	router := gin.New()
	router.GET("/oauth/callback", h.HandleKiroSocialLoopbackCallback)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=unused-code", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), kiro.SocialRedirectURI) {
		t.Fatalf("error response must not open Kiro, got %s", w.Body.String())
	}
}
