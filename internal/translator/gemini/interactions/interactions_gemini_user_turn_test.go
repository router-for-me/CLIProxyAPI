package interactions

import (
	"context"
	"errors"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// geminiTurnToInteractions translates two history turns followed by a final user
// turn holding the given parts. extra is spliced into the request root.
func geminiTurnToInteractions(extra, finalParts string) sdktranslator.RequestEnvelope {
	input := `{"model":"m",` + extra + `"contents":[{"role":"user","parts":[{"text":"a"}]},{"role":"model","parts":[{"text":"b"}]},{"role":"user","parts":[` + finalParts + `]}]}`
	return sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatGemini, sdktranslator.FormatInteractions, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatGemini,
		Model:  "m",
		Body:   []byte(input),
	})
}

func requireFileDataRefusal(t *testing.T, envelope sdktranslator.RequestEnvelope) {
	t.Helper()
	var unsupported *translatorcommon.UnsupportedPartError
	if !errors.As(envelope.Err, &unsupported) || unsupported.Type != "fileData" || unsupported.StatusCode() != 400 {
		t.Fatalf("envelope.Err = %v, want unsupported content part: fileData; body = %s", envelope.Err, envelope.Body)
	}
	if got, want := envelope.Err.Error(), "unsupported content part: fileData"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
	if !gjson.ValidBytes(envelope.Body) {
		t.Fatalf("body is not JSON: %q", envelope.Body)
	}
}

func TestGeminiToInteractionsFileDataOnlyTurnKeepsTheFile(t *testing.T) {
	cases := []struct {
		name     string
		part     string
		wantType string
		wantURI  string
		wantMIME string
	}{
		{
			name:     "camel case pdf",
			part:     `{"fileData":{"mimeType":"application/pdf","fileUri":"gs://bucket/doc.pdf"}}`,
			wantType: "document",
			wantURI:  "gs://bucket/doc.pdf",
			wantMIME: "application/pdf",
		},
		{
			name:     "snake case video",
			part:     `{"file_data":{"mime_type":"video/mp4","file_uri":"https://example.test/a.mp4"}}`,
			wantType: "video",
			wantURI:  "https://example.test/a.mp4",
			wantMIME: "video/mp4",
		},
		{
			name:     "image",
			part:     `{"fileData":{"mimeType":"image/png","fileUri":"https://example.test/a.png"}}`,
			wantType: "image",
			wantURI:  "https://example.test/a.png",
			wantMIME: "image/png",
		},
		{
			name:     "without a mime type",
			part:     `{"fileData":{"fileUri":"https://example.test/a"}}`,
			wantType: "document",
			wantURI:  "https://example.test/a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := geminiTurnToInteractions("", tc.part)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			if got := gjson.GetBytes(envelope.Body, "input.#").Int(); got != 3 {
				t.Fatalf("input has %d steps, want 3: %s", got, envelope.Body)
			}
			part := gjson.GetBytes(envelope.Body, "input.2.content.0")
			if part.Get("type").String() != tc.wantType || part.Get("uri").String() != tc.wantURI || part.Get("mime_type").String() != tc.wantMIME {
				t.Fatalf("part = %s, body = %s", part.Raw, envelope.Body)
			}
		})
	}
}

func TestGeminiToInteractionsFileDataWithoutURIIsRefused(t *testing.T) {
	cases := map[string]struct {
		extra string
		parts string
	}{
		"history then file only": {
			parts: `{"fileData":{"mimeType":"application/pdf"}}`,
		},
		"snake case spelling": {
			parts: `{"file_data":{"mime_type":"application/pdf","file_uri":""}}`,
		},
		"system instruction does not hide it": {
			extra: `"systemInstruction":{"parts":[{"text":"be brief"}]},`,
			parts: `{"fileData":{"mimeType":"application/pdf"}}`,
		},
		"empty text does not hide it": {
			parts: `{"text":""},{"fileData":{"mimeType":"application/pdf"}}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			requireFileDataRefusal(t, geminiTurnToInteractions(tc.extra, tc.parts))
		})
	}
}

func TestGeminiToInteractionsRefusesAnEmptiedTurnBeforeALaterTextTurn(t *testing.T) {
	input := `{"model":"m","contents":[{"role":"user","parts":[{"fileData":{"mimeType":"application/pdf"}}]},{"role":"model","parts":[{"text":"ok"}]},{"role":"user","parts":[{"text":"next"}]}]}`
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatGemini, sdktranslator.FormatInteractions, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatGemini,
		Model:  "m",
		Body:   []byte(input),
	})
	requireFileDataRefusal(t, envelope)
}

func TestGeminiToInteractionsFileDataWithoutURIBesideTextStillSucceeds(t *testing.T) {
	envelope := geminiTurnToInteractions("", `{"text":"keep me"},{"fileData":{"mimeType":"application/pdf"}}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "input.2.content.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "input.#").Int(); got != 3 {
		t.Fatalf("input has %d steps, want 3: %s", got, envelope.Body)
	}
}

func TestGeminiToInteractionsInlineDataOnlyTurnStillConverts(t *testing.T) {
	envelope := geminiTurnToInteractions("", `{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0xLjQK"}}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	part := gjson.GetBytes(envelope.Body, "input.2.content.0")
	if part.Get("type").String() != "document" || part.Get("mime_type").String() != "application/pdf" || part.Get("data").String() != "JVBERi0xLjQK" {
		t.Fatalf("part = %s, body = %s", part.Raw, envelope.Body)
	}
}

func TestGeminiToInteractionsModelFileDataWithoutURIIsNotARefusal(t *testing.T) {
	input := `{"model":"m","contents":[{"role":"user","parts":[{"text":"a"}]},{"role":"model","parts":[{"fileData":{"mimeType":"application/pdf"}}]},{"role":"user","parts":[{"text":"b"}]}]}`
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatGemini, sdktranslator.FormatInteractions, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatGemini,
		Model:  "m",
		Body:   []byte(input),
	})
	if envelope.Err != nil {
		t.Fatalf("only user turns are refused, envelope.Err = %v", envelope.Err)
	}
}

func TestGeminiToInteractionsExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := []byte(`{"model":"m","contents":[{"role":"user","parts":[{"fileData":{"mimeType":"application/pdf"}}]}]}`)
	if body, _ := ConvertGeminiRequestToInteractions("m", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}
