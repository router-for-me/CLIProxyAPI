package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCodexClientRequestSnapshotSurvivesGinContextReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("User-Agent", "codex_cli_rs/0.1")
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = req
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	snap := withCodexClientRequestSnapshot(ctx)
	// Simulate gin recycling its Context after the handler returned.
	ginCtx.Request = nil

	if !IsCodexClientRequest(snap) {
		t.Fatal("snapshot lost Codex client detection after gin Context reuse")
	}
	if IsCodexClientRequest(ctx) {
		t.Fatal("unsnapshotted ctx should read the live gin Context")
	}
}

func TestCodexClientRequestSnapshotNonCodexIgnoresGin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	snap := withCodexClientRequestSnapshot(ctx)
	// A later request on the recycled Context must not flip the snapshot.
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginCtx.Request.Header.Set("User-Agent", "codex_cli_rs/0.1")

	if IsCodexClientRequest(snap) {
		t.Fatal("snapshot must not follow a recycled gin Context")
	}
	headers := http.Header{"User-Agent": []string{"codex_cli_rs/0.1"}}
	if !IsCodexClientRequest(snap, headers) {
		t.Fatal("explicit client headers must still be honored")
	}
}
