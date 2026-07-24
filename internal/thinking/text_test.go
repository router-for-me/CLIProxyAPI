package thinking

import (
	"testing"
)

func TestStripThoughtTags(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantThinking string
		wantClean    string
	}{
		{
			name:         "no tags",
			input:        "Hello, world!",
			wantThinking: "",
			wantClean:    "Hello, world!",
		},
		{
			name:         "single thought tag",
			input:        "<thought>let me think about this</thought>Here is the answer",
			wantThinking: "let me think about this",
			wantClean:    "Here is the answer",
		},
		{
			name:         "only thought content",
			input:        "<thought>only reasoning</thought>",
			wantThinking: "only reasoning",
			wantClean:    "",
		},
		{
			name:         "think tag variant",
			input:        "<think>reasoning with think tag</think>The answer",
			wantThinking: "reasoning with think tag",
			wantClean:    "The answer",
		},
		{
			name:         "multiple tags",
			input:        "<thought>first reasoning</thought>text between<thought>second reasoning</thought>final answer",
			wantThinking: "first reasoning\nsecond reasoning",
			wantClean:    "text between final answer",
		},
		{
			name:         "multiline thought",
			input:        "<thought>\nmulti\nline\nreasoning\n</thought>\nanswer",
			wantThinking: "multi\nline\nreasoning",
			wantClean:    "answer",
		},
		{
			name:         "mixed tag types",
			input:        "<thought>some thought</thought>visible<think>some think</think>more visible",
			wantThinking: "some thought\nsome think",
			wantClean:    "visible more visible",
		},
		{
			name:         "empty thought tag",
			input:        "<thought></thought>clean text",
			wantThinking: "",
			wantClean:    "clean text",
		},
		{
			name:         "Chinese content",
			input:        "<thought>让我想一想</thought>这是回答",
			wantThinking: "让我想一想",
			wantClean:    "这是回答",
		},
		{
			name:         "code blocks with angle brackets",
			input:        "Here is code: `<div>` and `</div>` in HTML",
			wantThinking: "",
			wantClean:    "Here is code: `<div>` and `</div>` in HTML",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotThinking, gotClean := StripThoughtTags(tt.input)
			if gotThinking != tt.wantThinking {
				t.Errorf("StripThoughtTags() thinking = %q, want %q", gotThinking, tt.wantThinking)
			}
			if gotClean != tt.wantClean {
				t.Errorf("StripThoughtTags() clean = %q, want %q", gotClean, tt.wantClean)
			}
		})
	}
}

func TestThoughtTagStripper_SingleChunk(t *testing.T) {
	s := NewThoughtTagStripper()

	thinking, visible := s.Feed("<thought>reasoning</thought>answer")
	if thinking != "reasoning" {
		t.Errorf("thinking = %q, want %q", thinking, "reasoning")
	}
	if visible != "answer" {
		t.Errorf("visible = %q, want %q", visible, "answer")
	}
}

func TestThoughtTagStripper_MultiChunk(t *testing.T) {
	s := NewThoughtTagStripper()

	// Chunk 1: Opening tag starts, partial
	t1, v1 := s.Feed("<thou")
	if t1 != "" || v1 != "" {
		t.Errorf("chunk 1: thinking=%q visible=%q, want both empty", t1, v1)
	}

	// Chunk 2: Complete opening tag + partial content
	t2, v2 := s.Feed("ght>reason")
	if t2 != "" || v2 != "" {
		t.Errorf("chunk 2: thinking=%q visible=%q, want both empty", t2, v2)
	}

	// Chunk 3: More content
	t3, v3 := s.Feed("ing")
	if t3 != "" || v3 != "" {
		t.Errorf("chunk 3: thinking=%q visible=%q, want both empty", t3, v3)
	}

	// Chunk 4: Close tag + visible text
	t4, v4 := s.Feed("</thought>visible")
	if t4 != "reasoning" {
		t.Errorf("chunk 4 thinking = %q, want %q", t4, "reasoning")
	}
	if v4 != "visible" {
		t.Errorf("chunk 4 visible = %q, want %q", v4, "visible")
	}
}

func TestThoughtTagStripper_Flush(t *testing.T) {
	s := NewThoughtTagStripper()

	// Feed incomplete thought tag
	s.Feed("<thought>unterminated thinking")
	thinking, visible := s.Flush()

	if thinking != "unterminated thinking" {
		t.Errorf("flush thinking = %q, want %q", thinking, "unterminated thinking")
	}
	if visible != "" {
		t.Errorf("flush visible = %q, want empty", visible)
	}
}

func TestThoughtTagStripper_Reset(t *testing.T) {
	s := NewThoughtTagStripper()

	s.Feed("<thought>some reasoning")
	s.Reset()

	thinking, visible := s.Feed("clean text")
	if thinking != "" {
		t.Errorf("after reset: thinking = %q, want empty", thinking)
	}
	if visible != "clean text" {
		t.Errorf("after reset: visible = %q, want %q", visible, "clean text")
	}
}

func TestThoughtTagStripper_ComplexStream(t *testing.T) {
	s := NewThoughtTagStripper()

	chunks := []string{
		"Hello ",
		"<thought>",
		"let me ",
		"think",
		" about ",
		"this</thought>",
		" world",
	}

	var allThinking, allVisible string
	for _, chunk := range chunks {
		tv, vs := s.Feed(chunk)
		if tv != "" {
			if allThinking != "" {
				allThinking += "\n"
			}
			allThinking += tv
		}
		if vs != "" {
			if allVisible != "" {
				allVisible += " "
			}
			allVisible += vs
		}
	}
	flushT, flushV := s.Flush()
	if flushT != "" {
		if allThinking != "" {
			allThinking += "\n"
		}
		allThinking += flushT
	}
	if flushV != "" {
		if allVisible != "" {
			allVisible += " "
		}
		allVisible += flushV
	}

	if allThinking != "let me think about this" {
		t.Errorf("allThinking = %q, want %q", allThinking, "let me think about this")
	}
	if allVisible != "Hello world" {
		t.Errorf("allVisible = %q, want %q", allVisible, "Hello world")
	}
}

func TestThoughtTagStripper_VisibleBeforeTag(t *testing.T) {
	s := NewThoughtTagStripper()

	thinking, visible := s.Feed("visible before<thought>hidden</thought>visible after")
	if thinking != "hidden" {
		t.Errorf("thinking = %q, want %q", thinking, "hidden")
	}
	if visible != "visible before visible after" {
		t.Errorf("visible = %q, want %q", visible, "visible before visible after")
	}
}

func TestThoughtTagStripper_SpanAcrossChunks(t *testing.T) {
	s := NewThoughtTagStripper()

	// Partial text, then opening tag starts
	t1, v1 := s.Feed("visible1<tho")
	if v1 != "visible1" {
		t.Errorf("chunk 1 visible = %q, want %q", v1, "visible1")
	}
	if t1 != "" {
		t.Errorf("chunk 1 thinking = %q, want empty", t1)
	}

	// Complete thought tag + close, plus more visible
	t2, v2 := s.Feed("ught>hidden</thought>visible2")
	if t2 != "hidden" {
		t.Errorf("chunk 2 thinking = %q, want %q", t2, "hidden")
	}
	if v2 != "visible2" {
		t.Errorf("chunk 2 visible = %q, want %q", v2, "visible2")
	}
}

func TestThoughtTagStripper_NoTags(t *testing.T) {
	s := NewThoughtTagStripper()

	// Plain text, no tags at all
	thinking, visible := s.Feed("hello world")
	if thinking != "" {
		t.Errorf("thinking = %q, want empty", thinking)
	}
	if visible != "hello world" {
		t.Errorf("visible = %q, want %q", visible, "hello world")
	}
}

func TestThoughtTagStripper_TagThenCloseInNextChunk(t *testing.T) {
	s := NewThoughtTagStripper()

	// Full opening tag, no close
	t1, v1 := s.Feed("<thought>reason")
	if t1 != "" || v1 != "" {
		t.Errorf("chunk 1: thinking=%q visible=%q, want both empty", t1, v1)
	}

	// Close tag
	t2, v2 := s.Feed("ing</thought>")
	if t2 != "reasoning" {
		t.Errorf("chunk 2 thinking = %q, want %q", t2, "reasoning")
	}
	if v2 != "" {
		t.Errorf("chunk 2 visible = %q, want empty", v2)
	}
}
