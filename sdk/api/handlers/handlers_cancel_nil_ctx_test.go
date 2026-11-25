package handlers

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestGetContextWithCancel_NilGinContextAndNilWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewBaseAPIHandlers(&config.SDKConfig{RequestLog: true}, nil)

	t.Run("nil gin context with request log payload", func(t *testing.T) {
		ctx, cancel := h.GetContextWithCancel(nil, nil, context.Background())
		if ctx == nil {
			t.Fatal("expected non-nil context")
		}

		cancel([]byte(`{"ok":true}`))

		if err := ctx.Err(); err != context.Canceled {
			t.Fatalf("expected context.Canceled after cancel, got %v", err)
		}
	})

	t.Run("non-nil gin context with nil writer and request log payload", func(t *testing.T) {
		c := &gin.Context{}
		ctx, cancel := h.GetContextWithCancel(nil, c, context.Background())
		if ctx == nil {
			t.Fatal("expected non-nil context")
		}

		cancel([]byte(`{"ok":true}`))

		if err := ctx.Err(); err != context.Canceled {
			t.Fatalf("expected context.Canceled after cancel, got %v", err)
		}
	})
}
