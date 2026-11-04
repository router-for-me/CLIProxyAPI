package helps

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// NormalizeApplyPatchResponsesRequest opts a non-Codex executor into the patch contract.
func NormalizeApplyPatchResponsesRequest(body []byte, original ...[]byte) ([]byte, error) {
	if !ojson.ValidBytes(body) {
		return body, errors.New("invalid Responses request JSON")
	}
	doc := ojson.NewDoc(body)
	if errNorm := NormalizeApplyPatchResponsesRequestDoc(doc, original...); errNorm != nil {
		return body, errNorm
	}
	return doc.Bytes(), nil
}

// NormalizeApplyPatchResponsesRequestDoc opts a non-Codex executor into the patch contract in place on doc.
func NormalizeApplyPatchResponsesRequestDoc(doc *ojson.Doc, original ...[]byte) error {
	if doc == nil || doc.Len() == 0 {
		return errors.New("invalid Responses request JSON")
	}
	if len(original) > 0 {
		preferChatFunctionPatchToolsDoc(original[0], doc)
	}
	descriptors := util.CollectResponsesToolDescriptorsDoc(doc)
	winners := util.CollectResponsesToolWinnersDoc(doc)
	affected := map[string]bool{}
	for _, d := range descriptors {
		if applypatch.IsCustomTool(d.Tool) {
			affected[d.Name] = true
		}
	}
	shouldDropTool := func(tool ojson.Result, namespace string) bool {
		if tool.Get("type").String() == "namespace" {
			return false
		}
		name := tool.Get("name").String()
		if name == "" {
			name = tool.Get("function.name").String()
		}
		qualified := util.QualifyResponsesNamespaceToolName(namespace, name)
		winner, ok := winners[qualified]
		return ok && affected[qualified] && winner.Tool.Index != tool.Index
	}
	var normalizeToolsDoc func(string, ojson.Result, string)
	normalizeToolsDoc = func(toolsPath string, tools ojson.Result, namespace string) {
		arr := tools.Array()
		dropAny := false
		for _, tool := range arr {
			if shouldDropTool(tool, namespace) {
				dropAny = true
				break
			}
		}
		survivors := arr
		if dropAny {
			keptRaw := make([][]byte, 0, len(arr))
			survivors = make([]ojson.Result, 0, len(arr))
			for _, tool := range arr {
				if shouldDropTool(tool, namespace) {
					continue
				}
				keptRaw = append(keptRaw, []byte(tool.Raw))
				survivors = append(survivors, tool)
			}
			_ = doc.SetRawBytes(toolsPath, translatorcommon.JoinRawArray(keptRaw))
		}
		for idx, tool := range survivors {
			toolPath := fmt.Sprintf("%s.%d", toolsPath, idx)
			if tool.Get("type").String() == "namespace" {
				for _, key := range []string{"tools", "children"} {
					if children := tool.Get(key); children.IsArray() {
						normalizeToolsDoc(toolPath+"."+key, children, tool.Get("name").String())
						break
					}
				}
				continue
			}
			name := tool.Get("name").String()
			if name == "" {
				name = tool.Get("function.name").String()
			}
			qualified := util.QualifyResponsesNamespaceToolName(namespace, name)
			if winner, ok := winners[qualified]; ok && affected[qualified] && winner.Tool.Index == tool.Index && applypatch.IsCustomTool(tool) {
				_ = doc.Set(toolPath+".type", "function")
				_ = doc.Set(toolPath+".description", applypatch.Description(tool))
				_ = doc.SetRawBytes(toolPath+".parameters", applypatch.Parameters())
				_ = util.DeleteJSONDocIfExists(doc, toolPath+".format")
			}
		}
	}
	inputItems := doc.Get("input").Array()
	patchHistory := map[string]bool{}
	for _, item := range inputItems {
		if item.Get("type").String() == "custom_tool_call" && strings.TrimSpace(item.Get("name").String()) == "apply_patch" {
			if item.Get("input").Type != ojson.String {
				return errors.New("apply_patch history input must be a string")
			}
			patchHistory[item.Get("call_id").String()] = true
		}
	}
	if tools := doc.Get("tools"); tools.IsArray() {
		normalizeToolsDoc("tools", tools, "")
	}
	for i, item := range inputItems {
		path := fmt.Sprintf("input.%d", i)
		switch item.Get("type").String() {
		case "additional_tools":
			if tools := item.Get("tools"); tools.IsArray() {
				normalizeToolsDoc(path+".tools", tools, "")
			}
		case "custom_tool_call":
			if strings.TrimSpace(item.Get("name").String()) != "apply_patch" {
				continue
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
	var normalizeChoiceDoc func(string, ojson.Result)
	normalizeChoiceDoc = func(choicePath string, choice ojson.Result) {
		name := choice.Get("name").String()
		namespace := choice.Get("namespace").String()
		if d, ok := winners[util.QualifyResponsesNamespaceToolName(namespace, name)]; ok && applypatch.IsCustomTool(d.Tool) && choice.Get("type").String() == "custom" {
			_ = doc.Set(choicePath+".type", "function")
		}
		for i, child := range choice.Get("tools").Array() {
			normalizeChoiceDoc(fmt.Sprintf("%s.tools.%d", choicePath, i), child)
		}
	}
	if choice := doc.Get("tool_choice"); choice.IsObject() {
		normalizeChoiceDoc("tool_choice", choice)
	}
	return nil
}

// ApplyPatchResponsesState is owned explicitly by a non-native executor, never inferred
// from the wire format or a configured function schema. Supporting state stays request-local.
type ApplyPatchResponsesState struct {
	Bridge                 *translatorcommon.ApplyPatchResponsesBridge
	tools                  map[string]util.ResponsesToolDescriptor
	dispatchers            map[string]string
	byDispatcherKey        map[string]*patchDispatcherCall
	records                []*patchDispatcherCall
	upstream               []byte
	eventLine              []byte
	active, failed, closed bool
	transportDone          bool
}

type patchDispatcherCall struct {
	namespace string
	events    [][]byte
	snapshots [][]byte
	source    string
	originals [][]byte
	completed bool
	ordinary  bool
	index     int
	name      string
	arguments string
}

// preferChatFunctionPatchTools preserves the Chat request converter's existing winner rule.
func preferChatFunctionPatchTools(original, declarations []byte) []byte {
	if len(declarations) == 0 {
		return declarations
	}
	doc := ojson.NewDoc(declarations)
	if !preferChatFunctionPatchToolsDoc(original, doc) {
		return declarations
	}
	return doc.Bytes()
}

func preferChatFunctionPatchToolsDoc(original []byte, doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	ordinary := map[string]bool{}
	for _, tool := range ojson.GetBytesNoCopy(original, "tools").Array() {
		if tool.Get("type").String() == "function" {
			ordinary[tool.Get("function.name").String()] = true
		}
	}
	if len(ordinary) == 0 {
		return false
	}
	declTools := doc.Get("tools").Array()
	available := map[string]bool{}
	for _, tool := range declTools {
		if tool.Get("type").String() == "function" {
			available[tool.Get("name").String()] = true
		}
	}
	var tools [][]byte
	removed := false
	for _, tool := range declTools {
		name := tool.Get("name").String()
		if applypatch.IsCustomTool(tool) && ordinary[name] && available[name] {
			removed = true
			continue
		}
		tools = append(tools, []byte(tool.Raw))
	}
	if !removed {
		return false
	}
	return doc.SetRawBytes("tools", translatorcommon.JoinRawArray(tools)) == nil
}

func NewApplyPatchResponsesState(source sdktranslator.Format, original, declarations []byte) *ApplyPatchResponsesState {
	if source == sdktranslator.FormatOpenAI {
		declarations = preferChatFunctionPatchTools(original, declarations)
	}
	s := &ApplyPatchResponsesState{Bridge: translatorcommon.NewApplyPatchResponsesBridge(declarations), tools: util.CollectResponsesToolWinners(util.ParseGJSONBytesNoCopy(declarations)), dispatchers: map[string]string{}, byDispatcherKey: map[string]*patchDispatcherCall{}}
	for _, d := range s.tools {
		if applypatch.IsCustomTool(d.Tool) {
			s.active = true
		}
	}
	return s
}

// Active reports whether this explicitly non-native state owns a winning patch declaration.
func (s *ApplyPatchResponsesState) Active() bool { return s.active }

// AddDispatcher marks only xAI folded namespaces that contain a winning custom patch.
func (s *ApplyPatchResponsesState) AddDispatcher(name, namespace string) {
	for _, d := range s.tools {
		if d.Namespace == namespace && applypatch.IsCustomTool(d.Tool) {
			s.dispatchers[name] = namespace
			return
		}
	}
}

func dispatcherKeys(root ojson.Result) []string {
	var keys []string
	for _, path := range []string{"item.id", "item_id"} {
		if id := root.Get(path).String(); id != "" {
			keys = append(keys, "item:"+id)
		}
	}
	for _, path := range []string{"item.call_id", "call_id"} {
		if id := root.Get(path).String(); id != "" {
			keys = append(keys, "call:"+id)
		}
	}
	if index := root.Get("output_index"); index.Exists() {
		keys = append(keys, fmt.Sprintf("index:%d", index.Int()))
	}
	return keys
}
func (s *ApplyPatchResponsesState) dispatcher(root ojson.Result) *patchDispatcherCall {
	matched := map[*patchDispatcherCall]bool{}
	for _, key := range dispatcherKeys(root) {
		if call := s.byDispatcherKey[key]; call != nil {
			matched[call] = true
		}
	}
	for _, call := range s.records {
		if matched[call] {
			return call
		}
	}
	return nil
}

func (s *ApplyPatchResponsesState) newDispatcherCandidate(root ojson.Result) *patchDispatcherCall {
	call := &patchDispatcherCall{index: -1}
	s.records = append(s.records, call)
	for _, key := range dispatcherKeys(root) {
		if s.byDispatcherKey[key] == nil {
			s.byDispatcherKey[key] = call
		}
	}
	return call
}

// RememberDispatcherEvent retains upstream evidence before namespace restoration.
// A wrapper alone never proves dispatcher provenance: only a declared dispatcher name does.
func (s *ApplyPatchResponsesState) RememberDispatcherEvent(event []byte) {
	if s.failed || s.closed || s.transportDone || len(s.dispatchers) == 0 {
		return
	}
	s.upstream = bytes.Clone(event)
	s.RememberDispatcherArguments(event)
}

// RememberDispatcherArguments keeps full snapshots on every matched candidate, including
// unnamed calls. The common bridge retains all identity/type contradictions until acquisition.
func (s *ApplyPatchResponsesState) RememberDispatcherArguments(event []byte) {
	if s.failed || s.closed || s.transportDone || len(s.dispatchers) == 0 || ojson.GetBytesNoCopy(event, "type").String() != "response.function_call_arguments.done" {
		return
	}
	s.upstream = bytes.Clone(event)
	root := util.ParseGJSONBytesNoCopy(event)
	if s.dispatcher(root) == nil {
		s.newDispatcherCandidate(root)
	}
	seen := map[*patchDispatcherCall]bool{}
	for _, key := range dispatcherKeys(root) {
		if call := s.byDispatcherKey[key]; call != nil && !seen[call] {
			call.snapshots = append(call.snapshots, bytes.Clone(event))
			seen[call] = true
		}
	}
}

func patchDispatcherArguments(wrapper ojson.Result) string {
	arguments := wrapper.Get("arguments")
	if arguments.Type == ojson.String {
		return arguments.String()
	}
	return arguments.Raw
}

func dispatcherEventName(root ojson.Result) string {
	if root.Get("item").Exists() {
		return root.Get("item.name").String()
	}
	return root.Get("name").String()
}

func (s *ApplyPatchResponsesState) expandDispatcher(event, original []byte) ([][]byte, error) {
	root := util.ParseGJSONBytesNoCopy(event)
	raw := util.ParseGJSONBytesNoCopy(original)
	kind := root.Get("type").String()
	call := s.dispatcher(root)
	name := dispatcherEventName(raw)
	namespace, declared := s.dispatchers[name]
	if call == nil && len(s.dispatchers) > 0 {
		added := kind == "response.output_item.added" && raw.Get("item.type").String() == "function_call"
		if declared && raw.Get("item.type").String() == "function_call" || added || kind == "response.function_call_arguments.delta" || kind == "response.function_call_arguments.done" {
			call = s.newDispatcherCandidate(root)
		}
	}
	if call == nil {
		return [][]byte{event}, nil
	}
	if errIdentity := s.Bridge.CheckIdentity(event); errIdentity != nil {
		return nil, errIdentity
	}
	for _, key := range dispatcherKeys(root) {
		if s.byDispatcherKey[key] == nil {
			s.byDispatcherKey[key] = call
		}
	}
	if call.index < 0 && root.Get("output_index").Exists() {
		call.index = int(root.Get("output_index").Int())
	}
	if call.ordinary && !declared {
		return [][]byte{event}, nil
	}
	call.events = append(call.events, bytes.Clone(event))
	call.originals = append(call.originals, bytes.Clone(original))
	if declared {
		if call.namespace != "" && call.namespace != namespace {
			return nil, errors.New("conflicting apply_patch dispatcher namespace")
		}
		call.namespace = namespace
		call.ordinary = false
	}
	if kind == "response.function_call_arguments.delta" {
		if call.completed && root.Get("delta").String() != "" {
			child := s.tools[util.QualifyResponsesNamespaceToolName(call.namespace, call.name)]
			if applypatch.IsCustomTool(child.Tool) {
				return nil, errors.New("apply_patch dispatcher arguments received after completion")
			}
			return [][]byte{event}, nil
		}
		call.source += root.Get("delta").String()
	}
	if call.namespace == "" {
		if name != "" {
			// A late ordinary name releases untouched arguments, even if they look like a wrapper.
			call.ordinary = true
			events := call.events
			call.events, call.originals = nil, nil
			return events, nil
		}
		return nil, nil
	}
	if kind == "response.function_call_arguments.delta" && !call.completed {
		return nil, nil
	}
	if kind != "response.output_item.done" && !(call.completed && (kind == "response.function_call_arguments.done" || kind == "response.function_call_arguments.delta" || kind == "response.output_item.added")) {
		return nil, nil
	}
	path := "item."
	if kind == "response.function_call_arguments.done" || kind == "response.function_call_arguments.delta" {
		path = ""
	}

	var wrappers []string
	if call.source != "" {
		wrappers = append(wrappers, call.source)
	}
	for _, snapshot := range call.snapshots {
		arguments := ojson.GetBytesNoCopy(snapshot, "arguments").String()
		if ojson.Get(arguments, "name").Exists() {
			wrappers = append(wrappers, arguments)
		}
	}
	for _, pending := range call.originals {
		p := util.ParseGJSONBytesNoCopy(pending)
		arguments := p.Get("item.arguments").String()
		if (dispatcherEventName(p) == "" || s.dispatchers[dispatcherEventName(p)] == call.namespace) && ojson.Get(arguments, "name").Exists() {
			wrappers = append(wrappers, arguments)
		}
	}
	// For callers without a pre-restoration copy, retain the original full-source contract.
	if len(wrappers) == 0 {
		for _, pending := range call.events {
			if args := ojson.GetBytesNoCopy(pending, "arguments"); ojson.Get(args.String(), "name").String() != "" {
				wrappers = append(wrappers, args.String())
			}
		}
	}
	source := ojson.Result{}
	for _, wrapper := range wrappers {
		if ojson.Valid(wrapper) && ojson.Get(wrapper, "name").String() != "" {
			source = ojson.Parse(wrapper)
		}
	}
	eventDoc := ojson.NewDoc(event)
	name = root.Get(path + "name").String()
	if name == "" || s.dispatchers[name] == call.namespace {
		name = source.Get("name").String()
		if name == "" {
			name = call.name
		}
		_ = eventDoc.Set(path+"name", name)
		_ = eventDoc.Set(path+"namespace", call.namespace)
	}
	if root.Get(path+"namespace").String() == "" {
		_ = eventDoc.Set(path+"namespace", call.namespace)
	}
	if declared || dispatcherEventName(raw) == "" || kind == "response.function_call_arguments.done" {
		if wrapper := ojson.Parse(raw.Get(path + "arguments").String()); wrapper.Get("name").String() != "" {
			_ = eventDoc.Set(path+"arguments", patchDispatcherArguments(wrapper))
		}
	}
	if !root.Get(path + "arguments").Exists() {
		encoded := patchDispatcherArguments(source)
		if encoded == "" {
			encoded = call.arguments
		}
		if encoded != "" {
			_ = eventDoc.Set(path+"arguments", encoded)
		}
	}
	event = eventDoc.Bytes()
	root = util.ParseGJSONBytesNoCopy(event)
	call.events[len(call.events)-1] = event
	d := s.tools[util.QualifyResponsesNamespaceToolName(call.namespace, name)]
	patch := applypatch.IsCustomTool(d.Tool)
	finalArguments := root.Get(path + "arguments").String()
	if kind == "response.output_item.added" && finalArguments == "" {
		finalArguments = call.arguments
	}
	if patch {
		for _, snapshot := range call.snapshots {
			if ojson.GetBytesNoCopy(snapshot, "arguments").Type != ojson.String {
				return nil, errors.New("apply_patch dispatcher arguments snapshot must be a string")
			}
		}
	}
	for _, wrapperRaw := range wrappers {
		wrapper := ojson.Parse(wrapperRaw)
		child := s.tools[util.QualifyResponsesNamespaceToolName(call.namespace, wrapper.Get("name").String())]
		if !patch && !applypatch.IsCustomTool(child.Tool) {
			continue
		}
		input, errUnwrap := applypatch.UnwrapInput(patchDispatcherArguments(wrapper))
		final, errFinal := applypatch.UnwrapInput(finalArguments)
		if !ojson.Valid(wrapperRaw) || wrapper.Get("name").String() != name || errUnwrap != nil || errFinal != nil || input != final {
			return nil, errors.New("conflicting apply_patch dispatcher arguments")
		}
	}

	// Retain completed aliases and source evidence until the response actually closes.
	// Repeated snapshots validate only the new event, never replay completed progress.
	start := 0
	if call.completed {
		start = len(call.events) - 1
	}
	var out [][]byte
	for i := start; i < len(call.events); i++ {
		pending := call.events[i]
		p := util.ParseGJSONBytesNoCopy(pending)
		originalRoot := util.ParseGJSONBytesNoCopy(call.originals[i])
		if patch {
			for _, namespacePath := range []string{"namespace", "item.namespace"} {
				if supplied := originalRoot.Get(namespacePath).String(); supplied != "" && supplied != call.namespace {
					return nil, errors.New("conflicting apply_patch dispatcher namespace")
				}
			}
			for _, suppliedName := range []string{dispatcherEventName(originalRoot), dispatcherEventName(p)} {
				if suppliedName != "" && s.dispatchers[suppliedName] != call.namespace && util.QualifyResponsesNamespaceToolName(call.namespace, suppliedName) != d.Name {
					return nil, errors.New("conflicting apply_patch dispatcher child")
				}
			}
		}
		pendingPath := ""
		switch p.Get("type").String() {
		case "response.function_call_arguments.delta":
			if patch {
				// A dispatcher envelope is not incremental child input.
				continue
			}
		case "response.output_item.added", "response.output_item.done":
			pendingPath = "item."
		case "response.function_call_arguments.done":
			pendingPath = ""
		default:
			out = append(out, pending)
			continue
		}
		if p.Get("type").String() != "response.function_call_arguments.delta" {
			pendingDoc := ojson.NewDoc(pending)
			pendingName := p.Get(pendingPath + "name").String()
			if pendingName == "" || s.dispatchers[pendingName] == call.namespace {
				_ = pendingDoc.Set(pendingPath+"name", name)
				_ = pendingDoc.Set(pendingPath+"namespace", call.namespace)
			}
			if p.Get(pendingPath+"namespace").String() == "" {
				_ = pendingDoc.Set(pendingPath+"namespace", call.namespace)
			}
			// Only actual upstream wrappers are unwrapped, not restored child contents.
			args := originalRoot.Get(pendingPath + "arguments")
			if patch && args.Exists() && args.Type != ojson.String {
				return nil, errors.New("apply_patch dispatcher arguments snapshot must be a string")
			}
			if args.String() != "" && (dispatcherEventName(originalRoot) == "" || s.dispatchers[dispatcherEventName(originalRoot)] == call.namespace || pendingPath == "") {
				wrapper := ojson.Parse(args.String())
				if wrapper.Get("name").String() != "" {
					if patch && wrapper.Get("name").String() != name {
						return nil, errors.New("conflicting apply_patch dispatcher snapshot")
					}
					_ = pendingDoc.Set(pendingPath+"arguments", patchDispatcherArguments(wrapper))
				}
			}
			pending = pendingDoc.Bytes()
		}
		out = append(out, pending)
	}
	call.completed = true
	call.name, call.arguments = name, finalArguments
	return out, nil
}

func (s *ApplyPatchResponsesState) unfinishedDispatcher() bool {
	for _, call := range s.records {
		if call.namespace != "" && !call.completed {
			return true
		}
	}
	return false
}

func (s *ApplyPatchResponsesState) fail(err error) ([][]byte, error) {
	if s.failed {
		return nil, err
	}
	s.failed = true
	s.byDispatcherKey, s.records, s.upstream = nil, nil, nil
	return s.Bridge.Fail(err)
}

func (s *ApplyPatchResponsesState) Transform(event []byte) ([][]byte, error) {
	if s.failed || s.transportDone {
		return nil, nil
	}
	if s.active && bytes.Equal(bytes.TrimSpace(event), []byte("[DONE]")) {
		if errFinish := s.Finish(); errFinish != nil {
			return s.fail(errFinish)
		}
		s.transportDone = true
		return [][]byte{event}, nil
	}
	if s.closed {
		return nil, nil
	}
	original := s.upstream
	s.upstream = nil
	if original == nil {
		original = event
	}
	var preceding [][]byte
	root := util.ParseGJSONBytesNoCopy(event)
	kind := root.Get("type").String()
	if kind == "response.completed" || kind == "response.incomplete" || kind == "response.done" {
		originalItems := ojson.GetBytesNoCopy(original, "response.output").Array()
		var eventDoc *ojson.Doc
		for i, item := range root.Get("response.output").Array() {
			doneDoc := ojson.NewDocString(`{"type":"response.output_item.done"}`)
			_ = doneDoc.SetRaw("item", item.Raw)
			done := doneDoc.Bytes()
			// Explicit IDs take priority over array position in a sparse terminal snapshot.
			call := s.dispatcher(util.ParseGJSONBytesNoCopy(done))
			if call == nil && item.Get("id").String() == "" && item.Get("call_id").String() == "" {
				_ = doneDoc.Set("output_index", i)
				done = doneDoc.Bytes()
				call = s.dispatcher(util.ParseGJSONBytesNoCopy(done))
			}
			if call != nil && !call.ordinary {
				if call.index >= 0 {
					_ = doneDoc.Set("output_index", call.index)
				} else {
					_ = doneDoc.Set("output_index", i)
				}
				done = doneDoc.Bytes()
				originalDone := done
				// Filtering may shift array positions, but restoration preserves both IDs.
				matches := func(candidate ojson.Result) bool {
					return candidate.Get("id").String() == item.Get("id").String() && candidate.Get("call_id").String() == item.Get("call_id").String()
				}
				if i < len(originalItems) && matches(originalItems[i]) {
					origDoc := ojson.NewDoc(done)
					_ = origDoc.SetRaw("item", originalItems[i].Raw)
					originalDone = origDoc.Bytes()
				} else if item.Get("id").String() != "" || item.Get("call_id").String() != "" {
					for _, originalItem := range originalItems {
						if matches(originalItem) {
							origDoc := ojson.NewDoc(done)
							_ = origDoc.SetRaw("item", originalItem.Raw)
							originalDone = origDoc.Bytes()
							break
						}
					}
				}
				events, errExpand := s.expandDispatcher(done, originalDone)
				if errExpand != nil {
					return s.fail(errExpand)
				}
				preceding = append(preceding, events...)
				if len(events) > 0 {
					if eventDoc == nil {
						eventDoc = ojson.NewDoc(event)
					}
					_ = eventDoc.SetRaw(fmt.Sprintf("response.output.%d", i), ojson.GetBytesNoCopy(events[len(events)-1], "item").Raw)
				}
			}
		}
		if eventDoc != nil {
			event = eventDoc.Bytes()
		}
		if s.unfinishedDispatcher() {
			return s.fail(errors.New("incomplete apply_patch namespace dispatcher received from upstream"))
		}
		// Unproven candidates remain ordinary; let common resolve or flush their evidence.
		for _, call := range s.records {
			if call.namespace == "" && !call.ordinary {
				preceding = append(preceding, call.events...)
				call.events = nil
			}
		}
	}
	events, errExpand := s.expandDispatcher(event, original)
	events = append(preceding, events...)
	if errExpand != nil {
		return s.fail(errExpand)
	}
	var out [][]byte
	for _, e := range events {
		converted, errTransform := s.Bridge.Transform(e)
		out = append(out, converted...)
		if errTransform != nil {
			s.failed = true
			s.byDispatcherKey, s.records, s.upstream = nil, nil, nil
			return out, errTransform
		}
	}
	if kind == "response.completed" || kind == "response.incomplete" || kind == "response.done" || kind == "response.failed" {
		s.closed = true
		s.byDispatcherKey, s.records, s.upstream = nil, nil, nil
	}
	return out, nil
}

// Finish validates source response closure independently of completed tool input.
// Common Finish remains argument-only because it also runs inside terminal conversion.
func (s *ApplyPatchResponsesState) Finish() error {
	if errFinish := s.Bridge.Finish(); errFinish != nil {
		return errFinish
	}
	if s.closed || !s.active {
		return nil
	}
	if s.unfinishedDispatcher() {
		return errors.New("incomplete apply_patch namespace dispatcher received from upstream")
	}
	return errors.New("incomplete apply_patch source response received from upstream")
}

// Stream preserves SSE framing and updates event-name lines to match converted JSON.
// A premature [DONE] is checked before publishing any success marker.
func (s *ApplyPatchResponsesState) Stream(line []byte) ([][]byte, error) {
	if !s.active {
		return [][]byte{line}, nil
	}
	if s.failed || s.transportDone {
		return nil, nil
	}
	if bytes.HasPrefix(line, []byte("event:")) {
		s.eventLine = bytes.Clone(line)
		return nil, nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return [][]byte{line}, nil
	}
	payload := bytes.TrimSpace(line[5:])
	var events [][]byte
	var errTransform error
	if bytes.Equal(payload, []byte("[DONE]")) {
		if errFinish := s.Finish(); errFinish != nil {
			events, errTransform = s.fail(errFinish)
		} else {
			// JSON completion and transport completion are separate boundaries.
			s.transportDone = true
			s.byDispatcherKey, s.records, s.upstream = nil, nil, nil
			s.eventLine = nil
			return [][]byte{line}, nil
		}
	} else {
		events, errTransform = s.Transform(payload)
	}
	if len(events) == 1 && bytes.Equal(events[0], payload) && errTransform == nil {
		var out [][]byte
		if s.eventLine != nil {
			out = append(out, s.eventLine)
			s.eventLine = nil
		}
		return append(out, line), nil
	}
	var out [][]byte
	for _, event := range events {
		if s.eventLine != nil {
			if bytes.Equal(event, payload) {
				out = append(out, s.eventLine)
			} else {
				out = append(out, []byte("event: "+ojson.GetBytesNoCopy(event, "type").String()))
			}
		}
		data := append([]byte("data: "), event...)
		// Each expanded event must be a complete SSE frame, even if the source had only one.
		data = append(data, '\n', '\n')
		out = append(out, data)
	}
	s.eventLine = nil
	return out, errTransform
}

// FinishStream emits the local failure once on EOF without a validated completion.
func (s *ApplyPatchResponsesState) FinishStream() ([][]byte, error) {
	if s.failed || s.transportDone {
		return nil, nil
	}
	if errFinish := s.Finish(); errFinish != nil {
		events, errFailure := s.fail(errFinish)
		for i := range events {
			events[i] = append(append([]byte("data: "), events[i]...), '\n', '\n')
		}
		return events, errFailure
	}
	return nil, nil
}
