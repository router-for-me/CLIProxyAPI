package responses

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// The dev golden was produced by running renderToolSearchGolden against the
// translator at origin/dev d739485df, before Codex tool_search support. With
// upstream.claude.tool-search off the translator must reproduce it exactly.
type toolSearchGoldenCase struct {
	Name    string          `json:"name"`
	Request json.RawMessage `json:"request"`
}

type toolSearchGoldenFixtures struct {
	Stream []string               `json:"stream"`
	Cases  []toolSearchGoldenCase `json:"cases"`
}

type toolSearchGoldenOutput struct {
	Request      string   `json:"request"`
	RequestError string   `json:"request_error,omitempty"`
	Stream       []string `json:"stream"`
	NonStream    string   `json:"non_stream"`
}

var toolSearchGoldenCreatedAt = regexp.MustCompile(`"created_at":\d+`)

// renderToolSearchGolden runs one fixture through the request translator and
// both response translators, masking the wall-clock created_at field.
func renderToolSearchGolden(fixtures toolSearchGoldenFixtures, c toolSearchGoldenCase) toolSearchGoldenOutput {
	normalize := func(raw []byte) string {
		return toolSearchGoldenCreatedAt.ReplaceAllString(string(raw), `"created_at":0`)
	}
	translated, errConvert := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", c.Request, true)
	result := toolSearchGoldenOutput{Request: string(translated)}
	if errConvert != nil {
		result.RequestError = errConvert.Error()
	}
	var param any
	for _, chunk := range fixtures.Stream {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", c.Request, translated, []byte(chunk), &param) {
			result.Stream = append(result.Stream, normalize(output))
		}
	}
	result.NonStream = normalize(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", c.Request, translated, []byte(strings.Join(fixtures.Stream, "\n")), nil))
	return result
}

func loadToolSearchGolden(t *testing.T) (toolSearchGoldenFixtures, map[string]toolSearchGoldenOutput) {
	t.Helper()
	dir := filepath.Join("testdata", "tool_search_disabled")
	var fixtures toolSearchGoldenFixtures
	var golden map[string]toolSearchGoldenOutput
	for name, target := range map[string]any{"cases.json": &fixtures, "dev_golden.json": &golden} {
		raw, errRead := os.ReadFile(filepath.Join(dir, name))
		if errRead != nil {
			t.Fatalf("read %s: %v", name, errRead)
		}
		if errUnmarshal := json.Unmarshal(raw, target); errUnmarshal != nil {
			t.Fatalf("decode %s: %v", name, errUnmarshal)
		}
	}
	if len(fixtures.Cases) == 0 || len(golden) != len(fixtures.Cases) {
		t.Fatalf("golden has %d entries for %d cases", len(golden), len(fixtures.Cases))
	}
	return fixtures, golden
}

func TestToolSearchDisabledMatchesDevGolden(t *testing.T) {
	setClientToolSearchEnabledForTest(t, false)
	fixtures, golden := loadToolSearchGolden(t)
	for _, c := range fixtures.Cases {
		t.Run(c.Name, func(t *testing.T) {
			want, ok := golden[c.Name]
			if !ok {
				t.Fatalf("no dev golden for %s", c.Name)
			}
			got := renderToolSearchGolden(fixtures, c)
			if got.Request != want.Request || got.RequestError != want.RequestError {
				t.Fatalf("request differs from dev\n got: %s %s\nwant: %s %s", got.Request, got.RequestError, want.Request, want.RequestError)
			}
			if strings.Join(got.Stream, "\n") != strings.Join(want.Stream, "\n") {
				t.Fatalf("stream differs from dev\n got: %v\nwant: %v", got.Stream, want.Stream)
			}
			if got.NonStream != want.NonStream {
				t.Fatalf("non-stream differs from dev\n got: %s\nwant: %s", got.NonStream, want.NonStream)
			}
		})
	}
}

// With the switch on, the same fixtures must take the tool_search paths, so
// the golden comparison above cannot pass by accident.
func TestToolSearchEnabledDiffersFromDevGolden(t *testing.T) {
	setClientToolSearchEnabledForTest(t, true)
	fixtures, golden := loadToolSearchGolden(t)
	for _, c := range fixtures.Cases {
		if c.Name != "codex_session" {
			continue
		}
		if got := renderToolSearchGolden(fixtures, c); got.Request == golden[c.Name].Request {
			t.Fatal("codex_session translated as dev with tool search enabled")
		}
		return
	}
	t.Fatal("codex_session fixture missing")
}

// A value pinned in the request context wins over the process switch for the
// request translation and for both response translators, so a config reload
// while a request runs cannot mix the two behaviors.
func TestToolSearchPinnedContextWinsOverSwitch(t *testing.T) {
	fixtures, golden := loadToolSearchGolden(t)
	var session toolSearchGoldenCase
	for _, c := range fixtures.Cases {
		if c.Name == "codex_session" {
			session = c
		}
	}
	if session.Name == "" {
		t.Fatal("codex_session fixture missing")
	}
	translate := func(ctx context.Context) []byte {
		t.Helper()
		out := ConvertOpenAIResponsesRequestEnvelopeToClaude(ctx, sdktranslator.RequestEnvelope{Model: "claude-sonnet-4-5", Stream: true, Body: session.Request})
		if out.Err != nil {
			t.Fatalf("translate: %v", out.Err)
		}
		return out.Body
	}
	streamItemTypes := func(ctx context.Context, translated []byte) string {
		t.Helper()
		var param any
		var types []string
		for _, chunk := range fixtures.Stream {
			for _, output := range ConvertClaudeResponseToOpenAIResponses(ctx, "claude-test", session.Request, translated, []byte(chunk), &param) {
				if item := gjson.GetBytes(sseData(output), "item"); gjson.GetBytes(sseData(output), "type").String() == "response.output_item.done" {
					types = append(types, item.Get("type").String())
				}
			}
		}
		return strings.Join(types, ",")
	}
	nonStreamFirstType := func(ctx context.Context, translated []byte) string {
		out := ConvertClaudeResponseToOpenAIResponsesNonStream(ctx, "claude-test", session.Request, translated, []byte(strings.Join(fixtures.Stream, "\n")), nil)
		return gjson.GetBytes(out, "output.0.type").String()
	}

	setClientToolSearchEnabledForTest(t, false)
	pinnedOn := WithClientToolSearch(context.Background(), true)
	if translated := translate(pinnedOn); !gjson.GetBytes(translated, `tools.#(name=="tool_search")`).Exists() {
		t.Fatalf("pinned-on request lost tool_search with the switch off: %s", translated)
	}
	if got := streamItemTypes(pinnedOn, translate(pinnedOn)); !strings.HasPrefix(got, "tool_search_call,") {
		t.Fatalf("pinned-on stream items = %s, want a tool_search_call first", got)
	}
	if got := nonStreamFirstType(pinnedOn, translate(pinnedOn)); got != "tool_search_call" {
		t.Fatalf("pinned-on non-stream first item = %s, want tool_search_call", got)
	}

	setClientToolSearchEnabledForTest(t, true)
	pinnedOff := WithClientToolSearch(context.Background(), false)
	if got := string(translate(pinnedOff)); got != golden[session.Name].Request {
		t.Fatalf("pinned-off request differs from dev with the switch on\n got: %s\nwant: %s", got, golden[session.Name].Request)
	}
	if got := streamItemTypes(pinnedOff, translate(pinnedOff)); strings.Contains(got, "tool_search_call") {
		t.Fatalf("pinned-off stream items = %s, want no tool_search_call", got)
	}
	if got := nonStreamFirstType(pinnedOff, translate(pinnedOff)); got != "function_call" {
		t.Fatalf("pinned-off non-stream first item = %s, want function_call", got)
	}
}

// sseData returns the JSON payload of one translated SSE event.
func sseData(event []byte) []byte {
	for _, line := range strings.Split(string(event), "\n") {
		if strings.HasPrefix(line, "data: ") {
			return []byte(strings.TrimPrefix(line, "data: "))
		}
	}
	return nil
}

// The compatibility entry point used for API-key compat models follows the
// pinned value too.
func TestToolSearchCompatContextFollowsPin(t *testing.T) {
	request := codexToolSearchRequest(codexUserTurn, codexSearchCall1, codexSearchOutput1)
	for _, tc := range []struct {
		global, pinned bool
	}{{global: false, pinned: true}, {global: true, pinned: false}} {
		setClientToolSearchEnabledForTest(t, tc.global)
		out, errConvert := ConvertOpenAIResponsesRequestToClaudeWithCompatContext(WithClientToolSearch(context.Background(), tc.pinned), "claude-sonnet-4-5", request, true)
		if errConvert != nil {
			t.Fatalf("convert: %v", errConvert)
		}
		if got := gjson.GetBytes(out, `tools.#(name=="tool_search")`).Exists(); got != tc.pinned {
			t.Fatalf("global=%v pinned=%v: tool_search declared = %v", tc.global, tc.pinned, got)
		}
	}
}
