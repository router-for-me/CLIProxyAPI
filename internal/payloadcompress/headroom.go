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
	headroomDefaultCols = 32
)

func (e headroomEngine) compress(text string) (string, bool) {
	minRows := e.opts.MinRows
	if minRows <= 0 {
		minRows = headroomDefaultRows
	}
	maxCols := e.opts.MaxColumns
	if maxCols <= 0 {
		maxCols = headroomDefaultCols
	}
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < headroomMinBytes || trimmed[0] != '[' {
		return text, false
	}
	arr := gjson.Parse(trimmed)
	if !arr.IsArray() || arr.Get("#").Int() < int64(minRows) {
		return text, false
	}
	row0 := arr.Get("0")
	if !row0.IsObject() {
		return text, false
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
	if !ok || len(keys) == 0 || len(keys) > maxCols {
		return text, false
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
		return text, false
	}

	var b strings.Builder
	b.Grow(len(trimmed))
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
	if len(out) >= len(trimmed)-len(trimmed)/10 {
		return text, false
	}
	return out, true
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
