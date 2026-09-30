package responses

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Tests for issue #6209: distinct Responses tool identities must map to unique,
// valid Claude tool names, and every name must map back to its identity.

const (
	longToolNamespace = "mcp__example_apps__acme_inventory_service"
	longPricesChild   = "acme_inventory_service_get_item_prices"
	longMetricsChild  = "acme_inventory_service_get_item_metrics"
)

var validClaudeToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func collidingNamespaceTools() string {
	return fmt.Sprintf(`{"type":"namespace","name":%q,"tools":[
		{"type":"function","name":%q,"parameters":{"type":"object","properties":{}}},
		{"type":"function","name":%q,"parameters":{"type":"object","properties":{}}}
	]}`, longToolNamespace, longPricesChild, longMetricsChild)
}

// claudeToolNamesOf translates a Responses request and returns the Claude tool
// names in declaration order. It fails on duplicate or invalid names.
func claudeToolNamesOf(t *testing.T, request string) ([]string, gjson.Result) {
	t.Helper()
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", []byte(request), false)
	root := gjson.ParseBytes(out)
	var names []string
	seen := map[string]bool{}
	root.Get("tools").ForEach(func(_, tool gjson.Result) bool {
		name := tool.Get("name").String()
		if seen[name] {
			t.Fatalf("duplicate Claude tool name %q in %s", name, root.Get("tools").Raw)
		}
		if !validClaudeToolName.MatchString(name) {
			t.Fatalf("invalid Claude tool name %q", name)
		}
		seen[name] = true
		names = append(names, name)
		return true
	})
	return names, root
}

type restoredToolCall struct {
	itemType  string
	name      string
	namespace string
}

func claudeToolUseStream(name, id, partialJSON string) [][]byte {
	return [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_names","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, id, name)),
		[]byte(fmt.Sprintf(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, partialJSON)),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
}

// restoreNonStream feeds one Claude tool_use through the non-stream response
// translator and returns the restored Responses item.
func restoreNonStream(t *testing.T, request, claudeName string) (restoredToolCall, gjson.Result) {
	t.Helper()
	var lines []string
	for _, chunk := range claudeToolUseStream(claudeName, "toolu_ns", `{"input":"x"}`) {
		lines = append(lines, string(chunk))
	}
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", []byte(request), nil, []byte(strings.Join(lines, "\n")), nil)
	item := gjson.ParseBytes(out).Get("output.0")
	return restoredToolCall{itemType: item.Get("type").String(), name: item.Get("name").String(), namespace: item.Get("namespace").String()}, item
}

func TestClaudeToolNames_CollidingNamespaceChildrenGetUniqueNames(t *testing.T) {
	request := fmt.Sprintf(`{"model":"gpt-test","input":"hi","tools":[%s]}`, collidingNamespaceTools())
	names, _ := claudeToolNamesOf(t, request)
	if len(names) != 2 {
		t.Fatalf("tool count = %d, want 2: %v", len(names), names)
	}
}

func TestClaudeToolNames_CollidingFlatToolsGetUniqueNames(t *testing.T) {
	prefix := "flat_tool_" + strings.Repeat("x", 60)
	request := fmt.Sprintf(`{"model":"gpt-test","input":"hi","tools":[
		{"type":"function","name":%q},{"type":"function","name":%q}]}`, prefix+"_alpha", prefix+"_beta")
	names, _ := claudeToolNamesOf(t, request)
	if len(names) != 2 {
		t.Fatalf("tool count = %d, want 2: %v", len(names), names)
	}
	for _, want := range []string{prefix + "_alpha", prefix + "_beta"} {
		found := false
		for _, name := range names {
			if got, _ := restoreNonStream(t, request, name); got.name == want && got.namespace == "" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no emitted name restores to %q (names %v)", want, names)
		}
	}
}

func TestClaudeToolNames_SanitizeCollisionKeepsValidName(t *testing.T) {
	request := `{"model":"gpt-test","input":"hi","tools":[{"type":"function","name":"a.b"},{"type":"function","name":"a_b"}]}`
	names, _ := claudeToolNamesOf(t, request)
	if len(names) != 2 || names[1] != "a_b" {
		t.Fatalf("names = %v, want a_b kept for the valid tool", names)
	}
	if got, _ := restoreNonStream(t, request, names[0]); got.name != "a.b" {
		t.Fatalf("names[0] restores to %q, want a.b", got.name)
	}
	if got, _ := restoreNonStream(t, request, "a_b"); got.name != "a_b" {
		t.Fatalf("a_b restores to %q, want a_b", got.name)
	}
}

func TestClaudeToolNames_UnchangedForShortAndLoneLongNames(t *testing.T) {
	long := "single_tool_" + strings.Repeat("y", 70)
	request := fmt.Sprintf(`{"model":"gpt-test","input":"hi","tools":[
		{"type":"function","name":"get_weather"},{"type":"custom","name":"lookup"},{"type":"function","name":"x:y"},{"type":"function","name":%q}]}`, long)
	names, _ := claudeToolNamesOf(t, request)
	want := []string{"get_weather", "lookup", "x_y", long[:64]}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if got, _ := restoreNonStream(t, request, long[:64]); got.name != long {
		t.Fatalf("lone long name restores to %q, want %q", got.name, long)
	}
	if got, _ := restoreNonStream(t, request, "x_y"); got.name != "x:y" {
		t.Fatalf("x_y restores to %q, want x:y", got.name)
	}
}

// A generated name must not take a name that another tool already uses, and
// the result must not depend on declaration order.
func TestClaudeToolNames_GeneratedNamesAvoidExistingNamesInAnyOrder(t *testing.T) {
	sum := sha256.Sum256([]byte("p.q"))
	hashCandidate := "p_q_" + hex.EncodeToString(sum[:])[:10]
	tools := []string{
		`{"type":"function","name":"p.q"}`,
		`{"type":"function","name":"p:q"}`,
		fmt.Sprintf(`{"type":"function","name":%q}`, hashCandidate),
		fmt.Sprintf(`{"type":"function","name":%q}`, strings.Replace(hashCandidate, "p_q", "p.q", 1)),
		`{"type":"function","name":"p_q"}`,
	}
	identities := []string{"p.q", "p:q", hashCandidate, strings.Replace(hashCandidate, "p_q", "p.q", 1), "p_q"}
	var reference map[string]string
	for shift := 0; shift < len(tools); shift++ {
		ordered := append(append([]string{}, tools[shift:]...), tools[:shift]...)
		request := fmt.Sprintf(`{"model":"gpt-test","input":"hi","tools":[%s]}`, strings.Join(ordered, ","))
		names, _ := claudeToolNamesOf(t, request)
		byIdentity := map[string]string{}
		for _, name := range names {
			got, _ := restoreNonStream(t, request, name)
			byIdentity[got.name] = name
		}
		for _, identity := range identities {
			if _, ok := byIdentity[identity]; !ok {
				t.Fatalf("shift %d: identity %q not restorable, names %v", shift, identity, names)
			}
		}
		if byIdentity[hashCandidate] != hashCandidate || byIdentity["p_q"] != "p_q" {
			t.Fatalf("shift %d: valid names changed: %v", shift, byIdentity)
		}
		if reference == nil {
			reference = byIdentity
			continue
		}
		for identity, name := range reference {
			if byIdentity[identity] != name {
				t.Fatalf("shift %d: %q -> %q, want %q (order dependent)", shift, identity, byIdentity[identity], name)
			}
		}
	}
}

func TestClaudeToolNames_ForcedToolChoiceUsesCollidingToolName(t *testing.T) {
	for _, child := range []string{longPricesChild, longMetricsChild} {
		request := fmt.Sprintf(`{"model":"gpt-test","input":"hi","tools":[%s],"tool_choice":{"type":"function","namespace":%q,"name":%q}}`,
			collidingNamespaceTools(), longToolNamespace, child)
		names, root := claudeToolNamesOf(t, request)
		choice := root.Get("tool_choice.name").String()
		if choice == "" {
			t.Fatalf("tool_choice dropped: %s", root.Get("tool_choice").Raw)
		}
		if !strings.Contains(strings.Join(names, ","), choice) {
			t.Fatalf("tool_choice %q is not a declared tool %v", choice, names)
		}
		if got, _ := restoreNonStream(t, request, choice); got.name != child || got.namespace != longToolNamespace {
			t.Fatalf("tool_choice restores to %+v, want %s/%s", got, longToolNamespace, child)
		}
	}
}

func TestClaudeToolNames_ForcedToolChoiceForSanitizedName(t *testing.T) {
	request := `{"model":"gpt-test","input":"hi","tools":[{"type":"function","name":"a.b"}],"tool_choice":{"type":"function","name":"a.b"}}`
	_, root := claudeToolNamesOf(t, request)
	if got := root.Get("tool_choice.name").String(); got != "a_b" {
		t.Fatalf("tool_choice = %s, want name a_b", root.Get("tool_choice").Raw)
	}
}

func TestClaudeToolNames_ForcedToolChoicePrefersExactIdentity(t *testing.T) {
	request := `{"model":"gpt-test","input":"hi","tools":[
		{"type":"namespace","name":"n","tools":[{"type":"function","name":"x"}]},
		{"type":"namespace","name":"other","tools":[{"type":"function","name":"n__x"}]}],
		"tool_choice":{"type":"function","namespace":"n","name":"x"}}`
	_, root := claudeToolNamesOf(t, request)
	if got := root.Get("tool_choice.name").String(); got != "n__x" {
		t.Fatalf("tool_choice = %s, want n__x", root.Get("tool_choice").Raw)
	}
}

func historyRequest(extraInput string) string {
	input := fmt.Sprintf(`[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"prices and metrics"}]},
		{"type":"function_call","call_id":"call_prices","namespace":%q,"name":%q,"arguments":"{}"},
		{"type":"function_call","call_id":"call_metrics","namespace":%q,"name":%q,"arguments":"{}"},
		{"type":"function_call_output","call_id":"call_prices","output":"p"},
		{"type":"function_call_output","call_id":"call_metrics","output":"m"}%s]`,
		longToolNamespace, longPricesChild, longToolNamespace, longMetricsChild, extraInput)
	return fmt.Sprintf(`{"model":"gpt-test","input":%s,"tools":[%s]}`, input, collidingNamespaceTools())
}

func claudeToolUsesAndResults(root gjson.Result) (map[string]string, []string) {
	uses := map[string]string{}
	var results []string
	root.Get("messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "tool_use":
				uses[block.Get("id").String()] = block.Get("name").String()
			case "tool_result":
				results = append(results, block.Get("tool_use_id").String())
			}
			return true
		})
		return true
	})
	return uses, results
}

func TestClaudeToolNames_HistoryToolUseMatchesDeclaredNames(t *testing.T) {
	request := historyRequest("")
	names, root := claudeToolNamesOf(t, request)
	uses, results := claudeToolUsesAndResults(root)
	if len(uses) != 2 || uses["call_prices"] == uses["call_metrics"] {
		t.Fatalf("history tool_use names = %v, want two distinct names", uses)
	}
	for id, name := range uses {
		if !strings.Contains(","+strings.Join(names, ",")+",", ","+name+",") {
			t.Fatalf("history tool_use %s name %q is not a declared tool %v", id, name, names)
		}
	}
	if got, _ := restoreNonStream(t, request, uses["call_prices"]); got.name != longPricesChild || got.namespace != longToolNamespace {
		t.Fatalf("history prices name restores to %+v", got)
	}
	if strings.Join(results, ",") != "call_prices,call_metrics" {
		t.Fatalf("tool_result ids = %v", results)
	}
}

func TestClaudeToolNames_HistoryOnlyNameCannotTakeDeclaredName(t *testing.T) {
	request := `{"model":"gpt-test","input":[
		{"type":"function_call","call_id":"call_old","name":"a_b","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_old","output":"ok"}],
		"tools":[{"type":"function","name":"a.b"}]}`
	names, root := claudeToolNamesOf(t, request)
	uses, _ := claudeToolUsesAndResults(root)
	if len(names) != 1 || uses["call_old"] == names[0] {
		t.Fatalf("history-only a_b uses declared name: tools %v, uses %v", names, uses)
	}
	if got, _ := restoreNonStream(t, request, names[0]); got.name != "a.b" {
		t.Fatalf("declared name restores to %q, want a.b", got.name)
	}
}

func TestClaudeToolNames_NonStreamRestoresEachCollidingTool(t *testing.T) {
	request := fmt.Sprintf(`{"model":"gpt-test","input":"hi","tools":[%s]}`, collidingNamespaceTools())
	names, _ := claudeToolNamesOf(t, request)
	restored := map[string]bool{}
	for _, name := range names {
		got, _ := restoreNonStream(t, request, name)
		if got.itemType != "function_call" || got.namespace != longToolNamespace {
			t.Fatalf("%q restores to %+v", name, got)
		}
		restored[got.name] = true
	}
	if !restored[longPricesChild] || !restored[longMetricsChild] {
		t.Fatalf("restored children = %v", restored)
	}
}

func TestClaudeToolNames_StreamRestoresEachCollidingTool(t *testing.T) {
	request := fmt.Sprintf(`{"model":"gpt-test","input":"hi","tools":[%s]}`, collidingNamespaceTools())
	names, _ := claudeToolNamesOf(t, request)
	restored := map[string]bool{}
	for _, name := range names {
		var param any
		var added, argsDone, done, completed gjson.Result
		for _, chunk := range claudeToolUseStream(name, "toolu_stream", `{"q":1}`) {
			for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", []byte(request), nil, chunk, &param) {
				event, data := parseClaudeResponsesSSEEvent(t, output)
				switch event {
				case "response.output_item.added":
					added = data
				case "response.function_call_arguments.done":
					argsDone = data
				case "response.output_item.done":
					done = data
				case "response.completed":
					completed = data
				}
			}
		}
		for label, item := range map[string]gjson.Result{"added": added.Get("item"), "done": done.Get("item"), "completed": completed.Get("response.output.0")} {
			if item.Get("type").String() != "function_call" || item.Get("namespace").String() != longToolNamespace {
				t.Fatalf("%s item for %q = %s", label, name, item.Raw)
			}
			restored[item.Get("name").String()] = true
		}
		if argsDone.Get("item_id").String() != "fc_toolu_stream" || argsDone.Get("arguments").String() != `{"q":1}` {
			t.Fatalf("arguments.done = %s", argsDone.Raw)
		}
		if added.Get("item.name").String() != done.Get("item.name").String() {
			t.Fatalf("added/done names differ: %s vs %s", added.Get("item.name"), done.Get("item.name"))
		}
	}
	if !restored[longPricesChild] || !restored[longMetricsChild] {
		t.Fatalf("restored children = %v", restored)
	}
}

func TestClaudeToolNames_CollidingAdditionalCustomToolsRestoreAsCustom(t *testing.T) {
	request := fmt.Sprintf(`{"model":"gpt-test","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"additional_tools","tools":[{"type":"namespace","name":%q,"tools":[
			{"type":"custom","name":%q},{"type":"custom","name":%q}]}]}]}`, longToolNamespace, longPricesChild, longMetricsChild)
	names, _ := claudeToolNamesOf(t, request)
	if len(names) != 2 {
		t.Fatalf("tool count = %d, want 2: %v", len(names), names)
	}
	for _, name := range names {
		got, item := restoreNonStream(t, request, name)
		if got.itemType != "custom_tool_call" || got.namespace != longToolNamespace || item.Get("input").String() != "x" {
			t.Fatalf("non-stream %q restores to %s", name, item.Raw)
		}
		var param any
		var completed gjson.Result
		for _, chunk := range claudeToolUseStream(name, "toolu_custom", `{"input":"x"}`) {
			for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", []byte(request), nil, chunk, &param) {
				if event, data := parseClaudeResponsesSSEEvent(t, output); event == "response.completed" {
					completed = data
				}
			}
		}
		streamItem := completed.Get("response.output.0")
		if streamItem.Get("type").String() != "custom_tool_call" || streamItem.Get("name").String() != got.name || streamItem.Get("namespace").String() != longToolNamespace {
			t.Fatalf("stream %q restores to %s", name, streamItem.Raw)
		}
	}
}

// The items a client receives are sent back as history in the next turn. They
// must translate to the same Claude names as the declarations.
func TestClaudeToolNames_LaterTurnRoundTrip(t *testing.T) {
	first := fmt.Sprintf(`{"model":"gpt-test","input":"hi","tools":[%s]}`, collidingNamespaceTools())
	names, _ := claudeToolNamesOf(t, first)
	var history []string
	for i, name := range names {
		_, item := restoreNonStream(t, first, name)
		callID := fmt.Sprintf("call_turn_%d", i)
		item = gjson.Parse(strings.Replace(item.Raw, `"toolu_ns"`, fmt.Sprintf("%q", callID), -1))
		history = append(history, item.Raw, fmt.Sprintf(`{"type":"function_call_output","call_id":%q,"output":"ok"}`, callID))
	}
	second := fmt.Sprintf(`{"model":"gpt-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},%s],"tools":[%s]}`,
		strings.Join(history, ","), collidingNamespaceTools())
	secondNames, root := claudeToolNamesOf(t, second)
	if strings.Join(secondNames, ",") != strings.Join(names, ",") {
		t.Fatalf("second turn names %v differ from first %v", secondNames, names)
	}
	uses, _ := claudeToolUsesAndResults(root)
	for i, name := range names {
		if got := uses[fmt.Sprintf("call_turn_%d", i)]; got != name {
			t.Fatalf("turn call %d tool_use name = %q, want %q", i, got, name)
		}
	}
}

func assertClaudeToolNamesBijective(t *testing.T, m claudeToolNames, identities []string) {
	t.Helper()
	seen := map[string]string{}
	for _, identity := range identities {
		name, ok := m.toClaude[identity]
		if !ok {
			t.Fatalf("identity %q not mapped", identity)
		}
		if !validClaudeToolName.MatchString(name) {
			t.Fatalf("identity %q -> invalid name %q", identity, name)
		}
		if other, dup := seen[name]; dup {
			t.Fatalf("identities %q and %q share %q", identity, other, name)
		}
		seen[name] = identity
		if m.identity(name) != identity {
			t.Fatalf("name %q restores to %q, want %q", name, m.identity(name), identity)
		}
	}
}

func TestBuildClaudeToolNames_BijectiveAndOrderIndependent(t *testing.T) {
	long := strings.Repeat("n", 70)
	identities := []string{"a.b", "a_b", "a:b", "short", long[:64]}
	for i := 0; i < 20; i++ {
		identities = append(identities, fmt.Sprintf("%s%c%s", long, 'a'+i%26, strings.Repeat(".", i%3)))
	}
	requestFor := func(order []string) gjson.Result {
		var tools []string
		for _, identity := range order {
			tools = append(tools, fmt.Sprintf(`{"type":"function","name":%q}`, identity))
		}
		return gjson.Parse(`{"tools":[` + strings.Join(tools, ",") + `]}`)
	}
	reference := buildClaudeToolNames(requestFor(identities))
	assertClaudeToolNamesBijective(t, reference, identities)
	for shift := 1; shift < len(identities); shift++ {
		order := append(append([]string{}, identities[shift:]...), identities[:shift]...)
		for i, j := 0, len(order)-1; i < j && shift%2 == 0; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
		m := buildClaudeToolNames(requestFor(order))
		for _, identity := range identities {
			if m.toClaude[identity] != reference.toClaude[identity] {
				t.Fatalf("shift %d: %q -> %q, want %q", shift, identity, m.toClaude[identity], reference.toClaude[identity])
			}
		}
	}
}

func TestBuildClaudeToolNames_HashRetryWhenCandidateTaken(t *testing.T) {
	sum := sha256.Sum256([]byte("p.q"))
	firstCandidate := "p_q_" + hex.EncodeToString(sum[:])[:10]
	m := claudeToolNames{toClaude: map[string]string{}, fromClaude: map[string]string{}}
	taken := map[string]bool{"p_q": true, firstCandidate: true}
	m.allocate([]string{"p.q"}, taken)
	got := m.toClaude["p.q"]
	if got == firstCandidate || got == "p_q" || !validClaudeToolName.MatchString(got) {
		t.Fatalf("p.q -> %q, want a retried hashed name", got)
	}
}

func TestBuildClaudeToolNames_PassthroughAndHistory(t *testing.T) {
	root := gjson.Parse(`{"tools":[{"type":"web_search"},{"type":"function","name":"web.search"}],
		"input":[{"type":"function_call","name":"old.tool","call_id":"c"},{"type":"custom_tool_call","namespace":"ns","name":"x","call_id":"d"}]}`)
	m := buildClaudeToolNames(root)
	assertClaudeToolNamesBijective(t, m, []string{"web_search", "web.search", "old.tool", "ns__x"})
	if m.toClaude["web_search"] != "web_search" || m.toClaude["old.tool"] != "old_tool" || m.toClaude["ns__x"] != "ns__x" {
		t.Fatalf("unexpected names: %v", m.toClaude)
	}
	if m.identity("unknown_tool") != "unknown_tool" {
		t.Fatal("unknown names must pass through unchanged")
	}
}
