package translator

import (
	"context"
	"reflect"
	"testing"
)

func TestCheckedTranslationsReportHandledRoutes(t *testing.T) {
	for _, route := range []string{"missing", "identity", "plugin", "declining plugin", "native", "buffering native"} {
		t.Run(route, func(t *testing.T) {
			registry := NewRegistry()
			from, to := Format("client"), Format("provider")
			payload := []byte(`{"value":"original"}`)
			translated := []byte(`{"value":"translated"}`)
			wantHandled := route != "missing" && route != "declining plugin"
			want := payload
			var hooks *fakePluginHooks
			if route == "identity" {
				to = from
			}
			if route == "plugin" || route == "declining plugin" {
				hooks = &fakePluginHooks{requestTranslateBody: translated, requestTranslateOK: route == "plugin", responseTranslateBody: translated, responseTranslateOK: route == "plugin"}
				registry.SetPluginHooks(hooks)
				if route == "plugin" {
					want = translated
				}
			}
			if route == "native" || route == "buffering native" {
				want = translated
				registry.Register(from, to, func(string, []byte, bool) []byte { return translated }, ResponseTransform{
					NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte { return translated },
					Stream: func(context.Context, string, []byte, []byte, []byte, *any) [][]byte {
						if route == "buffering native" {
							return nil
						}
						return [][]byte{translated}
					},
				})
			}
			request, handled := registry.TranslateRequestChecked(from, to, "", payload, false)
			if handled != wantHandled || !reflect.DeepEqual(request, want) {
				t.Errorf("request=%s handled=%t, want %s/%t", request, handled, want, wantHandled)
			}
			response, handled := registry.TranslateNonStreamChecked(t.Context(), to, from, "", nil, nil, payload, nil)
			if handled != wantHandled || !reflect.DeepEqual(response, want) {
				t.Errorf("response=%s handled=%t, want %s/%t", response, handled, want, wantHandled)
			}
			stream, handled := registry.TranslateStreamChecked(t.Context(), to, from, "", nil, nil, payload, nil)
			wantStream := [][]byte{want}
			if route == "buffering native" {
				wantStream = nil
			}
			if handled != wantHandled || !reflect.DeepEqual(stream, wantStream) {
				t.Errorf("stream=%q handled=%t, want %q/%t", stream, handled, wantStream, wantHandled)
			}
			if hooks != nil {
				wantCalls := []string{"normalize-request", "translate-request", "normalize-response-before", "translate-response", "normalize-response-after", "normalize-response-before", "translate-response", "normalize-response-after"}
				if !reflect.DeepEqual(hooks.calls, wantCalls) {
					t.Errorf("hook order=%v, want %v", hooks.calls, wantCalls)
				}
			}
		})
	}
}
