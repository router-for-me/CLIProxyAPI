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
		wantImages int
	}{
		{
			name:       "StringContent",
			wrapper:    `{"content":"alpha"}`,
			wantResult: "alpha",
			wantRaw:    false,
			wantImages: 0,
		},
		{
			name:       "SingleTextBlock",
			wrapper:    `{"content":[{"type":"text","text":"alpha"}]}`,
			wantResult: `{"type":"text","text":"alpha"}`,
			wantRaw:    true,
			wantImages: 0,
		},
		{
			name:       "MultipleTextBlocks",
			wrapper:    `{"content":[{"type":"text","text":"alpha"},{"type":"text","text":"beta"}]}`,
			wantResult: `[{"type":"text","text":"alpha"},{"type":"text","text":"beta"}]`,
			wantRaw:    true,
			wantImages: 0,
		},
		{
			name:       "TextAndImage",
			wrapper:    `{"content":[{"type":"text","text":"alpha"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}`,
			wantResult: `{"type":"text","text":"alpha"}`,
			wantRaw:    true,
			wantImages: 1,
		},
		{
			name:       "ImageOnly",
			wrapper:    `{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}`,
			wantResult: "",
			wantRaw:    false,
			wantImages: 1,
		},
		{
			name:       "ImageWithoutDataDropped",
			wrapper:    `{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png"}}]}`,
			wantResult: "",
			wantRaw:    false,
			wantImages: 0,
		},
		{
			name:       "ObjectContent",
			wrapper:    `{"content":{"foo":"bar"}}`,
			wantResult: `{"foo":"bar"}`,
			wantRaw:    true,
			wantImages: 0,
		},
		{
			name:       "ObjectImage",
			wrapper:    `{"content":{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}}`,
			wantResult: "",
			wantRaw:    false,
			wantImages: 1,
		},
		{
			name:       "AbsentContent",
			wrapper:    `{}`,
			wantResult: "",
			wantRaw:    false,
			wantImages: 0,
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
			if len(got.Images) != tt.wantImages {
				t.Errorf("len(Images) = %d, want %d", len(got.Images), tt.wantImages)
			}
		})
	}
}

func TestConvertClaudeToolResultContent_ImageFields(t *testing.T) {
	content := gjson.Get(`{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}`, "content")
	got := ConvertClaudeToolResultContent(content)
	if len(got.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(got.Images))
	}
	if got.Images[0].MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", got.Images[0].MimeType)
	}
	if got.Images[0].Data != "aGVsbG8=" {
		t.Errorf("Data = %q, want aGVsbG8=", got.Images[0].Data)
	}
}
