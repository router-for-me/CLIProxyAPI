package chat_completions

import (
	"context"
	"errors"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	mediaImageURL  = `{"type":"image_url","image_url":{"url":"https://x.test/a.png"}}`
	mediaImageData = `{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}`
	mediaVideoURL  = `{"type":"video_url","video_url":{"url":"https://x.test/a.mp4"}}`
	mediaAudioNone = `{"type":"input_audio","input_audio":{"data":"","format":"wav"}}`
)

func mediaTurn(parts string) []byte {
	return []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + parts + `]}]}`)
}

func TestConvertOpenAIRequestToAntigravity_RefusesAnAttachmentOnlyTurnItCannotSend(t *testing.T) {
	cases := []struct {
		name     string
		parts    string
		wantType string
	}{
		{name: "http image_url", parts: mediaImageURL, wantType: "image_url"},
		{name: "image_url without a base64 payload", parts: `{"type":"image_url","image_url":{"url":"data:image/png,raw"}}`, wantType: "image_url"},
		{name: "image_url as a bare string", parts: `{"type":"image_url","image_url":"https://x.test/a.png"}`, wantType: "image_url"},
		{name: "http video_url", parts: mediaVideoURL, wantType: "video_url"},
		{name: "input_audio without bytes", parts: mediaAudioNone, wantType: "input_audio"},
		{name: "whitespace text beside http image_url", parts: `{"type":"text","text":"  "},` + mediaImageURL, wantType: "image_url"},
		{name: "first dropped type is reported", parts: mediaVideoURL + `,` + mediaImageURL, wantType: "video_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := convertOpenAIRequestToAntigravity("m", mediaTurn(tc.parts), false)
			var unsupported *translatorcommon.UnsupportedPartError
			if !errors.As(err, &unsupported) || unsupported.Type != tc.wantType || unsupported.StatusCode() != 400 {
				t.Fatalf("err = %v, want unsupported content part: %s; body = %s", err, tc.wantType, body)
			}
		})
	}
}

func TestConvertOpenAIRequestToAntigravity_RealTextBesideAnUnsendableAttachmentStillSucceeds(t *testing.T) {
	for _, parts := range []string{
		userTurnText + `,` + mediaImageURL,
		mediaImageURL + `,` + userTurnText,
		`{"type":"text","text":"  "},` + userTurnText + `,` + mediaVideoURL + `,` + mediaAudioNone,
	} {
		body, err := convertOpenAIRequestToAntigravity("m", mediaTurn(parts), false)
		if err != nil {
			t.Fatalf("parts %s: err = %v, body = %s", parts, err, body)
		}
		found := false
		for _, part := range gjson.GetBytes(body, "request.contents.2.parts").Array() {
			if part.Get("text").String() == "keep me" {
				found = true
			}
		}
		if !found {
			t.Fatalf("parts %s: text was lost: %s", parts, body)
		}
	}
}

func TestConvertOpenAIRequestToAntigravity_DataURLImageStillConverts(t *testing.T) {
	body, err := convertOpenAIRequestToAntigravity("m", mediaTurn(mediaImageData), false)
	if err != nil {
		t.Fatalf("err = %v, body = %s", err, body)
	}
	part := gjson.GetBytes(body, "request.contents.2.parts.0.inlineData")
	if part.Get("mimeType").String() != "image/png" || part.Get("data").String() != "iVBORw0KGgo=" {
		t.Fatalf("data URL image was not inlined: %s", body)
	}
}

func TestOpenAIToAntigravityRegistryCarriesTheImageURLRefusal(t *testing.T) {
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatOpenAI,
		Model:  "m",
		Body:   mediaTurn(mediaImageURL),
	})
	var unsupported *translatorcommon.UnsupportedPartError
	if !errors.As(envelope.Err, &unsupported) || unsupported.Type != "image_url" {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
}
