package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

type handlerTestTransport struct {
	http.RoundTripper
	source string
}

func TestGetContextWithCancelInheritsHTTPTransportDecorator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	reqCtx := httptransport.WithDecorator(req.Context(), func(next http.RoundTripper) http.RoundTripper {
		return &handlerTestTransport{RoundTripper: next, source: "request"}
	})
	ginCtx.Request = req.WithContext(reqCtx)

	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, context.Background())
	defer cancel()

	base := http.DefaultTransport.(*http.Transport).Clone()
	client := httptransport.Decorate(ctx, &http.Client{Transport: base})
	wrapped, ok := client.Transport.(*handlerTestTransport)
	if !ok || wrapped.source != "request" || wrapped.RoundTripper != base {
		t.Fatalf("expected request decorator to propagate through GetContextWithCancel, got %T", client.Transport)
	}

	parentCtx := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
		return &handlerTestTransport{RoundTripper: next, source: "parent"}
	})
	ctxParent, cancelParent := handler.GetContextWithCancel(nil, ginCtx, parentCtx)
	defer cancelParent()

	clientParent := httptransport.Decorate(ctxParent, &http.Client{Transport: base})
	wrappedParent, ok := clientParent.Transport.(*handlerTestTransport)
	if !ok || wrappedParent.source != "parent" {
		t.Fatalf("expected explicit parent decorator to win, got %T (%v)", clientParent.Transport, wrappedParent)
	}
}
