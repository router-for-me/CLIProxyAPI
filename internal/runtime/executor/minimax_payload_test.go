package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func minimaxImageOpts() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}
}

// TestMinimaxImageAppliesPayloadConfigLast proves the payload finalizer runs
// after every built-in translation step, so a configured override wins over the
// request body and a configured filter removes the field entirely.
func TestMinimaxImageAppliesPayloadConfigLast(t *testing.T) {
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"data":{"image_base64":["QUJD"]}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.Payload = config.PayloadConfig{
		OverrideRaw: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "image-01", Protocol: "openai-image"}},
			Params: map[string]any{"response_format": `"url"`},
		}},
	}

	auth := newMinimaxTestAuth()
	auth.Metadata["base_url"] = srv.URL

	e := NewMinimaxExecutor(cfg)
	req := cliproxyexecutor.Request{
		Model:   "image-01",
		Payload: []byte(`{"model":"image-01","prompt":"a cat","response_format":"b64_json"}`),
	}
	if _, err := e.Execute(context.Background(), auth, req, minimaxImageOpts()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := gjson.GetBytes(sent, "response_format").String(); got != "url" {
		t.Fatalf("configured override must win, sent response_format = %q (body %s)", got, sent)
	}
}

// TestMinimaxImageAppliesPayloadFilterLast proves a configured filter can remove
// a field the built-in translation added.
func TestMinimaxImageAppliesPayloadFilterLast(t *testing.T) {
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"data":{"image_base64":["QUJD"]}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.Payload = config.PayloadConfig{
		Filter: []config.PayloadFilterRule{{
			Models: []config.PayloadModelRule{{Name: "image-01", Protocol: "openai-image"}},
			Params: []string{"seed"},
		}},
	}

	auth := newMinimaxTestAuth()
	auth.Metadata["base_url"] = srv.URL

	e := NewMinimaxExecutor(cfg)
	req := cliproxyexecutor.Request{
		Model:   "image-01",
		Payload: []byte(`{"model":"image-01","prompt":"a cat","seed":42}`),
	}
	if _, err := e.Execute(context.Background(), auth, req, minimaxImageOpts()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gjson.GetBytes(sent, "seed").Exists() {
		t.Fatalf("configured filter must remove the field, body = %s", sent)
	}
	if gjson.GetBytes(sent, "prompt").String() != "a cat" {
		t.Fatalf("unrelated fields must survive, body = %s", sent)
	}
}

// TestMinimaxImagePayloadNotMutatedByFinalizer guards against the finalizer
// leaking state back into the caller's payload.
func TestMinimaxImagePayloadNotMutatedByFinalizer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"image_base64":["QUJD"]}}`))
	}))
	defer srv.Close()

	auth := newMinimaxTestAuth()
	auth.Metadata["base_url"] = srv.URL

	original := []byte(`{"model":"image-01","prompt":"a cat","response_format":"b64_json"}`)
	snapshot := string(original)

	e := NewMinimaxExecutor(&config.Config{})
	_, err := e.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "image-01", Payload: original}, minimaxImageOpts())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(original) != snapshot {
		t.Fatalf("request payload was mutated:\nbefore %s\nafter  %s", snapshot, string(original))
	}
}

// TestMinimaxImageNotMatchedByOtherProviderRules ensures a MiniMax request is
// only affected by rules that target it.
func TestMinimaxImageNotMatchedByOtherProviderRules(t *testing.T) {
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"data":{"image_base64":["QUJD"]}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.Payload = config.PayloadConfig{
		OverrideRaw: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "grok-imagine-image", Protocol: "openai-image"}},
			Params: map[string]any{"prompt": `"should not apply"`},
		}},
	}

	auth := newMinimaxTestAuth()
	auth.Metadata["base_url"] = srv.URL
	e := NewMinimaxExecutor(cfg)
	req := cliproxyexecutor.Request{Model: "image-01", Payload: []byte(`{"model":"image-01","prompt":"a cat"}`)}
	if _, err := e.Execute(context.Background(), auth, req, minimaxImageOpts()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !json.Valid(sent) {
		t.Fatalf("payload must remain valid JSON: %s", sent)
	}
	if got := gjson.GetBytes(sent, "prompt").String(); got != "a cat" {
		t.Fatalf("another provider's rule must not apply, prompt = %q", got)
	}
}
