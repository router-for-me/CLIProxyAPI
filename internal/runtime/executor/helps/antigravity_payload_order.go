package helps

import (
	"bytes"
	"cmp"
	"slices"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

type agentJSONMode uint8

const (
	agentOpaque agentJSONMode = iota
	agentDictionary
	agentSchemaProperties
	agentSchemaDefinitions
	agentEnvelope
	agentRequest
	agentContent
	agentPart
	agentCall
	agentResponse
	agentTool
	agentDeclaration
	agentLabels
	agentGeneration
	agentThinking
	agentSchema
)

type agentFieldGroup struct {
	keys string
	mode agentJSONMode
}

type agentFieldOrder struct {
	// Zero means the sample does not specify a rank, even for a recognized field.
	rank int
	mode agentJSONMode
}

func agentFields(sampleKeys string, children ...agentFieldGroup) map[string]agentFieldOrder {
	fields := make(map[string]agentFieldOrder)
	for i, key := range strings.Fields(sampleKeys) {
		fields[key] = agentFieldOrder{rank: i + 1}
	}
	for _, group := range children {
		for _, key := range strings.Fields(group.keys) {
			field := fields[key]
			field.mode = group.mode
			fields[key] = field
		}
	}
	return fields
}

// Only the first argument of each row specifies wire order. Child modes also
// recognize alternate spellings and extensions, but never give them a rank.
var antigravityAgentFields = [...]map[string]agentFieldOrder{
	agentEnvelope: agentFields("project requestId request model userAgent requestType",
		agentFieldGroup{"request", agentRequest},
	),
	agentRequest: agentFields("contents systemInstruction tools labels generationConfig sessionId",
		agentFieldGroup{"contents systemInstruction system_instruction", agentContent},
		agentFieldGroup{"tools", agentTool},
		agentFieldGroup{"labels", agentLabels},
		agentFieldGroup{"generationConfig generation_config", agentGeneration},
	),
	agentContent: agentFields("role parts", agentFieldGroup{"parts", agentPart}),
	agentPart: agentFields("text functionCall functionResponse thoughtSignature",
		agentFieldGroup{"functionCall function_call", agentCall},
		agentFieldGroup{"functionResponse function_response", agentResponse},
	),
	agentCall: agentFields("id name args", agentFieldGroup{"args", agentDictionary}),
	agentResponse: agentFields("id name response",
		agentFieldGroup{"response", agentDictionary},
		agentFieldGroup{"parts", agentPart},
	),
	agentTool: agentFields("functionDeclarations", agentFieldGroup{"functionDeclarations function_declarations", agentDeclaration}),
	agentDeclaration: agentFields("name description parameters",
		agentFieldGroup{"parameters parametersJsonSchema parameters_json_schema response responseJsonSchema response_json_schema", agentSchema},
	),
	agentLabels: agentFields("cascade_id last_step_index model_enum request_id root_cascade_id trajectory_id used_claude used_claude_conservative used_non_gemini_model"),
	agentGeneration: agentFields("maxOutputTokens thinkingConfig",
		agentFieldGroup{"thinkingConfig thinking_config", agentThinking},
		agentFieldGroup{"responseSchema response_schema responseJsonSchema response_json_schema", agentSchema},
	),
	agentThinking: agentFields("includeThoughts thinkingLevel"),
	agentSchema: agentFields("type description properties required items enum minLength minimum minItems",
		agentFieldGroup{"items additionalProperties additionalItems propertyNames contains prefixItems anyOf oneOf allOf not if then else unevaluatedItems unevaluatedProperties", agentSchema},
		agentFieldGroup{"properties", agentSchemaProperties},
		agentFieldGroup{"$defs definitions patternProperties dependentSchemas dependencies", agentSchemaDefinitions},
	),
}

// SerializeAntigravityPayload compacts all model requests without changing doc.
// Only agent requests use the reference key order.
func SerializeAntigravityPayload(doc *ojson.Doc, orderAgentKeys bool) []byte {
	return serializeOwnedAntigravityPayload(doc.Bytes(), orderAgentKeys)
}

func serializeOwnedAntigravityPayload(raw []byte, orderAgentKeys bool) []byte {
	if !ojson.ValidBytes(raw) {
		return raw
	}
	p := agentOrderer{raw: raw}
	p.compactWhitespace()
	if orderAgentKeys {
		p.pos = 0
		if changes := p.plan(); changes.first != 0 {
			return p.emit(changes.first)
		}
	}
	return p.raw
}

// Remove only JSON whitespace between tokens. Copy runs in place, skipping
// entire string literals so their contents and escape sequences stay intact.
func (p *agentOrderer) compactWhitespace() {
	start, written := 0, 0
	for p.pos < len(p.raw) {
		switch p.raw[p.pos] {
		case '"':
			p.skipString()
		case ' ', '\t', '\r', '\n':
			written += copy(p.raw[written:], p.raw[start:p.pos])
			p.space()
			start = p.pos
		default:
			p.pos++
		}
	}
	if start != 0 {
		written += copy(p.raw[written:], p.raw[start:])
		p.raw = p.raw[:written]
	}
}

// Object edits are linked in source order; zero is the end of a list. A moved
// member records the first edit within its span, so emitting it never scans
// edits that belong to other members, even in a reordered ancestor.
type agentEditList struct{ first, last int }

type agentReorder struct {
	start, end int // Object interior, not including braces.
	from, to   int // Moved member range in moves.
	next       int
}

type agentSpan struct {
	start, end int
	firstEdit  int
}

type agentMember struct {
	span agentSpan
	rank int
	key  []byte // Decoded key for lexical modes only.
}

type agentOrderer struct {
	raw     []byte
	pos     int
	objects []agentReorder
	moves   []agentSpan
}

func (p *agentOrderer) join(list *agentEditList, suffix agentEditList) {
	if suffix.first == 0 {
		return
	}
	if list.first == 0 {
		list.first = suffix.first
	} else {
		p.objects[list.last-1].next = suffix.first
	}
	list.last = suffix.last
}

// Emission keeps continuations only for reordered objects. A continuation
// resumes with the next moved member, then with the source after the edit.
func (p *agentOrderer) emit(firstEdit int) []byte {
	type continuation struct {
		rest     agentSpan
		next, to int
	}
	var small [12]continuation
	stack := small[:0]
	dst := make([]byte, 0, len(p.raw))
	span := agentSpan{end: len(p.raw), firstEdit: firstEdit}
	for {
		if span.firstEdit != 0 {
			edit := p.objects[span.firstEdit-1]
			if edit.start < span.end {
				dst = append(dst, p.raw[span.start:edit.start]...)
				stack = append(stack, continuation{
					rest: agentSpan{start: edit.end, end: span.end, firstEdit: edit.next},
					next: edit.from + 1, to: edit.to,
				})
				span = p.moves[edit.from]
				continue
			}
		}
		dst = append(dst, p.raw[span.start:span.end]...)
		if len(stack) == 0 {
			return dst
		}
		frame := &stack[len(stack)-1]
		if frame.next < frame.to {
			dst = append(dst, ',')
			span = p.moves[frame.next]
			frame.next++
		} else {
			span = frame.rest
			stack = stack[:len(stack)-1]
		}
	}
}

// plan walks validated JSON with an explicit stack, including nested arrays.
// Each frame owns the suffix of members for its object while it is active.
// Closing a container restores its parent's member before advancing it.
func (p *agentOrderer) plan() agentEditList {
	type container struct {
		start, base int
		changes     agentEditList
		mode        agentJSONMode
		close       byte
		unordered   bool
	}
	var smallStack [12]container
	var smallMembers [48]agentMember
	stack, members := smallStack[:0], smallMembers[:0]
	mode := agentEnvelope
	for {
		switch token := p.raw[p.pos]; {
		case mode != agentOpaque && (token == '{' || token == '['):
			close := byte('}')
			if token == '[' {
				close = ']'
			}
			p.pos++
			start := p.pos
			p.space()
			if p.raw[p.pos] != close {
				stack = append(stack, container{start: start, base: len(members), mode: mode, close: close})
				if close == '}' {
					member, childMode := p.member(mode, start)
					members = append(members, member)
					mode = childMode
				}
				continue
			}
			p.pos++ // empty container
		default:
			p.skipValue()
		}

		// Complete the value and any containers that end at this point.
		var changes agentEditList
		for {
			if len(stack) == 0 {
				return changes
			}
			frame := &stack[len(stack)-1]
			p.join(&frame.changes, changes)
			p.space()
			if frame.close == '}' {
				span := &members[len(members)-1].span
				span.end, span.firstEdit = p.pos, changes.first
			}
			if p.raw[p.pos] == frame.close {
				changes = frame.changes
				if frame.unordered {
					changes = p.record(frame.start, members[frame.base:])
				}
				p.pos++
				members = members[:frame.base]
				stack = stack[:len(stack)-1]
				continue
			}
			p.pos++ // comma
			start := p.pos
			p.space()
			mode = frame.mode
			if frame.close == '}' {
				member, childMode := p.member(mode, start)
				if !frame.unordered && compareAgentMembers(members[len(members)-1], member) > 0 {
					frame.unordered = true
				}
				members = append(members, member)
				mode = childMode
			}
			break
		}
	}
}

func (p *agentOrderer) member(mode agentJSONMode, start int) (agentMember, agentJSONMode) {
	keyStart := p.pos
	p.skipString()
	key := p.raw[keyStart+1 : p.pos-1]
	if bytes.IndexByte(key, '\\') >= 0 {
		key = []byte(ojson.Parse(string(p.raw[keyStart:p.pos])).Str)
	}
	p.space()
	p.pos++ // colon
	p.space()

	member := agentMember{span: agentSpan{start: start}}
	switch mode {
	case agentDictionary:
		member.key = key
		return member, agentDictionary
	case agentSchemaProperties:
		member.key = key
		return member, agentSchema
	case agentSchemaDefinitions:
		// Traverse each schema without sorting the definition names themselves.
		return member, agentSchema
	default:
		fields := antigravityAgentFields[mode]
		field := fields[string(key)]
		member.rank = field.rank
		if member.rank == 0 {
			member.rank = len(fields) + 1
		}
		return member, field.mode
	}
}

func compareAgentMembers(a, b agentMember) int {
	if order := cmp.Compare(a.rank, b.rank); order != 0 {
		return order
	}
	if order := bytes.Compare(a.key, b.key); order != 0 {
		return order
	}
	// The input offset makes ties stable, including duplicates and extensions.
	return cmp.Compare(a.span.start, b.span.start)
}

func (p *agentOrderer) record(start int, members []agentMember) agentEditList {
	slices.SortFunc(members, compareAgentMembers)
	if p.objects == nil {
		// Small requests rarely need more than a few edits. Keep the larger
		// reserve for histories with many declarations and tool calls.
		capacity := 4
		if len(p.raw) >= 32<<10 {
			capacity = 48
		}
		p.objects = make([]agentReorder, 0, capacity)
		p.moves = make([]agentSpan, 0, max(3*capacity, len(members)))
	}
	from := len(p.moves)
	for _, member := range members {
		p.moves = append(p.moves, member.span)
	}
	p.objects = append(p.objects, agentReorder{start: start, end: p.pos, from: from, to: len(p.moves)})
	return agentEditList{len(p.objects), len(p.objects)}
}

func (p *agentOrderer) space() {
	for p.pos < len(p.raw) && p.raw[p.pos] <= ' ' {
		p.pos++
	}
}

func (p *agentOrderer) skipString() {
	start := p.pos + 1
	for pos := start; ; {
		quote := pos + bytes.IndexByte(p.raw[pos:], '"')
		before := quote
		for before > start && p.raw[before-1] == '\\' {
			before--
		}
		pos = quote + 1
		if (quote-before)%2 == 0 { // An even number of backslashes leaves the quote unescaped.
			p.pos = pos
			return
		}
	}
}

func (p *agentOrderer) skipValue() {
	switch p.raw[p.pos] {
	case '"':
		p.skipString()
	case '{', '[':
		// Opaque objects/arrays stay byte-for-byte intact regardless of depth.
		for depth := 0; ; {
			switch p.raw[p.pos] {
			case '"':
				p.skipString()
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			p.pos++
			if depth == 0 {
				return
			}
		}
	default:
		for p.pos < len(p.raw) {
			switch p.raw[p.pos] {
			case ' ', '\n', '\r', '\t', ',', '}', ']':
				return
			}
			p.pos++
		}
	}
}
