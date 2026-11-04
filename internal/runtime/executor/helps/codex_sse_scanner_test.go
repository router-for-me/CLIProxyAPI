package helps

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// wellFormedCodexSSEStream is a Responses stream with one event per data line, comments, event
// lines, events without a blank separator, and the [DONE] sentinel.
func wellFormedCodexSSEStream() string {
	var stream strings.Builder
	stream.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\"}}\n\n")
	for i := range 6 {
		fmt.Fprintf(&stream, ": comment-%04d\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":%q}\n\n", i, strings.Repeat("x", 64))
	}
	stream.WriteString("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n")
	return stream.String()
}

func cloneLogHooks(src log.LevelHooks) log.LevelHooks {
	cloned := make(log.LevelHooks, len(src))
	for lvl, hooks := range src {
		cloned[lvl] = append([]log.Hook(nil), hooks...)
	}
	return cloned
}

func captureCodexSSEWarnings(t *testing.T) *logtest.Hook {
	t.Helper()
	origHooks := cloneLogHooks(log.StandardLogger().Hooks)
	hook := new(logtest.Hook)
	log.StandardLogger().AddHook(hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(origHooks) })
	return hook
}

func scanCodexSSE(t *testing.T, stream string) []string {
	t.Helper()
	scanner := NewCodexSSEScanner(context.Background(), strings.NewReader(stream), 1<<20)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, string(scanner.Bytes()))
	}
	if errScan := scanner.Err(); errScan != nil {
		t.Fatalf("scan error: %v", errScan)
	}
	return lines
}

func TestCodexSSEScannerAssemblesEventData(t *testing.T) {
	const marker = "body-bytes-must-not-be-logged"
	cases := []struct {
		name     string
		stream   string
		want     []string
		warnings int
	}{
		{
			name:   "one-line events pass through as read",
			stream: "event: response.created\ndata: {\"type\":\"response.created\"}\n\n: keep-alive\n\ndata: {\"type\":\"x\",\"v\":\"a b\"}\n\n",
			want:   []string{"event: response.created", `data: {"type":"response.created"}`, "", ": keep-alive", "", `data: {"type":"x","v":"a b"}`, ""},
		},
		{
			name:   "CRLF line endings",
			stream: "event: a\r\ndata: {\"type\":\"a\"}\r\n\r\n",
			want:   []string{"event: a", `data: {"type":"a"}`, ""},
		},
		{
			name:   "data lines of one event are joined and compacted",
			stream: "event: response.completed\ndata: {\"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"r\",\"output\":[]}}\n\n",
			want:   []string{"event: response.completed", `data: {"type":"response.completed","response":{"id":"r","output":[]}}`, ""},
		},
		{
			name:   "joined CRLF event",
			stream: "data: {\"a\":\r\ndata: [1,\r\ndata: 2]}\r\n\r\n",
			want:   []string{`data: {"a":[1,2]}`, ""},
		},
		{
			name:   "comment and field lines inside an event come before its data",
			stream: "event: e\ndata: {\"a\":\n: keep-alive\nid: 7\nretry: 3000\ndata: 1}\n\n",
			want:   []string{"event: e", ": keep-alive", "id: 7", "retry: 3000", `data: {"a":1}`, ""},
		},
		{
			name:   "unterminated one-line event at end of stream",
			stream: "data: {\"type\":\"response.completed\"}",
			want:   []string{`data: {"type":"response.completed"}`},
		},
		{
			name:   "unterminated multi-line event at end of stream",
			stream: "data: {\"type\":\ndata: \"response.completed\"}\n",
			want:   []string{`data: {"type":"response.completed"}`},
		},
		{
			name:   "complete events without blank separators stay separate",
			stream: "data: {\"a\":1}\ndata: {\"b\":2}\n",
			want:   []string{`data: {"a":1}`, `data: {"b":2}`},
		},
		{
			name:   "split event followed by an event without a blank separator",
			stream: "data: {\"a\":\ndata: 1}\ndata: {\"b\":2}\n",
			want:   []string{`data: {"a":1}`, `data: {"b":2}`},
		},
		{
			name:     "an invalid fragment absorbs the following data lines up to the blank line",
			stream:   "data: {\"a\":\"" + marker + "\ndata: {\"b\":2}\n\n",
			want:     []string{"data: {\"a\":\"" + marker + "\ndata: {\"b\":2}", ""},
			warnings: 1,
		},
		{
			name:     "only the first invalid event in a stream is described",
			stream:   "data: {\"a\":\n\ndata: {\"b\":\n\ndata: {\"c\":3}\n\n",
			want:     []string{"data: {\"a\":", "", "data: {\"b\":", "", `data: {"c":3}`, ""},
			warnings: 1,
		},
		{
			name:   "a complete object on its own line inside an open array stays in the event",
			stream: "data: {\"type\":\"response.completed\",\"response\":{\"output\":[\ndata: {\"id\":\"m\",\"type\":\"message\"}\ndata: ]}}\n\n",
			want:   []string{`data: {"type":"response.completed","response":{"output":[{"id":"m","type":"message"}]}}`, ""},
		},
		{
			name:     "unterminated incomplete event at end of stream is still delivered",
			stream:   "data: {\"type\":\"response.completed\",\"t\":\"" + marker + "\",",
			want:     []string{`data: {"type":"response.completed","t":"` + marker + `",`},
			warnings: 1,
		},
		{
			name:   "empty and non-JSON data lines take the stock path",
			stream: "data:\n\ndata: \n\ndata: ping\n\n",
			want:   []string{"data:", "", "data: ", "", "data: ping", ""},
		},
		{
			name:   "done sentinel",
			stream: "data: [DONE]\n\n",
			want:   []string{"data: [DONE]", ""},
		},
		{
			name:     "invalid one-line event passes through with one warning",
			stream:   "data: {\"type\":\"response.completed\",\"t\":\"" + marker + "\",\n\n",
			want:     []string{`data: {"type":"response.completed","t":"` + marker + `",`, ""},
			warnings: 1,
		},
		{
			name:     "event split inside a string stays invalid and keeps its data lines",
			stream:   "data: {\"t\":\"" + marker + "\ndata: " + marker + "\"}\n\n",
			want:     []string{`data: {"t":"` + marker + "\ndata: " + marker + `"}`, ""},
			warnings: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hook := captureCodexSSEWarnings(t)
			got := scanCodexSSE(t, tc.stream)
			if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", tc.want) {
				t.Fatalf("lines = %q\nwant    %q", got, tc.want)
			}
			if len(hook.AllEntries()) != tc.warnings {
				t.Fatalf("warnings = %d, want %d", len(hook.AllEntries()), tc.warnings)
			}
			for _, entry := range hook.AllEntries() {
				rendered, errRender := entry.String()
				if errRender != nil {
					t.Fatal(errRender)
				}
				_, hasLines := entry.Data["data_lines"]
				_, hasOffset := entry.Data["json_error_offset"]
				if entry.Level != log.WarnLevel || strings.Contains(rendered, marker) || !hasLines || !hasOffset {
					t.Fatalf("diagnostic = %q, want a bounded warning without data bytes", rendered)
				}
			}
		})
	}
}

func TestCodexSSEScannerBoundsAnEventToTheLineLimit(t *testing.T) {
	hook := captureCodexSSEWarnings(t)
	// Each fragment fits the 64-byte limit; together they do not.
	stream := "data: {\"a\":\"" + strings.Repeat("x", 40) + "\ndata: " + strings.Repeat("y", 40) + "\ndata: " + strings.Repeat("z", 40) + "\"}\n\ndata: {\"b\":2}\n\n"
	scanner := NewCodexSSEScanner(context.Background(), strings.NewReader(stream), 64)
	var got []string
	for scanner.Scan() {
		got = append(got, string(scanner.Bytes()))
	}
	if errScan := scanner.Err(); errScan != nil {
		t.Fatalf("scan error: %v", errScan)
	}
	// The collected fragment is flushed as an invalid event; the lines after it are read as stock reads them.
	want := []string{
		"data: {\"a\":\"" + strings.Repeat("x", 40),
		"data: " + strings.Repeat("y", 40),
		"data: " + strings.Repeat("z", 40) + "\"}",
		"",
		`data: {"b":2}`,
		"",
	}
	if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("lines = %q\nwant    %q", got, want)
	}
	if len(hook.AllEntries()) != 1 {
		t.Fatalf("warnings = %d, want 1 for the flushed invalid event", len(hook.AllEntries()))
	}
}

func TestCodexSSEScannerAssemblesAnEventOfThousandsOfLinesQuickly(t *testing.T) {
	// A 2 MB response.completed whose upstream wrapped it into 2,000 data lines between array
	// elements. Every line ends with "}", so only nesting, not the last byte, tells the event apart
	// from a finished one.
	item := func(i int) string {
		return fmt.Sprintf(`{"type":"message","content":[{"type":"output_text","text":"%s"}]}`, strings.Repeat(string(rune('a'+i%26)), 960))
	}
	head := `{"type":"response.completed","response":{"id":"resp_big","output":[`
	var payload, stream strings.Builder
	payload.WriteString(head + item(0))
	stream.WriteString("data: " + head + item(0) + "\n")
	for i := 1; i < 2000; i++ {
		payload.WriteString("," + item(i))
		stream.WriteString("data: ," + item(i) + "\n")
	}
	payload.WriteString("]}}")
	stream.WriteString("data: ]}}\n\n")
	started := time.Now()
	scanner := NewCodexSSEScanner(context.Background(), strings.NewReader(stream.String()), 52_428_800)
	var got []string
	for scanner.Scan() {
		got = append(got, string(scanner.Bytes()))
	}
	elapsed := time.Since(started)
	if len(got) != 2 || got[0] != "data: "+payload.String() {
		t.Fatalf("assembled %d lines, want the event on one line and its blank line", len(got))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("assembling 2,001 data lines took %v; validation must not grow with every line", elapsed)
	}
}

func TestCodexSSEScannerHoldsAnEventInMemoryThatFollowsItsBytes(t *testing.T) {
	// One valid event of 100,000 empty data lines: about 600 KB, well within the line limit.
	stream := "data: [\n" + strings.Repeat("data:\n", 100_000) + "data: ]\n\n"
	var got []string
	allocs := testing.AllocsPerRun(1, func() {
		got = got[:0]
		scanner := NewCodexSSEScanner(context.Background(), strings.NewReader(stream), 1<<20)
		for scanner.Scan() {
			got = append(got, string(scanner.Bytes()))
		}
	})
	if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", []string{"data: []", ""}) {
		t.Fatalf("lines = %q", got)
	}
	// Holding each line separately costs at least one allocation per line.
	if allocs > 1000 {
		t.Fatalf("assembling 100,002 data lines took %.0f allocations; holding an event must not allocate per line", allocs)
	}
}

func TestCodexSSEScannerReadsLineByLineWhileAssemblyIsOff(t *testing.T) {
	stream := "data: {\"a\":\ndata: 1}\n\ndata: {\"b\":\ndata: 2}\n\n"
	scanner := NewCodexSSEScanner(context.Background(), strings.NewReader(stream), 1<<20)
	scanner.SetAssembly(false)
	var got []string
	for scanner.Scan() {
		got = append(got, string(scanner.Bytes()))
		if len(got) == 3 {
			// The first event has been read as bufio.Scanner reads it; assemble from here on.
			scanner.SetAssembly(true)
		}
	}
	want := []string{"data: {\"a\":", "data: 1}", "", `data: {"b":2}`, ""}
	if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("lines = %q\nwant    %q", got, want)
	}
}

// codexSSEChunkReader returns one chunk per Read and counts the reads.
type codexSSEChunkReader struct {
	chunks []string
	reads  int
}

func (r *codexSSEChunkReader) Read(p []byte) (int, error) {
	if r.reads == len(r.chunks) {
		return 0, io.EOF
	}
	r.reads++
	return copy(p, r.chunks[r.reads-1]), nil
}

func TestCodexSSEScannerDeliversNonDataLinesAsTheyAreRead(t *testing.T) {
	reader := &codexSSEChunkReader{chunks: []string{"data: {\"a\":\n: keep-alive\nid: 7\n", "data: 1}\n\n"}}
	scanner := NewCodexSSEScanner(context.Background(), reader, 1<<20)
	var got []string
	for scanner.Scan() {
		got = append(got, fmt.Sprintf("%q@%d", scanner.Bytes(), reader.reads))
	}
	// Only the data lines wait for the rest of their event; the other lines are not held for it.
	want := []string{`": keep-alive"@1`, `"id: 7"@1`, `"data: {\"a\":1}"@2`, `""@2`}
	if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("lines = %q\nwant    %q", got, want)
	}
}

func TestCodexSSEScannerDiagnosticDescribesFraming(t *testing.T) {
	hook := captureCodexSSEWarnings(t)
	// A split inside a string: each fragment is invalid on its own, and so is their SSE join.
	scanCodexSSE(t, "data: {\"type\":\"response.completed\",\"t\":\"abc\ndata: def\"}\n\n")
	entries := hook.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("warnings = %d, want 1", len(entries))
	}
	want := log.Fields{"data_lines": 2, "line_bytes": "37,5", "lines_valid": "false,false", "data_bytes": 43, "json_error_offset": int64(38)}
	for key, value := range want {
		if entries[0].Data[key] != value {
			t.Fatalf("diagnostic field %s = %v, want %v (fields %v)", key, entries[0].Data[key], value, entries[0].Data)
		}
	}
}

func TestCodexSSEScannerMatchesBufioScannerOnWellFormedStreams(t *testing.T) {
	stream := wellFormedCodexSSEStream() + "data: {\"type\":\"x\"}\ndata: {\"type\":\"y\"}\n: trailing comment\n\nevent: done\ndata: [DONE]\n\n"
	for _, newline := range []string{"\n", "\r\n"} {
		input := strings.ReplaceAll(stream, "\n", newline)
		t.Run(fmt.Sprintf("newline=%q", newline), func(t *testing.T) {
			stock := bufio.NewScanner(strings.NewReader(input))
			var want []string
			for stock.Scan() {
				want = append(want, stock.Text())
			}
			got := scanCodexSSE(t, input)
			if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
				t.Fatalf("lines = %q\nwant bufio.Scanner's %q", got, want)
			}
		})
	}
}
