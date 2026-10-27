package common

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/tidwall/gjson"
)

const (
	maxVertexPartialArgsArrayIndex = 1024
	maxVertexPartialArgsDepth      = 64
	maxVertexPartialArgsOutputSize = 4 * 1024 * 1024
)

type pathSegmentKind uint8

const (
	segmentObjectKey pathSegmentKind = iota
	segmentArrayIndex
)

type pathSegment struct {
	kind  pathSegmentKind
	key   string
	index int
}

type vertexNodeKind uint8

const (
	vertexNodeUnset vertexNodeKind = iota
	vertexNodeObject
	vertexNodeArray
	vertexNodeString
	vertexNodeScalar
)

type vertexASTNode struct {
	kind vertexNodeKind

	// Object state
	keys       []string
	children   map[string]*vertexASTNode
	objNextIdx int

	// Array state
	elements   map[int]*vertexASTNode
	maxIdx     int
	arrNextIdx int

	// String state
	strBuf     strings.Builder
	strEmitted int
	strOpened  bool
	strClosed  bool
	stringOpen bool

	// Scalar state
	scalarRaw     string
	scalarEmitted bool

	holdUntilFinish bool

	// Container emission state
	containerOpened bool
	containerClosed bool
	commaEmitted    map[int]bool
}

// VertexPartialArgsAssembler incrementally reconstructs a JSON value from Vertex AI
// streaming functionCall.partialArgs fragments and emits valid JSON deltas that can
// be concatenated directly by downstream OpenAI and Claude clients.
type VertexPartialArgsAssembler struct {
	root             *vertexASTNode
	lastStringNode   *vertexASTNode
	cleaner          func(string) string
	closedEmptyCache map[string]bool
	fullBuf          strings.Builder
	bufferedBytes    int
	err              error
}

// NewVertexPartialArgsAssembler creates a new assembler for a single streaming function call.
func NewVertexPartialArgsAssembler() *VertexPartialArgsAssembler {
	return &VertexPartialArgsAssembler{}
}

// SetArgumentCleaner configures an optional JSON argument cleaner (such as
// Claude schema placeholder stripping) applied before potentially removable
// placeholder fields ("reason", "_", or closed empty object fields) are emitted.
func (a *VertexPartialArgsAssembler) SetArgumentCleaner(cleaner func(string) string) {
	a.cleaner = cleaner
	if cleaner != nil && a.closedEmptyCache == nil {
		a.closedEmptyCache = make(map[string]bool)
	}
}

// Err returns the first structural or limits error encountered while assembling partialArgs.
func (a *VertexPartialArgsAssembler) Err() error {
	return a.err
}

func (a *VertexPartialArgsAssembler) setError(err error) {
	if a.err == nil && err != nil {
		a.err = err
	}
}

// IsVertexStreamingFunctionCall reports whether a Gemini/Vertex functionCall object
// uses streaming arguments (partialArgs or willContinue).
func IsVertexStreamingFunctionCall(fc gjson.Result) bool {
	if !fc.Exists() || !fc.IsObject() {
		return false
	}
	partialArgs := fc.Get("partialArgs")
	if !partialArgs.Exists() {
		partialArgs = fc.Get("partial_args")
	}
	if partialArgs.Exists() {
		if partialArgs.IsArray() && len(partialArgs.Array()) == 0 && fc.Get("args").Exists() && !VertexFunctionCallWillContinue(fc) {
			return false
		}
		return true
	}
	if fc.Get("willContinue").Exists() || fc.Get("will_continue").Exists() {
		if fc.Get("args").Exists() && !VertexFunctionCallWillContinue(fc) {
			return false
		}
		return true
	}
	return false
}

// VertexFunctionCallWillContinue reports whether a functionCall chunk indicates that
// subsequent chunks will follow for the same function call.
func VertexFunctionCallWillContinue(fc gjson.Result) bool {
	return fc.Get("willContinue").Bool() || fc.Get("will_continue").Bool()
}

// PushFunctionCall processes one functionCall chunk and returns the newly emitted JSON
// argument delta and whether the function call has completed cleanly.
func (a *VertexPartialArgsAssembler) PushFunctionCall(fc gjson.Result) (string, bool) {
	if a.err != nil {
		return "", false
	}
	startLen := a.fullBuf.Len()
	outerWillContinue := VertexFunctionCallWillContinue(fc)

	partialArgs := fc.Get("partialArgs")
	if !partialArgs.Exists() {
		partialArgs = fc.Get("partial_args")
	}

	if partialArgs.Exists() {
		if !partialArgs.IsArray() {
			a.setError(errors.New("vertex partialArgs must be an array"))
			return "", false
		}
		partialArgs.ForEach(func(_, item gjson.Result) bool {
			a.pushPartialArg(item)
			return a.err == nil
		})
		if a.err != nil {
			return "", false
		}
	}
	if !outerWillContinue && a.root == nil && a.fullBuf.Len() == 0 {
		if args := fc.Get("args"); args.Exists() && args.IsObject() {
			raw := strings.TrimSpace(args.Raw)
			if raw != "" {
				if a.cleaner != nil {
					raw = a.cleaner(raw)
				}
				if !a.writeString(raw) {
					return "", false
				}
				return a.fullBuf.String()[startLen:], true
			}
		}
	}

	if a.root != nil {
		if !outerWillContinue && a.cleaner != nil && a.hasHeldNodes(a.root) {
			var rawBuf strings.Builder
			a.serializeNode(a.root, &rawBuf)
			cleaned := a.cleaner(rawBuf.String())
			if gjson.Valid(cleaned) {
				a.pruneHeldNodes(a.root, gjson.Parse(cleaned))
			} else {
				a.clearHeldFlags(a.root)
			}
		}
		a.emitNode(a.root, !outerWillContinue)
		if a.err != nil {
			return "", false
		}
	} else if !outerWillContinue && a.fullBuf.Len() == 0 {
		if !a.writeString("{}") {
			return "", false
		}
	}

	return a.fullBuf.String()[startLen:], !outerWillContinue
}

// FullArguments returns the concatenated JSON arguments emitted so far.
func (a *VertexPartialArgsAssembler) FullArguments() string {
	if a.fullBuf.Len() == 0 {
		return "{}"
	}
	return a.fullBuf.String()
}

func (a *VertexPartialArgsAssembler) reserveBuffered(n int) bool {
	if n < 0 || a.bufferedBytes+n > maxVertexPartialArgsOutputSize {
		a.setError(errors.New("vertex partialArgs output size limit exceeded"))
		return false
	}
	a.bufferedBytes += n
	return true
}

func (a *VertexPartialArgsAssembler) writeByte(b byte) bool {
	if a.fullBuf.Len()+1 > maxVertexPartialArgsOutputSize {
		a.setError(errors.New("vertex partialArgs output size limit exceeded"))
		return false
	}
	a.fullBuf.WriteByte(b)
	return true
}

func (a *VertexPartialArgsAssembler) writeString(s string) bool {
	if a.fullBuf.Len()+len(s) > maxVertexPartialArgsOutputSize {
		a.setError(errors.New("vertex partialArgs output size limit exceeded"))
		return false
	}
	a.fullBuf.WriteString(s)
	return true
}

func (a *VertexPartialArgsAssembler) writeEscapedString(s string) bool {
	var tmp strings.Builder
	appendEscapedJSONStringContent(&tmp, s)
	return a.writeString(tmp.String())
}

func newContainerNode(kind vertexNodeKind) *vertexASTNode {
	node := &vertexASTNode{
		kind:         kind,
		maxIdx:       -1,
		commaEmitted: make(map[int]bool),
	}
	if kind == vertexNodeObject {
		node.children = make(map[string]*vertexASTNode)
	} else if kind == vertexNodeArray {
		node.elements = make(map[int]*vertexASTNode)
	}
	return node
}

func (a *VertexPartialArgsAssembler) findNode(segments []pathSegment) *vertexASTNode {
	curr := a.root
	for _, seg := range segments {
		if curr == nil {
			return nil
		}
		if seg.kind == segmentObjectKey {
			if curr.kind != vertexNodeObject {
				return nil
			}
			curr = curr.children[seg.key]
		} else {
			if curr.kind != vertexNodeArray {
				return nil
			}
			curr = curr.elements[seg.index]
		}
	}
	return curr
}

func (a *VertexPartialArgsAssembler) pushPartialArg(item gjson.Result) {
	if !item.IsObject() {
		a.setError(errors.New("vertex partialArgs element must be an object"))
		return
	}

	hasPathField := item.Get("jsonPath").Exists() || item.Get("json_path").Exists()
	rawPath := item.Get("jsonPath").String()
	if !item.Get("jsonPath").Exists() {
		rawPath = item.Get("json_path").String()
	}

	var segments []pathSegment
	if hasPathField {
		var okPath bool
		segments, okPath = parseVertexJSONPath(rawPath)
		if !okPath {
			a.setError(fmt.Errorf("invalid vertex partialArgs jsonPath: %q", rawPath))
			return
		}
	}

	innerWillContinue := item.Get("willContinue").Bool() || item.Get("will_continue").Bool()

	strRes := item.Get("stringValue")
	if !strRes.Exists() {
		strRes = item.Get("string_value")
	}
	numRes := item.Get("numberValue")
	if !numRes.Exists() {
		numRes = item.Get("number_value")
	}
	boolRes := item.Get("boolValue")
	if !boolRes.Exists() {
		boolRes = item.Get("bool_value")
	}
	nullRes := item.Get("nullValue")
	if !nullRes.Exists() {
		nullRes = item.Get("null_value")
	}

	hasString := strRes.Exists() && strRes.Type != gjson.Null
	hasNumber := numRes.Exists() && numRes.Type != gjson.Null
	hasBool := boolRes.Exists() && boolRes.Type != gjson.Null
	hasNull := nullRes.Exists() && !hasString && !hasNumber && !hasBool
	hasAnyValue := hasString || hasNumber || hasBool || hasNull

	// Valueless end marker (e.g. {"jsonPath":"$.city"} or {"jsonPath":"$.city","willContinue":false}).
	if !hasAnyValue && !innerWillContinue {
		if len(segments) == 0 {
			if a.lastStringNode != nil && a.lastStringNode.stringOpen {
				a.lastStringNode.stringOpen = false
			}
			return
		}
		if node := a.findNode(segments); node != nil && node.kind == vertexNodeString && node.stringOpen {
			node.stringOpen = false
		}
		return
	}

	if len(segments) == 0 {
		a.setError(errors.New("vertex partialArgs value requires non-empty jsonPath"))
		return
	}

	rootKind := vertexNodeObject
	if segments[0].kind == segmentArrayIndex {
		rootKind = vertexNodeArray
	}
	if a.root == nil {
		a.root = newContainerNode(rootKind)
	} else if a.root.kind != rootKind {
		a.setError(errors.New("conflicting root container type in vertex partialArgs"))
		return
	}

	curr := a.root
	for k := 0; k < len(segments)-1; k++ {
		if curr.containerClosed {
			a.setError(fmt.Errorf("cannot modify closed container in vertex partialArgs: %q", rawPath))
			return
		}
		seg := segments[k]
		wantChildKind := vertexNodeObject
		if segments[k+1].kind == segmentArrayIndex {
			wantChildKind = vertexNodeArray
		}
		var child *vertexASTNode
		if seg.kind == segmentObjectKey {
			if curr.kind != vertexNodeObject {
				a.setError(fmt.Errorf("expected object container in vertex partialArgs: %q", rawPath))
				return
			}
			child = curr.children[seg.key]
			if child == nil {
				if !a.reserveBuffered(len(seg.key) + 4) {
					return
				}
				child = newContainerNode(wantChildKind)
				child.holdUntilFinish = a.shouldHoldObjectKey(segments[:k], seg.key)
				a.insertObjectChild(curr, seg.key, child)
			}
		} else {
			if curr.kind != vertexNodeArray {
				a.setError(fmt.Errorf("expected array container in vertex partialArgs: %q", rawPath))
				return
			}
			if seg.index < curr.arrNextIdx && curr.elements[seg.index] == nil {
				a.setError(fmt.Errorf("array index %d already emitted in vertex partialArgs: %q", seg.index, rawPath))
				return
			}
			child = curr.elements[seg.index]
			if child == nil {
				if !a.reserveBuffered(4) {
					return
				}
				child = newContainerNode(wantChildKind)
				curr.elements[seg.index] = child
				if seg.index > curr.maxIdx {
					curr.maxIdx = seg.index
				}
			}
		}
		if child.kind != wantChildKind {
			a.setError(fmt.Errorf("conflicting node type in vertex partialArgs: %q", rawPath))
			return
		}
		curr = child
	}

	if curr.containerClosed {
		a.setError(fmt.Errorf("cannot modify closed container in vertex partialArgs: %q", rawPath))
		return
	}

	leafSeg := segments[len(segments)-1]
	var leaf *vertexASTNode
	if leafSeg.kind == segmentObjectKey {
		if curr.kind != vertexNodeObject {
			a.setError(fmt.Errorf("expected object container at leaf in vertex partialArgs: %q", rawPath))
			return
		}
		leaf = curr.children[leafSeg.key]
		if leaf == nil {
			if !a.reserveBuffered(len(leafSeg.key) + 4) {
				return
			}
			leaf = &vertexASTNode{
				holdUntilFinish: a.shouldHoldObjectKey(segments[:len(segments)-1], leafSeg.key),
			}
			a.insertObjectChild(curr, leafSeg.key, leaf)
		}
	} else {
		if curr.kind != vertexNodeArray {
			a.setError(fmt.Errorf("expected array container at leaf in vertex partialArgs: %q", rawPath))
			return
		}
		if leafSeg.index < curr.arrNextIdx && curr.elements[leafSeg.index] == nil {
			a.setError(fmt.Errorf("array index %d already emitted in vertex partialArgs: %q", leafSeg.index, rawPath))
			return
		}
		leaf = curr.elements[leafSeg.index]
		if leaf == nil {
			if !a.reserveBuffered(4) {
				return
			}
			leaf = &vertexASTNode{}
			curr.elements[leafSeg.index] = leaf
			if leafSeg.index > curr.maxIdx {
				curr.maxIdx = leafSeg.index
			}
		}
	}

	switch {
	case hasString || innerWillContinue:
		if leaf.kind == vertexNodeUnset {
			leaf.kind = vertexNodeString
			leaf.stringOpen = true
		} else if leaf.kind != vertexNodeString || leaf.strClosed || !leaf.stringOpen {
			a.setError(fmt.Errorf("cannot append to completed value at %q", rawPath))
			return
		}
		strVal := strRes.String()
		if !a.reserveBuffered(len(strVal)) {
			return
		}
		appendEscapedJSONStringContent(&leaf.strBuf, strVal)
		leaf.stringOpen = innerWillContinue
		a.lastStringNode = leaf
	case hasNumber:
		if leaf.kind != vertexNodeUnset {
			a.setError(fmt.Errorf("cannot overwrite existing value at %q", rawPath))
			return
		}
		numRaw := formatVertexJSONNumber(numRes)
		if !a.reserveBuffered(len(numRaw)) {
			return
		}
		leaf.kind = vertexNodeScalar
		leaf.scalarRaw = numRaw
	case hasBool:
		if leaf.kind != vertexNodeUnset {
			a.setError(fmt.Errorf("cannot overwrite existing value at %q", rawPath))
			return
		}
		leaf.kind = vertexNodeScalar
		if boolRes.Bool() {
			leaf.scalarRaw = "true"
		} else {
			leaf.scalarRaw = "false"
		}
	case hasNull:
		if leaf.kind != vertexNodeUnset {
			a.setError(fmt.Errorf("cannot overwrite existing value at %q", rawPath))
			return
		}
		leaf.kind = vertexNodeScalar
		leaf.scalarRaw = "null"
	}
}

func (a *VertexPartialArgsAssembler) insertObjectChild(curr *vertexASTNode, key string, child *vertexASTNode) {
	curr.children[key] = child
	if !child.holdUntilFinish {
		insertAt := len(curr.keys)
		for i := curr.objNextIdx; i < len(curr.keys); i++ {
			if existing := curr.children[curr.keys[i]]; existing != nil && existing.holdUntilFinish {
				insertAt = i
				break
			}
		}
		if insertAt < len(curr.keys) {
			curr.keys = append(curr.keys, "")
			copy(curr.keys[insertAt+1:], curr.keys[insertAt:])
			curr.keys[insertAt] = key
			return
		}
	}
	curr.keys = append(curr.keys, key)
}

func (a *VertexPartialArgsAssembler) shouldHoldObjectKey(parentPath []pathSegment, key string) bool {
	if a.cleaner == nil {
		return false
	}
	probeKey := "__cpa_probe__"
	if key == "reason" || key == "_" {
		probeKey = key
	}
	return a.isKeyRemovedByCleaner(parentPath, probeKey)
}

func (a *VertexPartialArgsAssembler) isKeyRemovedByCleaner(path []pathSegment, probeKey string) bool {
	if a.cleaner == nil {
		return false
	}
	var keyBuf strings.Builder
	keyBuf.WriteByte('$')
	for _, seg := range path {
		if seg.kind == segmentArrayIndex {
			keyBuf.WriteString("[]")
		} else {
			keyBuf.WriteByte('{')
			keyBuf.WriteString(strconv.Itoa(len(seg.key)))
			keyBuf.WriteByte(':')
			keyBuf.WriteString(seg.key)
			keyBuf.WriteByte('}')
		}
	}
	keyBuf.WriteByte('/')
	keyBuf.WriteString(strconv.Itoa(len(probeKey)))
	keyBuf.WriteByte(':')
	keyBuf.WriteString(probeKey)
	cacheKey := keyBuf.String()
	if cached, ok := a.closedEmptyCache[cacheKey]; ok {
		return cached
	}
	probeJSON := buildSegmentProbeJSON(path, probeKey)
	cleaned := a.cleaner(probeJSON)
	removed := false
	if gjson.Valid(cleaned) {
		removed = !segmentProbeKeyExists(gjson.Parse(cleaned), path, probeKey)
	}
	a.closedEmptyCache[cacheKey] = removed
	return removed
}

func buildSegmentProbeJSON(path []pathSegment, probeKey string) string {
	inner := `{"` + probeKey + `":1}`
	for i := len(path) - 1; i >= 0; i-- {
		seg := path[i]
		if seg.kind == segmentArrayIndex {
			inner = "[" + inner + "]"
		} else {
			var b strings.Builder
			b.WriteString(`{"`)
			appendEscapedJSONStringContent(&b, seg.key)
			b.WriteString(`":`)
			b.WriteString(inner)
			b.WriteByte('}')
			inner = b.String()
		}
	}
	return inner
}

func segmentProbeKeyExists(cur gjson.Result, path []pathSegment, probeKey string) bool {
	for _, seg := range path {
		if seg.kind == segmentArrayIndex {
			arr := cur.Array()
			if len(arr) == 0 {
				return false
			}
			cur = arr[0]
		} else {
			if !cur.IsObject() {
				return false
			}
			next, ok := cur.Map()[seg.key]
			if !ok {
				return false
			}
			cur = next
		}
	}
	if !cur.IsObject() {
		return false
	}
	_, ok := cur.Map()[probeKey]
	return ok
}

func (a *VertexPartialArgsAssembler) hasHeldNodes(n *vertexASTNode) bool {
	if n == nil {
		return false
	}
	if n.holdUntilFinish {
		return true
	}
	switch n.kind {
	case vertexNodeObject:
		for _, k := range n.keys {
			if a.hasHeldNodes(n.children[k]) {
				return true
			}
		}
	case vertexNodeArray:
		for i := 0; i <= n.maxIdx; i++ {
			if a.hasHeldNodes(n.elements[i]) {
				return true
			}
		}
	}
	return false
}

func (a *VertexPartialArgsAssembler) clearHeldFlags(n *vertexASTNode) {
	if n == nil {
		return
	}
	n.holdUntilFinish = false
	for _, k := range n.keys {
		a.clearHeldFlags(n.children[k])
	}
	for i := 0; i <= n.maxIdx; i++ {
		a.clearHeldFlags(n.elements[i])
	}
}

func (a *VertexPartialArgsAssembler) serializeNode(n *vertexASTNode, out *strings.Builder) {
	if n == nil {
		out.WriteString("null")
		return
	}
	switch n.kind {
	case vertexNodeObject:
		out.WriteByte('{')
		for i, k := range n.keys {
			if i > 0 {
				out.WriteByte(',')
			}
			out.WriteByte('"')
			appendEscapedJSONStringContent(out, k)
			out.WriteString(`":`)
			a.serializeNode(n.children[k], out)
		}
		out.WriteByte('}')
	case vertexNodeArray:
		out.WriteByte('[')
		for i := 0; i <= n.maxIdx; i++ {
			if i > 0 {
				out.WriteByte(',')
			}
			a.serializeNode(n.elements[i], out)
		}
		out.WriteByte(']')
	case vertexNodeString:
		out.WriteByte('"')
		out.WriteString(n.strBuf.String())
		out.WriteByte('"')
	case vertexNodeScalar:
		if n.scalarRaw == "" {
			out.WriteString("null")
		} else {
			out.WriteString(n.scalarRaw)
		}
	default:
		out.WriteString("null")
	}
}

func (a *VertexPartialArgsAssembler) pruneHeldNodes(n *vertexASTNode, cleaned gjson.Result) {
	if n == nil {
		return
	}
	switch n.kind {
	case vertexNodeObject:
		cleanedMap := cleaned.Map()
		kept := make([]string, 0, len(n.keys))
		kept = append(kept, n.keys[:n.objNextIdx]...)
		for i := n.objNextIdx; i < len(n.keys); i++ {
			k := n.keys[i]
			child := n.children[k]
			if child == nil {
				continue
			}
			cleanedChild, exists := cleanedMap[k]
			if child.holdUntilFinish && !exists {
				delete(n.children, k)
				continue
			}
			child.holdUntilFinish = false
			a.pruneHeldNodes(child, cleanedChild)
			kept = append(kept, k)
		}
		n.keys = kept
	case vertexNodeArray:
		cleanedArr := cleaned.Array()
		for i := 0; i <= n.maxIdx; i++ {
			elem := n.elements[i]
			if elem == nil {
				continue
			}
			elem.holdUntilFinish = false
			var cleanedElem gjson.Result
			if i < len(cleanedArr) {
				cleanedElem = cleanedArr[i]
			}
			a.pruneHeldNodes(elem, cleanedElem)
		}
	}
}

func (a *VertexPartialArgsAssembler) emitNode(node *vertexASTNode, final bool) bool {
	if a.err != nil || node == nil || node.holdUntilFinish {
		return false
	}
	switch node.kind {
	case vertexNodeString:
		if !node.strOpened {
			if !a.writeByte('"') {
				return false
			}
			node.strOpened = true
		}
		if node.strEmitted < node.strBuf.Len() {
			if !a.writeString(node.strBuf.String()[node.strEmitted:]) {
				return false
			}
			node.strEmitted = node.strBuf.Len()
		}
		if node.stringOpen && !final {
			return false
		}
		if !node.strClosed {
			if !a.writeByte('"') {
				return false
			}
			node.strClosed = true
			node.stringOpen = false
		}
		return true

	case vertexNodeScalar:
		if !node.scalarEmitted {
			if !a.writeString(node.scalarRaw) {
				return false
			}
			node.scalarEmitted = true
		}
		return true

	case vertexNodeObject:
		if node.containerClosed {
			return true
		}
		if !node.containerOpened {
			if !final && len(node.keys) > 0 && node.children[node.keys[0]] != nil && node.children[node.keys[0]].holdUntilFinish {
				return false
			}
			if !a.writeByte('{') {
				return false
			}
			node.containerOpened = true
		}
		for node.objNextIdx < len(node.keys) {
			idx := node.objNextIdx
			key := node.keys[idx]
			child := node.children[key]
			if child != nil && child.holdUntilFinish {
				return false
			}
			if !node.commaEmitted[idx] {
				if idx > 0 {
					if !a.writeByte(',') {
						return false
					}
				}
				if !a.writeByte('"') || !a.writeEscapedString(key) || !a.writeString(`":`) {
					return false
				}
				node.commaEmitted[idx] = true
			}
			if !a.emitNode(child, final) {
				return false
			}
			node.objNextIdx++
		}
		if !final {
			return false
		}
		if !a.writeByte('}') {
			return false
		}
		node.containerClosed = true
		return true

	case vertexNodeArray:
		if node.containerClosed {
			return true
		}
		if !node.containerOpened {
			if !a.writeByte('[') {
				return false
			}
			node.containerOpened = true
		}
		for node.arrNextIdx <= node.maxIdx {
			idx := node.arrNextIdx
			elem := node.elements[idx]
			if elem == nil {
				if !final {
					return false
				}
				if !node.commaEmitted[idx] {
					if idx > 0 {
						if !a.writeByte(',') {
							return false
						}
					}
					if !a.writeString("null") {
						return false
					}
					node.commaEmitted[idx] = true
				}
				node.arrNextIdx++
				continue
			}
			if !node.commaEmitted[idx] {
				if idx > 0 {
					if !a.writeByte(',') {
						return false
					}
				}
				node.commaEmitted[idx] = true
			}
			if !a.emitNode(elem, final) {
				return false
			}
			node.arrNextIdx++
		}
		if !final {
			return false
		}
		if !a.writeByte(']') {
			return false
		}
		node.containerClosed = true
		return true
	}
	return false
}

func formatVertexJSONNumber(res gjson.Result) string {
	if res.Type == gjson.Number {
		raw := strings.TrimSpace(res.Raw)
		if raw != "" {
			return raw
		}
	}
	if res.Type == gjson.String {
		s := strings.TrimSpace(res.String())
		if s != "" && gjson.Valid(s) && gjson.Parse(s).Type == gjson.Number {
			return s
		}
	}
	return strconv.FormatFloat(res.Float(), 'f', -1, 64)
}

func appendEscapedJSONStringContent(buf *strings.Builder, s string) {
	const hex = "0123456789abcdef"
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if c < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteByte(hex[c>>4])
				buf.WriteByte(hex[c&0x0f])
			} else {
				buf.WriteByte(c)
			}
		}
	}
}

func parseHex4(s string) (rune, bool) {
	if len(s) < 4 {
		return 0, false
	}
	var val rune
	for i := 0; i < 4; i++ {
		c := s[i]
		val <<= 4
		switch {
		case c >= '0' && c <= '9':
			val |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			val |= rune(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			val |= rune(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return val, true
}

func parseVertexJSONPath(rawPath string) ([]pathSegment, bool) {
	s := strings.TrimSpace(rawPath)
	if s == "" || s == "$" {
		return nil, true
	}
	if !strings.HasPrefix(s, "$") {
		return nil, false
	}
	s = s[1:]
	var segments []pathSegment
	i := 0
	for i < len(s) {
		if len(segments) >= maxVertexPartialArgsDepth {
			return nil, false
		}
		switch s[i] {
		case '.':
			i++
			if i >= len(s) {
				return nil, false
			}
			start := i
			for i < len(s) && s[i] != '.' && s[i] != '[' {
				if s[i] < 0x20 {
					return nil, false
				}
				i++
			}
			if i == start {
				return nil, false
			}
			segments = append(segments, pathSegment{
				kind: segmentObjectKey,
				key:  s[start:i],
			})
		case '[':
			i++
			if i >= len(s) {
				return nil, false
			}
			if s[i] == '\'' || s[i] == '"' {
				quote := s[i]
				i++
				var keyBuf strings.Builder
				closed := false
				for i < len(s) {
					ch := s[i]
					i++
					if ch == '\\' {
						if i >= len(s) {
							return nil, false
						}
						esc := s[i]
						i++
						switch esc {
						case '"', '\'', '\\', '/':
							keyBuf.WriteByte(esc)
						case 'n':
							keyBuf.WriteByte('\n')
						case 'r':
							keyBuf.WriteByte('\r')
						case 't':
							keyBuf.WriteByte('\t')
						case 'b':
							keyBuf.WriteByte('\b')
						case 'f':
							keyBuf.WriteByte('\f')
						case 'u':
							r1, okHex := parseHex4(s[i:])
							if !okHex {
								return nil, false
							}
							i += 4
							if r1 >= 0xd800 && r1 <= 0xdbff {
								if i+6 > len(s) || s[i] != '\\' || s[i+1] != 'u' {
									return nil, false
								}
								r2, okLow := parseHex4(s[i+2:])
								if !okLow || r2 < 0xdc00 || r2 > 0xdfff {
									return nil, false
								}
								i += 6
								keyBuf.WriteRune(utf16.DecodeRune(r1, r2))
							} else if r1 >= 0xdc00 && r1 <= 0xdfff {
								return nil, false
							} else {
								keyBuf.WriteRune(r1)
							}
						default:
							return nil, false
						}
						continue
					}
					if ch < 0x20 {
						return nil, false
					}
					if ch == quote {
						closed = true
						break
					}
					keyBuf.WriteByte(ch)
				}
				if !closed || i >= len(s) || s[i] != ']' {
					return nil, false
				}
				i++
				segments = append(segments, pathSegment{
					kind: segmentObjectKey,
					key:  keyBuf.String(),
				})
			} else {
				start := i
				idxVal := 0
				for i < len(s) && s[i] >= '0' && s[i] <= '9' {
					digit := int(s[i] - '0')
					if idxVal > (maxVertexPartialArgsArrayIndex-digit)/10 {
						return nil, false
					}
					idxVal = idxVal*10 + digit
					i++
				}
				if i == start || i >= len(s) || s[i] != ']' {
					return nil, false
				}
				i++
				segments = append(segments, pathSegment{
					kind:  segmentArrayIndex,
					index: idxVal,
				})
			}
		default:
			return nil, false
		}
	}
	return segments, true
}

// VertexStreamingToolCall tracks an in-progress Vertex streaming function call
// across SSE chunks for a single candidate or stream.
type VertexStreamingToolCall struct {
	RawName    string
	Name       string
	UpstreamID string
	ID         string
	Index      int
	Assembler  *VertexPartialArgsAssembler
}

// NewVertexStreamingToolCall creates a new active streaming tool call tracker.
func NewVertexStreamingToolCall(rawName, name, upstreamID, id string, index int) *VertexStreamingToolCall {
	return &VertexStreamingToolCall{
		RawName:    rawName,
		Name:       name,
		UpstreamID: upstreamID,
		ID:         id,
		Index:      index,
		Assembler:  NewVertexPartialArgsAssembler(),
	}
}

// CanContinueWith reports whether fc belongs to this in-progress streaming tool call.
func (c *VertexStreamingToolCall) CanContinueWith(fc gjson.Result) bool {
	if c == nil || c.Assembler == nil || !fc.Exists() || !fc.IsObject() {
		return false
	}
	if fc.Get("args").Exists() && !IsVertexStreamingFunctionCall(fc) {
		return false
	}
	fcID := strings.TrimSpace(fc.Get("id").String())
	if fcID != "" && c.UpstreamID != "" && fcID != c.UpstreamID {
		return false
	}
	rawName := strings.TrimSpace(fc.Get("name").String())
	if rawName == "" {
		return true
	}
	if rawName != c.RawName {
		return false
	}
	if fcID != "" && c.UpstreamID != "" && fcID == c.UpstreamID {
		return true
	}
	return c.Assembler.CanAcceptContinuation(fc)
}

// CanAcceptContinuation reports whether fc's partialArgs can continue the current
// in-progress argument tree without overwriting any already-completed leaf value.
func (a *VertexPartialArgsAssembler) CanAcceptContinuation(fc gjson.Result) bool {
	if a == nil || a.err != nil {
		return false
	}
	partialArgs := fc.Get("partialArgs")
	if !partialArgs.Exists() {
		partialArgs = fc.Get("partial_args")
	}
	if !partialArgs.Exists() {
		return true
	}
	if !partialArgs.IsArray() {
		return false
	}
	compatible := true
	partialArgs.ForEach(func(_, item gjson.Result) bool {
		if !item.IsObject() {
			compatible = false
			return false
		}
		hasPathField := item.Get("jsonPath").Exists() || item.Get("json_path").Exists()
		if !hasPathField {
			return true
		}
		rawPath := item.Get("jsonPath").String()
		if !item.Get("jsonPath").Exists() {
			rawPath = item.Get("json_path").String()
		}
		segments, okPath := parseVertexJSONPath(rawPath)
		if !okPath || len(segments) == 0 {
			return true
		}
		node := a.findNode(segments)
		if node == nil || node.kind == vertexNodeUnset {
			return true
		}
		innerWillContinue := item.Get("willContinue").Bool() || item.Get("will_continue").Bool()
		strRes := item.Get("stringValue")
		if !strRes.Exists() {
			strRes = item.Get("string_value")
		}
		numRes := item.Get("numberValue")
		if !numRes.Exists() {
			numRes = item.Get("number_value")
		}
		boolRes := item.Get("boolValue")
		if !boolRes.Exists() {
			boolRes = item.Get("bool_value")
		}
		nullRes := item.Get("nullValue")
		if !nullRes.Exists() {
			nullRes = item.Get("null_value")
		}
		hasString := strRes.Exists() && strRes.Type != gjson.Null
		hasNumber := numRes.Exists() && numRes.Type != gjson.Null
		hasBool := boolRes.Exists() && boolRes.Type != gjson.Null
		hasNull := nullRes.Exists() && !hasString && !hasNumber && !hasBool
		hasAnyValue := hasString || hasNumber || hasBool || hasNull
		if !hasAnyValue && !innerWillContinue {
			return true
		}
		if hasString || innerWillContinue {
			if node.kind != vertexNodeString || node.strClosed || !node.stringOpen {
				compatible = false
				return false
			}
			return true
		}
		compatible = false
		return false
	})
	return compatible
}
