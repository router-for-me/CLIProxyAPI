package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGetVertexActionForImagen(t *testing.T) {
	if !isImagenModel("imagen-4.0-fast-generate-001") {
		t.Fatalf("expected imagen model detection to be true")
	}
	if got := getVertexAction("imagen-4.0-fast-generate-001", false); got != "predict" {
		t.Fatalf("getVertexAction(non-stream) = %q, want %q", got, "predict")
	}
	if got := getVertexAction("imagen-4.0-fast-generate-001", true); got != "predict" {
		t.Fatalf("getVertexAction(stream) = %q, want %q", got, "predict")
	}
}

func TestConvertToImagenRequestFromContents(t *testing.T) {
	payload := []byte(`{
		"contents":[{"parts":[{"text":"draw a red robot"}]}],
		"aspectRatio":"16:9",
		"sampleCount":2,
		"negativePrompt":"blurry"
	}`)

	got, err := convertToImagenRequest(payload)
	if err != nil {
		t.Fatalf("convertToImagenRequest returned error: %v", err)
	}
	res := gjson.ParseBytes(got)

	if prompt := res.Get("instances.0.prompt").String(); prompt != "draw a red robot" {
		t.Fatalf("instances.0.prompt = %q, want %q", prompt, "draw a red robot")
	}
	if ar := res.Get("parameters.aspectRatio").String(); ar != "16:9" {
		t.Fatalf("parameters.aspectRatio = %q, want %q", ar, "16:9")
	}
	if sc := res.Get("parameters.sampleCount").Int(); sc != 2 {
		t.Fatalf("parameters.sampleCount = %d, want %d", sc, 2)
	}
	if np := res.Get("instances.0.negativePrompt").String(); np != "blurry" {
		t.Fatalf("instances.0.negativePrompt = %q, want %q", np, "blurry")
	}
}

func TestConvertImagenToGeminiResponse(t *testing.T) {
	input := []byte(`{
		"predictions":[
			{"bytesBase64Encoded":"abc123","mimeType":"image/png"}
		]
	}`)

	got := convertImagenToGeminiResponse(input, "imagen-4.0-fast-generate-001")
	res := gjson.ParseBytes(got)

	if mime := res.Get("candidates.0.content.parts.0.inlineData.mimeType").String(); mime != "image/png" {
		t.Fatalf("inlineData.mimeType = %q, want %q", mime, "image/png")
	}
	if data := res.Get("candidates.0.content.parts.0.inlineData.data").String(); data != "abc123" {
		t.Fatalf("inlineData.data = %q, want %q", data, "abc123")
	}
	if !strings.HasPrefix(res.Get("responseId").String(), "imagen-") {
		t.Fatalf("expected responseId to start with imagen-, got %q", res.Get("responseId").String())
	}
}
