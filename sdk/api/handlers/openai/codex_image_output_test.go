package openai

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const onePixelPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9ZQMcAAAAASUVORK5CYII="

func TestResponsesSSEFramerPersistsImageAndInjectsMarkdown(t *testing.T) {
	outputDir := t.TempDir()
	framer := &responsesSSEFramer{imageOutputDir: outputDir}
	var output bytes.Buffer

	framer.WriteChunk(&output, []byte(fmt.Sprintf("event: response.output_item.done\n"+
		"data: {\"type\":\"response.output_item.done\",\"sequence_number\":6,\"output_index\":0,\"item\":{\"id\":\"ig-1\",\"type\":\"image_generation_call\",\"status\":\"generating\",\"result\":%q}}\n\n", onePixelPNGBase64)))
	framer.WriteChunk(&output, []byte("event: response.output_text.done\n"+
		"data: {\"type\":\"response.output_text.done\",\"sequence_number\":7,\"output_index\":1,\"content_index\":0,\"text\":\"\"}\n\n"))

	imagePath := filepath.Join(outputDir, "ig-1.png")
	imageData, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read persisted image: %v", err)
	}
	if !bytes.HasPrefix(imageData, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("persisted image is not a PNG: %x", imageData[:min(len(imageData), 8)])
	}

	frames := strings.Split(strings.TrimSpace(output.String()), "\n\n")
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3; output=%q", len(frames), output.String())
	}
	payload, ok := responsesSSEDataPayload([]byte(frames[2]))
	if !ok {
		t.Fatalf("text frame has no data payload: %q", frames[2])
	}
	text := gjson.GetBytes(payload, "text").String()
	if !strings.Contains(text, "![Generated image 1]("+imagePath+")") {
		t.Fatalf("text does not contain generated image Markdown: %q", text)
	}
}

func TestResolveCodexImageOutputDirExpandsHome(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	resolved, err := resolveCodexImageOutputDir("~/.codex/generated_images/cliproxy")
	if err != nil {
		t.Fatalf("resolveCodexImageOutputDir: %v", err)
	}
	want := filepath.Join(homeDir, ".codex", "generated_images", "cliproxy")
	if resolved != want {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}
}
