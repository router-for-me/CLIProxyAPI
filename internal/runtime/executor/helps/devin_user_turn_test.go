package helps

import (
	"errors"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

func TestCheckDevinUserTurnsRefusesAPromptEmptiedByUnsendableMedia(t *testing.T) {
	cases := map[string][]DevinPrompt{
		"only the media prompt": {
			{Source: 1, DroppedPart: "image"},
		},
		"after earlier turns": {
			{Source: 1, Content: "a"},
			{Source: 2, Content: "b"},
			{Source: 1, DroppedPart: "image"},
		},
		"whitespace text does not count": {
			{Source: 1, Content: " \n\t"},
			{Source: 1, DroppedPart: "image"},
		},
		"before a later text turn": {
			{Source: 1, DroppedPart: "image"},
			{Source: 2, Content: "ok"},
			{Source: 1, Content: "next"},
		},
	}
	for name, prompts := range cases {
		t.Run(name, func(t *testing.T) {
			kept, err := CheckDevinUserTurns(prompts)
			var unsupported *translatorcommon.UnsupportedPartError
			if !errors.As(err, &unsupported) || unsupported.Type != "image" || unsupported.StatusCode() != 400 {
				t.Fatalf("err = %v, want unsupported content part: image", err)
			}
			if kept != nil {
				t.Fatalf("kept = %#v, want nil on refusal", kept)
			}
		})
	}
}

func TestCheckDevinUserTurnsKeepsATurnWithOtherContent(t *testing.T) {
	cases := map[string]struct {
		prompts []DevinPrompt
		want    int
	}{
		"text beside the media in one prompt": {
			prompts: []DevinPrompt{{Source: 1, Content: "keep me", DroppedPart: "audio"}},
			want:    1,
		},
		"text in a neighbouring prompt": {
			prompts: []DevinPrompt{{Source: 1, Content: "keep me"}, {Source: 1, DroppedPart: "audio"}},
			want:    1,
		},
		"inline image in a neighbouring prompt": {
			prompts: []DevinPrompt{{Source: 1, DroppedPart: "audio"}, {Source: 1, Images: []DevinImage{{Base64Data: "aGk=", MimeType: "image/png"}}}},
			want:    1,
		},
		"tool result in the same turn": {
			prompts: []DevinPrompt{{Source: 2, ToolCalls: []DevinToolCall{{ID: "c1"}}}, {Source: 4, ToolCallID: "c1", Content: "{}"}, {Source: 1, DroppedPart: "audio"}},
			want:    2,
		},
		"orphaned tool result in the same turn": {
			prompts: []DevinPrompt{{Source: 1, IsOrphanedTool: true, Content: "{}"}, {Source: 1, DroppedPart: "audio"}},
			want:    1,
		},
		"prompt without dropped media is left alone": {
			prompts: []DevinPrompt{{Source: 1, Content: ""}},
			want:    1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kept, err := CheckDevinUserTurns(tc.prompts)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if len(kept) != tc.want {
				t.Fatalf("kept %d prompts, want %d: %#v", len(kept), tc.want, kept)
			}
			for _, prompt := range kept {
				if prompt.Source == 1 && !prompt.IsOrphanedTool && prompt.DroppedPart != "" && prompt.Content == "" && len(prompt.Images) == 0 {
					t.Fatalf("an empty user prompt was kept: %#v", prompt)
				}
			}
		})
	}
}
