package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeToolResultContent(t *testing.T) {
	tests := []struct {
		name       string
		wrapper    string
		wantResult string
		wantRaw    bool
		wantInline int
	}{
		{
			name:       "StringContent",
			wrapper:    `{"content":"alpha"}`,
			wantResult: "alpha",
			wantRaw:    false,
			wantInline: 0,
		},
		{
			name:       "SingleTextBlock",
			wrapper:    `{"content":[{"type":"text","text":"alpha"}]}`,
			wantResult: `{"type":"text","text":"alpha"}`,
			wantRaw:    true,
			wantInline: 0,
		},
		{
			name:       "MultipleTextBlocks",
			wrapper:    `{"content":[{"type":"text","text":"alpha"},{"type":"text","text":"beta"}]}`,
			wantResult: `[{"type":"text","text":"alpha"},{"type":"text","text":"beta"}]`,
			wantRaw:    true,
			wantInline: 0,
		},
		{
			name:       "TextAndImage",
			wrapper:    `{"content":[{"type":"text","text":"alpha"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}`,
			wantResult: `{"type":"text","text":"alpha"}`,
			wantRaw:    true,
			wantInline: 1,
		},
		{
			name:       "ImageOnly",
			wrapper:    `{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}`,
			wantResult: "",
			wantRaw:    false,
			wantInline: 1,
		},
		{
			name:       "TextAndDocument",
			wrapper:    `{"content":[{"type":"text","text":"alpha"},{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"aGVsbG8="}}]}`,
			wantResult: `{"type":"text","text":"alpha"}`,
			wantRaw:    true,
			wantInline: 1,
		},
		{
			name:       "DocumentOnly",
			wrapper:    `{"content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"aGVsbG8="}}]}`,
			wantResult: "",
			wantRaw:    false,
			wantInline: 1,
		},
		{
			name:       "DocumentWithoutMediaTypeStaysAsJSON",
			wrapper:    `{"content":[{"type":"document","source":{"type":"base64","data":"aGVsbG8="}}]}`,
			wantResult: `{"type":"document","source":{"type":"base64","data":"aGVsbG8="}}`,
			wantRaw:    true,
			wantInline: 0,
		},
		{
			name:       "DocumentURLStaysAsJSON",
			wrapper:    `{"content":[{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"}}]}`,
			wantResult: `{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"}}`,
			wantRaw:    true,
			wantInline: 0,
		},
		{
			name:       "ImageWithoutDataDropped",
			wrapper:    `{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png"}}]}`,
			wantResult: "",
			wantRaw:    false,
			wantInline: 0,
		},
		{
			name:       "ObjectContent",
			wrapper:    `{"content":{"foo":"bar"}}`,
			wantResult: `{"foo":"bar"}`,
			wantRaw:    true,
			wantInline: 0,
		},
		{
			name:       "ObjectImage",
			wrapper:    `{"content":{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}}`,
			wantResult: "",
			wantRaw:    false,
			wantInline: 1,
		},
		{
			name:       "AbsentContent",
			wrapper:    `{}`,
			wantResult: "",
			wantRaw:    false,
			wantInline: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConvertClaudeToolResultContent(gjson.Get(tt.wrapper, "content"))
			if got.Result != tt.wantResult {
				t.Errorf("Result = %q, want %q", got.Result, tt.wantResult)
			}
			if got.ResultIsRaw != tt.wantRaw {
				t.Errorf("ResultIsRaw = %v, want %v", got.ResultIsRaw, tt.wantRaw)
			}
			if len(got.InlineData) != tt.wantInline {
				t.Errorf("len(InlineData) = %d, want %d", len(got.InlineData), tt.wantInline)
			}
		})
	}
}

func TestConvertClaudeToolResultContent_ImageFields(t *testing.T) {
	content := gjson.Get(`{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}`, "content")
	got := ConvertClaudeToolResultContent(content)
	if len(got.InlineData) != 1 {
		t.Fatalf("expected 1 image, got %d", len(got.InlineData))
	}
	if got.InlineData[0].MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", got.InlineData[0].MimeType)
	}
	if got.InlineData[0].Data != "aGVsbG8=" {
		t.Errorf("Data = %q, want aGVsbG8=", got.InlineData[0].Data)
	}
}

func TestConvertClaudeToolResultContent_DocumentFields(t *testing.T) {
	content := gjson.Get(`{"content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"aGVsbG8="}}]}`, "content")
	got := ConvertClaudeToolResultContent(content)
	if len(got.InlineData) != 1 {
		t.Fatalf("expected 1 inline document, got %d", len(got.InlineData))
	}
	if got.InlineData[0].MimeType != "application/pdf" || got.InlineData[0].Data != "aGVsbG8=" {
		t.Errorf("InlineData[0] = %+v, want application/pdf with aGVsbG8=", got.InlineData[0])
	}
}
