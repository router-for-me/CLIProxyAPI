package payloadcompress

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/MoeForge/moego/ojson"
)

// Defaults from upstream's IONIZER_SCHEMA.
const (
	ionizerMaxRows    = 100000
	ionizerThreshold  = 200
	ionizerTargetRows = 50
	ionizerFirstK     = 3
	ionizerLastK      = 3
)

var ionizerErrorKey = regexp.MustCompile(`(?i)error|fail|exception|stderr|denied`)

// ionizerEngine samples long homogeneous JSON arrays embedded in chat message
// content and replaces them with a representative subset plus a CCR marker.
type ionizerEngine struct{}

func newIonizerEngine() ionizerEngine { return ionizerEngine{} }

func (ionizerEngine) name() string { return "ionizer" }

// applyBody rewrites eligible chat message content. It fails open on any
// unexpected shape and never panics.
func (ionizerEngine) applyBody(body []byte) (out []byte) {
	out = body
	defer func() {
		if recover() != nil {
			out = body
		}
	}()

	doc, items, ok := bodyMessageItems(body)
	if !ok {
		return body
	}
	raws := make([]string, len(items))
	changed := false
	for i, item := range items {
		raws[i] = item.Raw
		if item.Get("role").String() == "system" {
			continue
		}
		content := item.Get("content")
		if content.Type != ojson.String {
			continue
		}
		text := content.String()
		next, hit := ionizeContent(text)
		if !hit || charLen(next) >= charLen(text) {
			continue
		}
		raws[i] = msgReplaceTextItem(item, next)
		changed = true
	}
	if !changed {
		return body
	}
	nextBody, ok := setBodyMessages(doc, raws)
	if !ok {
		return body
	}
	return nextBody
}

// ionizeContent replaces a JSON array of plain objects with its sampled rows
// and a CCR retrieve marker. It reports whether the content was ionized.
func ionizeContent(serialized string) (string, bool) {
	if !ojson.Valid(serialized) {
		return "", false
	}
	arr := ojson.Parse(serialized)
	if !arr.IsArray() {
		return "", false
	}
	rows := arr.Array()
	if len(rows) <= ionizerThreshold || len(rows) > ionizerMaxRows {
		return "", false
	}
	for _, row := range rows {
		if !row.IsObject() {
			return "", false
		}
	}

	keptIdx := ionizeIndices(rows, ionizerFNV1a(serialized))
	if len(keptIdx) >= len(rows) {
		return "", false
	}
	id, stored := ccrTryStore(serialized)
	if !stored {
		return "", false
	}

	var b strings.Builder
	b.WriteByte('[')
	for i, idx := range keptIdx {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(rows[idx].Raw)
	}
	b.WriteByte(']')
	marker := "[ionizer: kept " + strconv.Itoa(len(keptIdx)) + "/" +
		strconv.Itoa(len(rows)) + " rows; full → CCR retrieve hash=" + id +
		" chars=" + strconv.Itoa(charLen(serialized)) + "]"
	return b.String() + "\n" + marker, true
}

// ionizeIndices picks schema-covering rows, all error rows, first/last K rows
// and a seeded sample of the middle, returning them in original order.
func ionizeIndices(rows []ojson.Result, seed uint32) []int {
	n := len(rows)
	if n <= ionizerTargetRows {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}

	keep := make([]bool, n)
	seen := make(map[string]struct{})
	for i, row := range rows {
		novel := false
		for key := range row.Map() {
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				novel = true
			}
		}
		if novel {
			keep[i] = true
		}
	}
	for i, row := range rows {
		if ionizerIsErrorRow(row) {
			keep[i] = true
		}
	}
	for i := 0; i < ionizerFirstK && i < n; i++ {
		keep[i] = true
	}
	for i := n - ionizerLastK; i < n; i++ {
		if i >= 0 {
			keep[i] = true
		}
	}

	count := 0
	for _, v := range keep {
		if v {
			count++
		}
	}
	if count < ionizerTargetRows {
		middle := make([]int, 0, n-count)
		for i := 0; i < n; i++ {
			if !keep[i] {
				middle = append(middle, i)
			}
		}
		for _, idx := range ionizerSeededSample(middle, ionizerTargetRows-count, seed) {
			keep[idx] = true
		}
	}

	kept := make([]int, 0, n)
	for i, v := range keep {
		if v {
			kept = append(kept, i)
		}
	}
	return kept
}

// ionizerIsErrorRow reports whether a row looks like an error or failure.
func ionizerIsErrorRow(row ojson.Result) bool {
	for key, value := range row.Map() {
		if ionizerErrorKey.MatchString(key) && ionizerTruthy(value) {
			return true
		}
	}
	for _, key := range []string{"status", "statusCode", "code"} {
		value := row.Get(key)
		if value.Type == ojson.Number {
			n := value.Float()
			if n >= 400 && n <= 599 {
				return true
			}
		}
	}
	return false
}

func ionizerTruthy(value ojson.Result) bool {
	switch value.Type {
	case ojson.Null, ojson.False:
		return false
	case ojson.Number:
		return value.Float() != 0
	case ojson.String:
		return value.String() != ""
	default:
		return true
	}
}

// ionizerSeededSample returns k deterministic elements of pool, preserving
// pool order. k >= len(pool) returns all of pool.
func ionizerSeededSample(pool []int, k int, seed uint32) []int {
	if k >= len(pool) {
		return append([]int(nil), pool...)
	}
	idx := make([]int, len(pool))
	for i := range idx {
		idx[i] = i
	}
	rand := ionizerMulberry32(seed)
	for i := 0; i < k; i++ {
		j := i + int(rand()*float64(len(idx)-i))
		idx[i], idx[j] = idx[j], idx[i]
	}
	chosen := append([]int(nil), idx[:k]...)
	sort.Ints(chosen)
	out := make([]int, len(chosen))
	for i, v := range chosen {
		out[i] = pool[v]
	}
	return out
}

// ionizerMulberry32 is a deterministic seeded PRNG matching upstream.
func ionizerMulberry32(seed uint32) func() float64 {
	a := seed
	return func() float64 {
		a += 0x6d2b79f5
		t := a
		t = (t ^ (t >> 15)) * (1 | a)
		t = (t + (t^(t>>7))*(61|t)) ^ t
		return float64(t^(t>>14)) / 4294967296.0
	}
}

// ionizerFNV1a hashes the UTF-16 code units of s, matching JS string hashing.
func ionizerFNV1a(s string) uint32 {
	var h uint32 = 0x811c9dc5
	for _, unit := range utf16.Encode([]rune(s)) {
		h ^= uint32(unit)
		h *= 0x01000193
	}
	return h
}
