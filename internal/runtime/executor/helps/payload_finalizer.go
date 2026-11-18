package helps

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// PayloadFinalizer applies user rules to a fully prepared business payload.
// Each rebuilt attempt must start from the unconfigured body, not a previous result.
type PayloadFinalizer func([]byte) []byte

var (
	payloadDocProbes        sync.Map
	builtinFinalizerCodePtr atomic.Uintptr
)

func payloadFinalizerKeyPtr(f PayloadFinalizer) uintptr {
	if f == nil {
		return 0
	}
	return reflect.ValueOf(f).Pointer()
}

// FinalizeDoc applies the finalizer to doc in place without probing custom wrappers.
func (f PayloadFinalizer) FinalizeDoc(doc *ojson.Doc) {
	if f == nil || doc == nil {
		return
	}
	if ptr := payloadFinalizerKeyPtr(f); ptr != 0 && ptr == builtinFinalizerCodePtr.Load() {
		var probe [1]byte
		key := &probe[0]
		payloadDocProbes.Store(key, doc)
		_ = f(probe[:0])
		if _, remaining := payloadDocProbes.LoadAndDelete(key); !remaining {
			return
		}
	}
	updated := f(doc.Bytes())
	*doc = *ojson.NewDoc(updated)
}

// NewPayloadFinalizer snapshots matching context before built-in request mutations.
func NewPayloadFinalizer(cfg *config.Config, executor, model, protocol, root string, original []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, auths ...*cliproxyauth.Auth) PayloadFinalizer {
	requestedModel := PayloadRequestedModel(opts, req.Model)
	requestPath := PayloadRequestPath(opts)
	headers := opts.Headers.Clone()
	source := append([]byte(nil), original...)
	var auth *cliproxyauth.Auth
	if len(auths) > 0 {
		auth = auths[0]
	}
	var formatCallback func(string)
	if opts.Metadata != nil {
		formatCallback, _ = opts.Metadata[cliproxyexecutor.FinalizedResponseFormatCallbackMetadataKey].(func(string))
	}
	fn := PayloadFinalizer(func(body []byte) []byte {
		if len(body) == 0 && cap(body) == 1 {
			key := &body[:1][0]
			if v, ok := payloadDocProbes.LoadAndDelete(key); ok {
				if doc, okDoc := v.(*ojson.Doc); okDoc && doc != nil {
					applyPayloadConfigWithTrackedPathsForExecutorAndAuthDoc(cfg, executor, model, protocol, opts.SourceFormat.String(), root, doc, source, requestedModel, requestPath, headers, auth)
					if formatCallback != nil {
						formatCallback(strings.Clone(strings.TrimSpace(doc.Get(buildPayloadPath(root, "response_format")).String())))
					}
				}
				return nil
			}
		}
		doc := ojson.NewDoc(body)
		_, mutated := applyPayloadConfigWithTrackedPathsForExecutorAndAuthDoc(cfg, executor, model, protocol, opts.SourceFormat.String(), root, doc, source, requestedModel, requestPath, headers, auth)
		out := body
		if mutated {
			out = doc.Bytes()
		}
		if formatCallback != nil {
			formatCallback(strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(out, buildPayloadPath(root, "response_format")).String())))
		}
		return out
	})
	builtinFinalizerCodePtr.Store(payloadFinalizerKeyPtr(fn))
	return fn
}

type payloadFinalizerKey struct{}

// WithPayloadFinalizer carries the final barrier through shared request builders.
func WithPayloadFinalizer(ctx context.Context, finalize PayloadFinalizer) context.Context {
	return context.WithValue(ctx, payloadFinalizerKey{}, finalize)
}

// FinalizePayload runs immediately before serialization or transport framing.
func FinalizePayload(ctx context.Context, body []byte) []byte {
	if finalize, ok := ctx.Value(payloadFinalizerKey{}).(PayloadFinalizer); ok && finalize != nil {
		return finalize(body)
	}
	return body
}

// FinalizePayloadDoc runs immediately before serialization or transport framing,
// mutating doc in place.
func FinalizePayloadDoc(ctx context.Context, doc *ojson.Doc) {
	if doc == nil {
		return
	}
	if finalize, ok := ctx.Value(payloadFinalizerKey{}).(PayloadFinalizer); ok && finalize != nil {
		finalize.FinalizeDoc(doc)
	}
}
