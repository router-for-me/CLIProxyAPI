package payloadcompress

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func ionizerRows(n int, extra map[int]string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if row, ok := extra[i]; ok {
			b.WriteString(row)
			continue
		}
		b.WriteString(`{"id":`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`,"value":"row"}`)
	}
	b.WriteByte(']')
	return b.String()
}

func ionizerTestBody(role, content string) []byte {
	b := []byte(`{"model":"m","messages":[{"role":`)
	b = appendJSONString(b, role)
	b = append(b, `,"content":`...)
	b = appendJSONString(b, content)
	return append(b, `}]}`...)
}

func TestIonizerEngineIdentity(t *testing.T) {
	if got := newIonizerEngine().name(); got != "ionizer" {
		t.Fatalf("unexpected engine name %q", got)
	}
}

func TestIonizerBelowThresholdUnchanged(t *testing.T) {
	ccrResetStore()
	body := ionizerTestBody("user", ionizerRows(ionizerThreshold, nil))
	if got := newIonizerEngine().applyBody(body); string(got) != string(body) {
		t.Fatalf("expected body unchanged at threshold")
	}
}

func TestIonizerSamplesLargeArray(t *testing.T) {
	ccrResetStore()
	content := ionizerRows(250, nil)
	body := ionizerTestBody("user", content)
	got := newIonizerEngine().applyBody(body)
	if string(got) == string(body) {
		t.Fatal("expected large array to be ionized")
	}

	doc := ojson.NewDocString(string(got))
	text := doc.Get("messages.0.content").String()
	parts := strings.SplitN(text, "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("expected sampled array plus marker, got %q", text)
	}

	var marker string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "[ionizer:") {
			marker = line
			break
		}
	}
	want := regexp.MustCompile(`^\[ionizer: kept (\d+)/250 rows; full → CCR retrieve hash=[0-9a-f]{24} chars=` +
		strconv.Itoa(charLen(content)) + `\]$`)
	m := want.FindStringSubmatch(marker)
	if m == nil {
		t.Fatalf("expected ionizer marker, got %q", marker)
	}
	if m[1] != strconv.Itoa(ionizerTargetRows) {
		t.Fatalf("expected %d kept rows, got %s", ionizerTargetRows, m[1])
	}

	kept := ojson.Parse(parts[0])
	if !kept.IsArray() || len(kept.Array()) != ionizerTargetRows {
		t.Fatalf("expected %d sampled rows, got %s", ionizerTargetRows, parts[0])
	}
	if charLen(text) >= charLen(content) {
		t.Fatal("ionized content did not shrink")
	}
}

func TestIonizerKeepsErrorRows(t *testing.T) {
	ccrResetStore()
	extra := map[int]string{
		120: `{"id":120,"error":"boom"}`,
		180: `{"id":180,"status":500}`,
	}
	body := ionizerTestBody("user", ionizerRows(300, extra))
	got := newIonizerEngine().applyBody(body)
	text := ojson.NewDocString(string(got)).Get("messages.0.content").String()
	sampled := strings.SplitN(text, "\n", 2)[0]
	for _, want := range []string{`{"id":120,"error":"boom"}`, `{"id":180,"status":500}`} {
		if !strings.Contains(sampled, want) {
			t.Fatalf("expected error row %s kept, got %s", want, sampled)
		}
	}
}

func TestIonizerPreservesOrderAndDeterminism(t *testing.T) {
	ccrResetStore()
	content := ionizerRows(400, nil)
	body := ionizerTestBody("user", content)
	first := newIonizerEngine().applyBody(body)
	second := newIonizerEngine().applyBody(body)
	if string(first) != string(second) {
		t.Fatal("expected deterministic output")
	}

	text := ojson.NewDocString(string(first)).Get("messages.0.content").String()
	sampled := ojson.Parse(strings.SplitN(text, "\n", 2)[0])
	last := -1
	for _, row := range sampled.Array() {
		id := int(row.Get("id").Int())
		if id <= last {
			t.Fatalf("rows not in original order: %d after %d", id, last)
		}
		last = id
	}
}

func TestIonizerSkipsSystemAndNonStringContent(t *testing.T) {
	ccrResetStore()
	content := ionizerRows(300, nil)
	if got := newIonizerEngine().applyBody(ionizerTestBody("system", content)); string(got) != string(ionizerTestBody("system", content)) {
		t.Fatal("expected system message unchanged")
	}

	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":` + string(ionizerJSONString(content)) + `}]}]}`)
	if got := newIonizerEngine().applyBody(body); string(got) != string(body) {
		t.Fatal("expected non-string content unchanged")
	}
}

func TestIonizerSkipsNonHomogeneousAndInvalid(t *testing.T) {
	ccrResetStore()
	heterogeneous := `[{"id":1},2,{"id":3}]`
	if got := newIonizerEngine().applyBody(ionizerTestBody("user", heterogeneous)); string(got) != string(ionizerTestBody("user", heterogeneous)) {
		t.Fatal("expected array with non-objects unchanged")
	}

	invalid := strings.Repeat("not json ", 300)
	if got := newIonizerEngine().applyBody(ionizerTestBody("user", invalid)); string(got) != string(ionizerTestBody("user", invalid)) {
		t.Fatal("expected non-JSON content unchanged")
	}
}

func TestIonizerLeavesOtherMessagesByteIdentical(t *testing.T) {
	ccrResetStore()
	large := ionizerRows(300, nil)
	small := `keep me exactly`
	b := []byte(`{"model":"m","messages":[{"role":"user","content":`)
	b = appendJSONString(b, small)
	b = append(b, `},{"role":"user","content":`...)
	b = appendJSONString(b, large)
	b = append(b, `}]}`...)

	got := newIonizerEngine().applyBody(b)
	if !strings.Contains(string(got), `"content":"keep me exactly"`) {
		t.Fatalf("expected untouched message preserved, got %s", got)
	}
}

func ionizerJSONString(s string) []byte {
	return appendJSONString(nil, s)
}
