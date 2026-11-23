package common

import (
	"errors"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// NormalizeApplyPatchResponsesRequest adapts declarations and explicit custom patch history.
// Winners are collected before rewriting: normalization must not change declaration precedence.
func NormalizeApplyPatchResponsesRequest(raw []byte) ([]byte, error) {
	if !ojson.ValidBytes(raw) {
		return raw, errors.New("invalid Responses request JSON")
	}
	root := util.ParseGJSONBytesNoCopy(raw)
	winners := util.CollectResponsesToolWinners(root)
	affected := map[string]bool{}
	for _, d := range util.CollectResponsesToolDescriptors(root) {
		if applypatch.IsCustomTool(d.Tool) {
			affected[d.Name] = true
		}
	}
	var normalizeTools func(ojson.Result, string) []byte
	normalizeTools = func(tools ojson.Result, namespace string) []byte {
		var items [][]byte
		for _, tool := range tools.Array() {
			item := []byte(tool.Raw)
			if tool.Get("type").String() == "namespace" {
				for _, key := range []string{"tools", "children"} {
					if children := tool.Get(key); children.IsArray() {
						item, _ = ojson.SetRawBytes(item, key, normalizeTools(children, tool.Get("name").String()))
						break
					}
				}
			} else {
				name := tool.Get("name").String()
				if name == "" {
					name = tool.Get("function.name").String()
				}
				qualified := util.QualifyResponsesNamespaceToolName(namespace, name)
				winner, ok := winners[qualified]
				if ok && affected[qualified] {
					if winner.Tool.Index != tool.Index {
						continue
					}
					if applypatch.IsCustomTool(tool) {
						itemDoc := ojson.NewDoc(item)
						_ = itemDoc.Set("type", "function")
						_ = itemDoc.Set("description", applypatch.Description(tool))
						_ = itemDoc.SetRawBytes("parameters", applypatch.Parameters())
						_ = itemDoc.Delete("format")
						item = itemDoc.Bytes()
					}
				}
			}
			items = append(items, item)
		}
		return JoinRawArray(items)
	}
	doc := ojson.NewDoc(raw)
	if tools := root.Get("tools"); tools.IsArray() {
		_ = doc.SetRawBytes("tools", normalizeTools(tools, ""))
	}
	patchHistory := map[string]bool{}
	for _, item := range root.Get("input").Array() {
		if item.Get("type").String() == "custom_tool_call" && strings.TrimSpace(item.Get("name").String()) == "apply_patch" {
			patchHistory[item.Get("call_id").String()] = true
		}
	}
	for i, item := range root.Get("input").Array() {
		path := fmt.Sprintf("input.%d", i)
		switch item.Get("type").String() {
		case "additional_tools":
			if tools := item.Get("tools"); tools.IsArray() {
				_ = doc.SetRawBytes(path+".tools", normalizeTools(tools, ""))
			}
		case "custom_tool_call":
			if strings.TrimSpace(item.Get("name").String()) != "apply_patch" {
				continue
			}
			if item.Get("input").Type != ojson.String {
				return nil, errors.New("apply_patch history input must be a string")
			}
			_ = doc.Set(path+".type", "function_call")
			_ = doc.Set(path+".arguments", applypatch.WrapInput(item.Get("input").String()))
			_ = doc.Delete(path + ".input")
		case "custom_tool_call_output":
			if patchHistory[item.Get("call_id").String()] {
				_ = doc.Set(path+".type", "function_call_output")
			}
		}
	}
	var normalizeChoice func(ojson.Result) []byte
	normalizeChoice = func(choice ojson.Result) []byte {
		choiceDoc := ojson.NewDoc([]byte(choice.Raw))
		name := choice.Get("name").String()
		namespace := choice.Get("namespace").String()
		if d, ok := winners[util.QualifyResponsesNamespaceToolName(namespace, name)]; ok && applypatch.IsCustomTool(d.Tool) && choice.Get("type").String() == "custom" {
			_ = choiceDoc.Set("type", "function")
		}
		for i, child := range choice.Get("tools").Array() {
			_ = choiceDoc.SetRawBytes(fmt.Sprintf("tools.%d", i), normalizeChoice(child))
		}
		return choiceDoc.Bytes()
	}
	if choice := root.Get("tool_choice"); choice.IsObject() {
		_ = doc.SetRawBytes("tool_choice", normalizeChoice(choice))
	}
	return doc.Bytes(), nil
}

type responsesPatchRecord struct {
	state                                    ApplyPatchCallState
	kind, qualified, source                  string
	patch, named, added, inputDone, itemDone bool
	snapshot, completedItem                  string
	hasSnapshot                              bool
	pending                                  [][]byte
	evidence                                 error
}

// ApplyPatchResponsesBridge handles JSON event payloads, without SSE framing.
// Identity evidence is retained even before a call receives its name.
// A bridge is local to one response and must not be shared between goroutines.
type ApplyPatchResponsesBridge struct {
	ApplyPatchErrorState
	tools                               map[string]util.ResponsesToolDescriptor
	records                             []*responsesPatchRecord
	byItemID                            map[string]*responsesPatchRecord
	byCallID                            map[string]*responsesPatchRecord
	byOutputIndex                       map[int]*responsesPatchRecord
	sequence, lastSequence              int
	responseID                          string
	failed, terminal, active, converted bool
}

// NewApplyPatchResponsesBridge resolves original declarations before any normalization.
func NewApplyPatchResponsesBridge(originalRequest []byte) *ApplyPatchResponsesBridge {
	b := &ApplyPatchResponsesBridge{tools: util.CollectResponsesToolWinners(util.ParseGJSONBytesNoCopy(originalRequest)), byItemID: map[string]*responsesPatchRecord{}, byCallID: map[string]*responsesPatchRecord{}, byOutputIndex: map[int]*responsesPatchRecord{}}
	for _, d := range b.tools {
		if applypatch.IsCustomTool(d.Tool) {
			b.active = true
		}
	}
	return b
}

func (b *ApplyPatchResponsesBridge) next() int { b.sequence++; return b.sequence }
func (b *ApplyPatchResponsesBridge) failure(err error) ([][]byte, error) {
	if b.failed || b.terminal {
		return nil, nil
	}
	b.failed = true
	b.SetToolInputError(err)
	return [][]byte{ApplyPatchFailure(b.responseID, b.next())}, err
}

// Fail terminates an executor-owned bridge with the same one-shot failure contract.
func (b *ApplyPatchResponsesBridge) Fail(err error) ([][]byte, error) { return b.failure(err) }

func (b *ApplyPatchResponsesBridge) descriptor(item ojson.Result) (util.ResponsesToolDescriptor, bool) {
	name := util.QualifyResponsesNamespaceToolName(item.Get("namespace").String(), item.Get("name").String())
	d, ok := b.tools[name]
	return d, ok
}

// resolve checks every supplied identity, not just the first usable key. Conflicting
// unmatched keys and multiple matched records retain evidence until patch provenance is known.
func (b *ApplyPatchResponsesBridge) resolve(event, item ojson.Result) (*responsesPatchRecord, error) {
	ids := []string{event.Get("item_id").String(), item.Get("id").String()}
	calls := []string{event.Get("call_id").String(), item.Get("call_id").String()}
	index := event.Get("output_index")
	matched := map[*responsesPatchRecord]bool{}
	for _, id := range ids {
		if r := b.byItemID[id]; id != "" && r != nil {
			matched[r] = true
		}
	}
	for _, id := range calls {
		if r := b.byCallID[id]; id != "" && r != nil {
			matched[r] = true
		}
	}
	if index.Exists() {
		if r := b.byOutputIndex[int(index.Int())]; r != nil {
			matched[r] = true
		}
	}
	var r *responsesPatchRecord
	for _, candidate := range b.records {
		if matched[candidate] {
			r = candidate
			break
		}
	}
	if r == nil {
		r = &responsesPatchRecord{state: ApplyPatchCallState{OutputIndex: -1}}
		b.records = append(b.records, r)
	}
	bad := len(matched) > 1
	for _, id := range ids {
		if id != "" && r.state.ItemID != "" && r.state.ItemID != id {
			bad = true
		}
	}
	for _, id := range calls {
		if id != "" && r.state.CallID != "" && r.state.CallID != id {
			bad = true
		}
	}
	if ids[0] != "" && ids[1] != "" && ids[0] != ids[1] {
		bad = true
	}
	if calls[0] != "" && calls[1] != "" && calls[0] != calls[1] {
		bad = true
	}
	if index.Exists() && r.state.OutputIndex >= 0 && r.state.OutputIndex != int(index.Int()) {
		bad = true
	}
	d, known := b.descriptor(item)
	incomingPatch := known && applypatch.IsCustomTool(d.Tool) && item.Get("type").String() != "custom_tool_call"
	if bad {
		errIdentity := errors.New("conflicting apply_patch call identity")
		r.evidence = errIdentity
		for candidate := range matched {
			candidate.evidence = errIdentity
			if candidate.patch {
				incomingPatch = true
			}
		}
		// Keep aliases for unmatched conflicting keys, too. Their later patch provenance
		// must not create a fresh record and erase the earlier contradiction.
		for _, id := range ids {
			if id != "" && b.byItemID[id] == nil {
				b.byItemID[strings.Clone(id)] = r
			}
		}
		for _, id := range calls {
			if id != "" && b.byCallID[id] == nil {
				b.byCallID[strings.Clone(id)] = r
			}
		}
		if index.Exists() && b.byOutputIndex[int(index.Int())] == nil {
			b.byOutputIndex[int(index.Int())] = r
		}
		if r.patch || incomingPatch {
			return nil, errIdentity
		}
	} else {
		for _, id := range ids {
			if id != "" {
				clonedID := strings.Clone(id)
				r.state.ItemID = clonedID
				b.byItemID[clonedID] = r
			}
		}
		for _, id := range calls {
			if id != "" {
				clonedID := strings.Clone(id)
				r.state.CallID = clonedID
				b.byCallID[clonedID] = r
			}
		}
		if index.Exists() {
			r.state.OutputIndex = int(index.Int())
			b.byOutputIndex[r.state.OutputIndex] = r
		}
	}
	kind := item.Get("type").String()
	if kind != "" {
		if r.kind != "" && r.kind != kind {
			r.evidence = errors.New("conflicting apply_patch call type")
		}
		if r.kind == "" {
			r.kind = strings.Clone(kind)
		}
	}
	name := item.Get("name").String()
	if name != "" {
		qualified := util.QualifyResponsesNamespaceToolName(item.Get("namespace").String(), name)
		if known {
			qualified = d.Name
		}
		if r.named && r.qualified != qualified {
			r.evidence = errors.New("conflicting apply_patch call name")
		}
		r.named = true
		r.qualified = strings.Clone(qualified)
		r.state.Name = strings.Clone(name)
		r.state.Namespace = strings.Clone(item.Get("namespace").String())
		if known {
			r.state.Name = d.LocalName
			r.state.Namespace = d.Namespace
		}
	}
	if incomingPatch {
		r.patch = true
	}
	if r.patch && r.evidence != nil {
		return nil, r.evidence
	}
	return r, nil
}

// CheckIdentity retains every supplied key before a folded dispatcher reveals its child.
// Names are deliberately withheld: a dispatcher name is not the selected child name.
func (b *ApplyPatchResponsesBridge) CheckIdentity(event []byte) error {
	root := util.ParseGJSONBytesNoCopy(event)
	item := root.Get("item")
	if item.Exists() {
		rawDoc := ojson.NewDocString(item.Raw)
		_ = rawDoc.Delete("name")
		_ = rawDoc.Delete("namespace")
		item = util.ParseGJSONBytesNoCopy(rawDoc.Bytes())
	}
	_, errResolve := b.resolve(root, item)
	return errResolve
}

func (b *ApplyPatchResponsesBridge) restoreItem(item []byte, r *responsesPatchRecord, input string, added bool) []byte {
	itemDoc := ojson.NewDoc(item)
	if r.patch {
		_ = itemDoc.Set("type", "custom_tool_call")
		_ = itemDoc.Delete("arguments")
		_ = itemDoc.Set("input", input)
	}
	if d, ok := b.tools[r.qualified]; ok && d.Namespace != "" {
		_ = itemDoc.Set("name", d.LocalName)
		_ = itemDoc.Set("namespace", d.Namespace)
	}
	if r.patch && !added {
		if r.state.ItemID != "" {
			_ = itemDoc.Set("id", r.state.ItemID)
		}
		if r.state.CallID != "" {
			_ = itemDoc.Set("call_id", r.state.CallID)
		}
		_ = itemDoc.Set("name", r.state.Name)
	}
	return itemDoc.Bytes()
}

func (b *ApplyPatchResponsesBridge) itemEvent(kind string, item []byte, r *responsesPatchRecord) []byte {
	outDoc := ojson.NewDoc([]byte(`{"type":"","output_index":0,"sequence_number":0,"item":{}}`))
	_ = outDoc.Set("type", kind)
	_ = outDoc.Set("output_index", r.state.OutputIndex)
	_ = outDoc.Set("sequence_number", b.next())
	_ = outDoc.SetRawBytes("item", item)
	return outDoc.Bytes()
}

func (b *ApplyPatchResponsesBridge) snapshot(r *responsesPatchRecord, arguments ojson.Result, final bool) error {
	if !arguments.Exists() {
		return nil
	}
	if arguments.Type != ojson.String {
		return errors.New("apply_patch arguments snapshot must be a string")
	}
	if arguments.String() == "" && !final {
		return nil
	}
	var decoder ApplyPatchInputDecoder
	if _, errFinish := decoder.Finish(arguments.String()); errFinish != nil {
		return errFinish
	}
	if r.hasSnapshot {
		var previous ApplyPatchInputDecoder
		_, _ = previous.Finish(r.snapshot)
		if previous.Input() != decoder.Input() {
			return errors.New("conflicting apply_patch arguments snapshot")
		}
	}
	if !strings.HasPrefix(decoder.Input(), r.state.Decoder.Input()) {
		return errors.New("apply_patch snapshot conflicts with streamed input")
	}
	r.snapshot = strings.Clone(arguments.String())
	r.hasSnapshot = true
	return nil
}

func (r *responsesPatchRecord) identityReady() bool {
	return r.state.ItemID != "" && r.state.CallID != "" && r.state.OutputIndex >= 0
}

func (b *ApplyPatchResponsesBridge) patchEvent(raw []byte, r *responsesPatchRecord) ([][]byte, error) {
	if !r.identityReady() {
		return nil, errors.New("unresolved apply_patch call identity")
	}
	root := util.ParseGJSONBytesNoCopy(raw)
	kind := root.Get("type").String()
	item := root.Get("item")
	b.converted = true
	var out [][]byte
	if item.Exists() {
		if item.Get("type").String() != "function_call" {
			return nil, errors.New("conflicting apply_patch call type")
		}
		if errSnapshot := b.snapshot(r, item.Get("arguments"), kind == "response.output_item.done"); errSnapshot != nil {
			return nil, errSnapshot
		}
	}
	if !r.added {
		added := []byte(`{"type":"function_call","name":"","arguments":""}`)
		if item.Exists() {
			added = []byte(item.Raw)
		}
		addedDoc := ojson.NewDoc(added)
		_ = addedDoc.Set("name", r.state.Name)
		if r.state.ItemID != "" {
			_ = addedDoc.Set("id", r.state.ItemID)
		}
		if r.state.CallID != "" {
			_ = addedDoc.Set("call_id", r.state.CallID)
		}
		if r.state.Namespace != "" {
			_ = addedDoc.Set("namespace", r.state.Namespace)
		}
		out = append(out, b.itemEvent("response.output_item.added", b.restoreItem(addedDoc.Bytes(), r, "", true), r))
		r.added = true
	}
	switch kind {
	case "response.function_call_arguments.delta":
		fragment := root.Get("delta").String()
		if r.inputDone {
			if fragment != "" {
				return nil, errors.New("apply_patch arguments received after completion")
			}
			return out, nil
		}
		r.source += fragment
		delta, errPush := r.state.PushArguments(fragment)
		if errPush != nil {
			return nil, errPush
		}
		if r.hasSnapshot {
			var snapshot ApplyPatchInputDecoder
			_, _ = snapshot.Finish(r.snapshot)
			if !strings.HasPrefix(snapshot.Input(), r.state.Decoder.Input()) {
				return nil, errors.New("apply_patch stream conflicts with snapshot")
			}
		}
		if delta != "" {
			out = append(out, ApplyPatchInputDelta(&r.state, delta, b.next()))
		}
	case "response.function_call_arguments.done", "response.output_item.done":
		arguments := root.Get("arguments")
		if item.Exists() {
			arguments = item.Get("arguments")
		}
		if arguments.Exists() {
			if errSnapshot := b.snapshot(r, arguments, true); errSnapshot != nil {
				return nil, errSnapshot
			}
		}
		final := r.source
		if r.hasSnapshot {
			final = r.snapshot
		}
		tail, input, errFinish := r.state.FinishArguments(final)
		if errFinish != nil {
			return nil, errFinish
		}
		if !r.inputDone {
			if tail != "" && r.source != "" {
				out = append(out, ApplyPatchInputDelta(&r.state, tail, b.next()))
			}
			out = append(out, ApplyPatchInputDone(&r.state, input, b.next()))
			r.inputDone = true
		}
		if kind == "response.output_item.done" && !r.itemDone {
			r.completedItem = string(b.restoreItem([]byte(item.Raw), r, input, false))
			out = append(out, b.itemEvent(kind, []byte(r.completedItem), r))
			r.itemDone = true
		}
	}
	return out, nil
}

func (b *ApplyPatchResponsesBridge) transformItemEvent(raw []byte) ([][]byte, error) {
	root := util.ParseGJSONBytesNoCopy(raw)
	item := root.Get("item")
	if !item.Exists() && root.Get("name").Exists() {
		// Arguments events can supply late names and identities at the root.
		identityDoc := ojson.NewDoc([]byte(`{"type":"function_call"}`))
		for _, key := range []string{"name", "namespace", "call_id"} {
			if value := root.Get(key); value.Exists() {
				_ = identityDoc.Set(key, value.Value())
			}
		}
		item = util.ParseGJSONBytesNoCopy(identityDoc.Bytes())
	}
	r, errResolve := b.resolve(root, item)
	if errResolve != nil {
		return nil, errResolve
	}
	// A known name is provenance, not readiness. Retain the real source events
	// until both upstream IDs and the output index can identify every emitted event.
	if (!r.named && (r.kind == "" || r.kind == "function_call")) || (r.patch && !r.identityReady()) {
		if r.patch {
			arguments := root.Get("arguments")
			if root.Get("item").Exists() {
				arguments = item.Get("arguments")
			}
			kind := root.Get("type").String()
			if errSnapshot := b.snapshot(r, arguments, kind == "response.output_item.done" || kind == "response.function_call_arguments.done"); errSnapshot != nil {
				return nil, errSnapshot
			}
		}
		r.pending = append(r.pending, append([]byte(nil), raw...))
		return nil, nil
	}
	var out [][]byte
	pending := r.pending
	r.pending = nil
	if r.patch {
		for _, event := range append(pending, raw) {
			converted, errEvent := b.patchEvent(event, r)
			if errEvent != nil {
				return nil, errEvent
			}
			out = append(out, converted...)
		}
		return out, nil
	}
	out = append(out, pending...)
	if root.Get("item").Exists() && r.kind == "function_call" && b.tools[r.qualified].Namespace != "" {
		raw, _ = ojson.SetRawBytes(raw, "item", b.restoreItem([]byte(item.Raw), r, "", false))
	}
	if root.Get("type").String() == "response.output_item.done" && item.Exists() {
		r.itemDone = true
		r.completedItem = strings.Clone(ojson.GetBytesNoCopy(raw, "item").Raw)
	}
	return append(out, raw), nil
}

func (b *ApplyPatchResponsesBridge) envelope(raw []byte, stream bool) ([]byte, [][]byte, error) {
	root := util.ParseGJSONBytesNoCopy(raw)
	path := "output"
	response := root
	if root.Get("response").Exists() {
		path = "response.output"
		response = root.Get("response")
	}
	rawDoc := ojson.NewDoc(raw)
	modified := false
	var preceding [][]byte
	seen := map[*responsesPatchRecord]bool{}
	var items [][]byte
	for i, item := range response.Get("output").Array() {
		index := i
		// A terminal snapshot may omit earlier completed items: array position is not identity.
		known := b.byItemID[item.Get("id").String()]
		if known == nil {
			known = b.byCallID[item.Get("call_id").String()]
		}
		if known != nil && known.state.OutputIndex >= 0 {
			index = known.state.OutputIndex
		} else if known == nil && (item.Get("id").String() != "" || item.Get("call_id").String() != "") {
			if previous := b.byOutputIndex[index]; previous != nil && (previous.state.ItemID != "" || previous.state.CallID != "") {
				for _, r := range b.records {
					if r.state.OutputIndex >= index {
						index = r.state.OutputIndex + 1
					}
				}
			}
		}
		eventBytes := patchEnvelopeItem(index, item)
		event := util.ParseGJSONBytesNoCopy(eventBytes)
		r, errResolve := b.resolve(event, item)
		if errResolve != nil {
			return nil, nil, errResolve
		}
		seen[r] = true
		itemBytes := []byte(item.Raw)
		if r.patch {
			pending := r.pending
			r.pending = nil
			for _, source := range pending {
				events, errEvent := b.patchEvent(source, r)
				if errEvent != nil {
					return nil, nil, errEvent
				}
				preceding = append(preceding, events...)
			}
			events, errEvent := b.patchEvent(eventBytes, r)
			if errEvent != nil {
				return nil, nil, errEvent
			}
			preceding = append(preceding, events...)
			itemBytes = b.restoreItem(itemBytes, r, r.state.Decoder.Input(), false)
			_ = rawDoc.SetRawBytes(fmt.Sprintf("%s.%d", path, i), itemBytes)
			modified = true
		} else if item.Get("type").String() == "function_call" && b.tools[r.qualified].Namespace != "" {
			itemBytes = b.restoreItem(itemBytes, r, "", false)
			_ = rawDoc.SetRawBytes(fmt.Sprintf("%s.%d", path, i), itemBytes)
			modified = true
		}
		items = append(items, itemBytes)
	}
	for _, r := range b.records {
		if seen[r] || (!r.patch && !b.converted) {
			continue
		}
		if r.inputDone && !r.itemDone {
			item := []byte(`{"type":"function_call","status":"completed"}`)
			r.completedItem = string(b.restoreItem(item, r, r.state.Decoder.Input(), false))
			preceding = append(preceding, b.itemEvent("response.output_item.done", []byte(r.completedItem), r))
			r.itemDone = true
		}
		if r.itemDone {
			index := r.state.OutputIndex
			if index < 0 || index > len(items) {
				index = len(items)
			}
			items = append(items, nil)
			copy(items[index+1:], items[index:])
			items[index] = []byte(r.completedItem)
		}
	}
	if len(items) != len(response.Get("output").Array()) {
		_ = rawDoc.SetRawBytes(path, JoinRawArray(items))
		modified = true
	}
	if stream {
		if errFinish := b.Finish(); errFinish != nil {
			return nil, nil, errFinish
		}
		for _, r := range b.records {
			preceding = append(preceding, r.pending...)
			r.pending = nil
		}
		if b.converted {
			_ = rawDoc.Set("sequence_number", b.next())
			modified = true
		}
	}
	if modified {
		raw = rawDoc.Bytes()
	}
	return raw, preceding, nil
}

func patchEnvelopeItem(index int, item ojson.Result) []byte {
	outDoc := ojson.NewDoc([]byte(`{"type":"response.output_item.done","output_index":0,"item":{}}`))
	_ = outDoc.Set("output_index", index)
	_ = outDoc.SetRaw("item", item.Raw)
	return outDoc.Bytes()
}

// Transform converts one payload. A failure is terminal and is emitted only once.
func (b *ApplyPatchResponsesBridge) Transform(event []byte) ([][]byte, error) {
	if b.failed || b.terminal {
		return nil, nil
	}
	if !b.active {
		return [][]byte{event}, nil
	}
	root := util.ParseGJSONBytesNoCopy(event)
	if id := root.Get("response.id").String(); id != "" {
		b.responseID = strings.Clone(id)
	}
	if seq := int(root.Get("sequence_number").Int()); seq > b.sequence {
		b.sequence = seq
	}
	kind := root.Get("type").String()
	var out [][]byte
	var errTransform error
	switch kind {
	case "response.output_item.added", "response.output_item.done", "response.function_call_arguments.delta", "response.function_call_arguments.done":
		out, errTransform = b.transformItemEvent(event)
	case "response.completed", "response.incomplete", "response.done":
		var final []byte
		final, out, errTransform = b.envelope(event, true)
		if errTransform == nil {
			out = append(out, final)
			b.terminal = true
		}
	case "response.failed":
		b.terminal = true
		out = [][]byte{event}
	default:
		out = [][]byte{event}
	}
	if errTransform != nil {
		return b.failure(errTransform)
	}
	for i, event := range out {
		seq := int(ojson.GetBytesNoCopy(event, "sequence_number").Int())
		// Native custom payloads are opaque. Only a stream that acquired a function
		// patch call needs resequencing of its other compatibility events.
		nativeCustom := strings.HasPrefix(kind, "response.custom_tool_call_input.") || root.Get("item.type").String() == "custom_tool_call"
		if b.converted && !nativeCustom && seq <= b.lastSequence {
			seq = b.next()
			out[i], _ = ojson.SetBytes(event, "sequence_number", seq)
		}
		if seq > b.lastSequence {
			b.lastSequence = seq
		}
	}
	return out, nil
}

// TransformNonStream accepts either a bare response or a terminal event envelope.
func (b *ApplyPatchResponsesBridge) TransformNonStream(response []byte) ([]byte, error) {
	if !b.active {
		return response, nil
	}
	out, _, errEnvelope := b.envelope(response, false)
	if errEnvelope != nil {
		b.failed = true
		b.SetToolInputError(errEnvelope)
		return nil, errEnvelope
	}
	return out, nil
}

// Finish rejects acquired calls whose final arguments have not been validated.
func (b *ApplyPatchResponsesBridge) Finish() error {
	if b.ToolInputError() != nil {
		return b.ToolInputError()
	}
	if b.terminal {
		return nil
	}
	for _, r := range b.records {
		if r.patch && !r.inputDone {
			return errors.New("incomplete apply_patch tool arguments received from upstream")
		}
	}
	return nil
}
