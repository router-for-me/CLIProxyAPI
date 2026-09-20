package billing

import (
	"context"

	"github.com/gin-gonic/gin"
)

type principalContextKey struct{}

const GinPrincipalKey = "billingPrincipal"

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if p.UserID == "" {
		return ctx
	}
	return context.WithValue(ctx, principalContextKey{}, p)
}

func PrincipalFromContext(ctx context.Context) Principal {
	if ctx == nil {
		return Principal{}
	}
	if p, ok := ctx.Value(principalContextKey{}).(Principal); ok {
		return p
	}
	return Principal{}
}

func SetGinPrincipal(c *gin.Context, p Principal) {
	if c == nil || p.UserID == "" {
		return
	}
	c.Set(GinPrincipalKey, p)
	if c.Request != nil {
		c.Request = c.Request.WithContext(WithPrincipal(c.Request.Context(), p))
	}
}

func PrincipalFromGin(c *gin.Context) Principal {
	if c == nil {
		return Principal{}
	}
	if raw, ok := c.Get(GinPrincipalKey); ok {
		if p, ok := raw.(Principal); ok {
			return p
		}
	}
	if c.Request != nil {
		return PrincipalFromContext(c.Request.Context())
	}
	return Principal{}
}
