package payloadcompress

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// headroomEngine rewrites a homogeneous JSON array of flat records into one
// header line plus one line per row, so field names are written once instead of
// once per row. Tool output that is a list of records is the common case.
type headroomEngine struct {
	opts StepOptions
}

func (headroomEngine) name() string { return EngineHeadroom }

const (
	headroomMarker      = "# columns="
	headroomMinBytes    = 512
	headroomDefaultRows = 8
	// headroomMaxColumns is a sanity guard, not a knob: a table this wide does not
	// shrink enough to be worth rewriting.
	headroomMaxColumns = 32
)

func (e headroomEngine) compress(text string) (string, bool) {
	minRows := e.opts.MinRows
	if minRows <= 0 {
		minRows = headroomDefaultRows
	}
	trimmed := strings.TrimSpace(text)
	if len(trimmed) >= headroomMinBytes && trimmed[0] == '[' {
		if out, ok := compactJSONArray(trimmed, minRows); ok {
			return out, true
		}
	}
	// Only ```json fenced blocks are rewritten here. Bare arrays inside arbitrary
	// prose or JSON objects are left alone: upstream crushText never compacts
	// them, and splicing a columnar table into an object would break it.
	return compactEmbeddedArrays(text, minRows)
}

// compactJSONArray rewrites text, which must be exactly one JSON array, as a
// header line plus one line per row. It reports false when the input is not a
// homogeneous array of flat records or the rewrite does not save at least 10%.
func compactJSONArray(text string, minRows int) (string, bool) {
	arr := gjson.Parse(text)
	if !arr.IsArray() || arr.Get("#").Int() < int64(minRows) {
		return "", false
	}
	row0 := arr.Get("0")
	if !row0.IsObject() {
		return "", false
	}

	keys := make([]string, 0, 8)
	ok := true
	row0.ForEach(func(k, v gjson.Result) bool {
		if !isScalar(v) {
			ok = false
			return false
		}
		keys = append(keys, k.String())
		return true
	})
	if !ok || len(keys) == 0 || len(keys) > headroomMaxColumns {
		return "", false
	}
	homogeneous := true
	arr.ForEach(func(_, item gjson.Result) bool {
		if !item.IsObject() {
			homogeneous = false
			return false
		}
		i := 0
		item.ForEach(func(k, v gjson.Result) bool {
			if i >= len(keys) || k.String() != keys[i] || !isScalar(v) {
				homogeneous = false
				return false
			}
			i++
			return true
		})
		if i != len(keys) {
			homogeneous = false
		}
		return homogeneous
	})
	if !homogeneous {
		return "", false
	}

	var b strings.Builder
	b.Grow(len(text))
	b.WriteString(headroomMarker)
	b.WriteString(strings.Join(keys, ","))
	b.WriteByte('\n')
	arr.ForEach(func(_, item gjson.Result) bool {
		first := true
		item.ForEach(func(_, v gjson.Result) bool {
			if !first {
				b.WriteByte(',')
			}
			first = false
			b.WriteString(cell(v))
			return true
		})
		b.WriteByte('\n')
		return true
	})

	out := b.String()
	if len(out) >= len(text)-len(text)/10 {
		return "", false
	}
	return out, true
}

// compactEmbeddedArrays compacts arrays inside ```json fenced blocks. Everything
// outside those blocks is copied verbatim. Other fenced blocks are skipped,
// matching upstream's ```json-only scan and avoiding rewrites inside code that
// is not JSON.
func compactEmbeddedArrays(text string, minRows int) (string, bool) {
	var b strings.Builder
	b.Grow(len(text))
	changed := false
	for i := 0; i < len(text); {
		if (i == 0 || text[i-1] == '\n') && strings.HasPrefix(text[i:], "```") {
			lineEnd := strings.IndexByte(text[i:], '\n')
			if lineEnd < 0 {
				b.WriteString(text[i:])
				break
			}
			info := text[i+3 : i+lineEnd]
			contentStart := i + lineEnd + 1
			rel := strings.Index(text[contentStart:], "\n```")
			if rel < 0 {
				b.WriteString(text[i:])
				break
			}
			blockEnd := contentStart + rel + 4
			if info == "json" {
				content := strings.TrimSpace(text[contentStart : contentStart+rel])
				if out, ok := compactJSONArray(content, minRows); ok {
					b.WriteString(out)
					changed = true
					i = blockEnd
					continue
				}
			}
			b.WriteString(text[i:blockEnd])
			i = blockEnd
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	if !changed {
		return text, false
	}
	return b.String(), true
}

func isScalar(v gjson.Result) bool {
	switch v.Type {
	case gjson.String, gjson.Number, gjson.True, gjson.False, gjson.Null:
		return true
	}
	return false
}

// cell renders one scalar as a column value. Values that would break the line
// layout are quoted, so the output can be read back unambiguously.
func cell(v gjson.Result) string {
	if v.Type == gjson.String {
		s := v.String()
		if strings.ContainsAny(s, ",\"\n\r") {
			return strconv.Quote(s)
		}
		return s
	}
	return v.Raw
}
