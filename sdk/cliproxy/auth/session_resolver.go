package auth

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

func init() {
	util.SessionIDResolver = func(ctx context.Context, clientHeaders http.Header) string {
		if ctx != nil {
			if id := util.SessionIDFromContext(ctx); id != "" {
				return id
			}
			if util.HasExplicitSessionID(ctx) {
				return ""
			}
			if meta := logging.GetClientRequestMetadata(ctx); meta.SessionID != "" {
				return meta.SessionID
			}
			if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
				if util.HasExplicitSessionID(ginCtx.Request.Context()) {
					return ""
				}
				if meta := logging.GetClientRequestMetadata(ginCtx.Request.Context()); meta.SessionID != "" {
					return meta.SessionID
				}
				if clientHeaders == nil {
					clientHeaders = ginCtx.Request.Header
				}
			} else if ginCtx, ok := ctx.(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
				if util.HasExplicitSessionID(ginCtx.Request.Context()) {
					return ""
				}
				if meta := logging.GetClientRequestMetadata(ginCtx.Request.Context()); meta.SessionID != "" {
					return meta.SessionID
				}
				if clientHeaders == nil {
					clientHeaders = ginCtx.Request.Header
				}
			}
		}
		if clientHeaders != nil {
			if canonical := CanonicalSessionID(clientHeaders, nil, nil); canonical != "" {
				return canonical
			}
		}
		return ""
	}
}
