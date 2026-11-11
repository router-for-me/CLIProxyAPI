package payloadcompress

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

const mcpDescriptionFiller = "This tool is basically used in order to read a file. " +
	"It is really important that you actually provide a path.  Please note that it must exist."

func TestMCPDescriptionIsItsOwnToggle(t *testing.T) {
	// Independent of the compression master toggle, which stays off here.
	if (Config{}).MCPDescriptionEnabledOn() {
		t.Fatalf("mcp description compression is opt-in in this port")
	}
	if !(Config{MCPDescriptionEnabled: true}).MCPDescriptionEnabledOn() {
		t.Fatalf("enabled mcp description compression must run without Enabled")
	}
}

func TestMCPDescriptionCompressionShortensBothShapes(t *testing.T) {
	body := `{"tools":[` +
		`{"name":"read_file","description":"` + mcpDescriptionFiller + `"},` +
		`{"type":"function","function":{"name":"read_file","description":"` + mcpDescriptionFiller + `"}}]}`
	doc := ojson.NewDocString(body)
	if n := ApplyMCPDescriptionCompression(doc); n != 2 {
		t.Fatalf("compressed %d descriptions, want 2", n)
	}
	for _, path := range []string{"tools.0.description", "tools.1.function.description"} {
		got := doc.Get(path).String()
		if got == mcpDescriptionFiller {
			t.Fatalf("%s left unchanged", path)
		}
		if len(got) >= len(mcpDescriptionFiller) {
			t.Fatalf("%s not shorter: %d >= %d", path, len(got), len(mcpDescriptionFiller))
		}
	}
}

func TestMCPDescriptionCompressionKeepsProtectedSpans(t *testing.T) {
	desc := "This tool is basically used in order to fetch https://example.com/a?b=c and it is very important that you actually keep the code `x  ==  y` as it is."
	out, ok := compressMCPDescription(desc)
	if !ok {
		t.Fatalf("description should shorten")
	}
	if !strings.Contains(out, "https://example.com/a?b=c") {
		t.Fatalf("URL not preserved: %q", out)
	}
	if !strings.Contains(out, "`x  ==  y`") {
		t.Fatalf("inline code not preserved: %q", out)
	}
}

func TestMCPDescriptionCompressionNoop(t *testing.T) {
	if n := ApplyMCPDescriptionCompression(nil); n != 0 {
		t.Fatalf("nil doc must be a no-op, got %d", n)
	}
	short := ojson.NewDocString(`{"tools":[{"name":"x","description":"read-file"}]}`)
	if n := ApplyMCPDescriptionCompression(short); n != 0 {
		t.Fatalf("already terse description rewritten (%d)", n)
	}
	noTools := ojson.NewDocString(`{"messages":[]}`)
	if n := ApplyMCPDescriptionCompression(noTools); n != 0 {
		t.Fatalf("body without tools rewritten (%d)", n)
	}
}
