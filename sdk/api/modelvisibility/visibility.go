// Package modelvisibility lets a plugin narrow the model list a caller sees.
//
// WHY THIS EXISTS. The host serves /v1/models and /v1beta/models from a global
// registry keyed only by handler type. The lookup takes no principal, no
// request context, and its result is cached per handler type, so every caller
// receives the same catalogue no matter which API key they present.
//
// A gateway plugin that restricts which models a key may CALL therefore cannot
// restrict which models that key can SEE: clients that pick a model from the
// listing (Cline, Roo, Cherry Studio and friends) show the user every model and
// then fail on most of them. It is also an information leak: a restricted key
// still enumerates the whole catalogue.
//
// None of the existing model hooks can express this. model.register,
// model.static and model.for_auth only ADD models at auth-registration time and
// receive no caller identity; the response interceptors run only on the
// execution path. This package adds the one missing direction: a filter applied
// to the assembled list, per request, with the caller's identity in hand.
//
// The filter is optional. With no plugin installed the host behaves exactly as
// before: Apply returns its input untouched.
package modelvisibility

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// Request carries everything a filter is allowed to see.
type Request struct {
	// Principal identifies the caller, as resolved by the access layer. Empty
	// when the deployment does not authenticate callers.
	Principal string
	// Provider names the access provider that authenticated the caller.
	Provider string
	// Metadata is provider-supplied caller metadata.
	Metadata map[string]string
	// Handler is the API surface being served: openai, claude or gemini.
	Handler string
	// Models is the model id list the host is about to return, in order.
	Models []string
}

// Filter returns the subset of req.Models the caller may see. Returning nil
// means "no opinion", and the host keeps the full list.
type Filter func(ctx context.Context, req Request) []string

var current atomic.Pointer[Filter]

// Set installs the filter. Passing nil removes it.
func Set(filter Filter) {
	if filter == nil {
		current.Store(nil)
		return
	}
	current.Store(&filter)
}

// Apply narrows models through the installed filter.
//
// Every failure mode keeps the full list: no filter installed, a filter that
// returns nil, or a filter that panics. A listing endpoint must not break
// because a plugin misbehaves, and a plugin that wants to hide models can
// always do so by returning an explicit, possibly empty, slice.
func Apply(ctx context.Context, req Request) (out []string) {
	stored := current.Load()
	if stored == nil || len(req.Models) == 0 {
		return req.Models
	}
	out = req.Models
	defer func() {
		if recover() != nil {
			out = req.Models
		}
	}()
	if narrowed := (*stored)(ctx, req); narrowed != nil {
		return narrowed
	}
	return req.Models
}

// Allowed reports whether one model id survives the filter. It exists for
// handlers that serve a single model rather than a list.
func Allowed(ctx context.Context, req Request, model string) bool {
	req.Models = []string{model}
	return len(Apply(ctx, req)) == 1
}

// FromGin builds a Request from the values the access middleware stored.
func FromGin(c *gin.Context, handler string) Request {
	req := Request{Handler: handler}
	if c == nil {
		return req
	}
	if v, ok := c.Get("userApiKey"); ok {
		req.Principal, _ = v.(string)
	}
	if v, ok := c.Get("accessProvider"); ok {
		req.Provider, _ = v.(string)
	}
	if v, ok := c.Get("accessMetadata"); ok {
		req.Metadata, _ = v.(map[string]string)
	}
	return req
}

// GinContext returns the request context carried by c, falling back to
// context.Background when no request is attached. A gin.Context assembled by
// hand, as tests do, has a nil Request, and a model listing must not panic on
// that.
func GinContext(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}

// FilterMaps narrows a registry model list, reading each id from "id" then
// "name". Entries without a readable id are always kept: a filter must not drop
// a model just because the host shaped it differently.
func FilterMaps(ctx context.Context, req Request, models []map[string]any) []map[string]any {
	if len(models) == 0 || current.Load() == nil {
		return models
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if id := modelID(model); id != "" {
			ids = append(ids, id)
		}
	}
	req.Models = ids
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range Apply(ctx, req) {
		allowed[strings.TrimPrefix(id, "models/")] = struct{}{}
	}

	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		id := modelID(model)
		if id == "" {
			out = append(out, model)
			continue
		}
		if _, ok := allowed[strings.TrimPrefix(id, "models/")]; ok {
			out = append(out, model)
		}
	}
	return out
}

func modelID(model map[string]any) string {
	for _, key := range []string{"id", "name"} {
		if v, ok := model[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
