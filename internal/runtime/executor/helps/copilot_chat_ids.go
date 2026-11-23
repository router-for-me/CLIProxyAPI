package helps

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Some Chat models recycle IDs in later rounds. Rename a call and its result
// together, only when every occurrence is paired within its own tool batch.
func (input *CopilotTurnInput) UniqueChatToolIDs(doc *ojson.Doc, numberByName bool) {
	type pair struct {
		call, result, name string
		ordinal            int
	}
	ordinal := 0
	pairs := make(map[string][]pair)
	invalid := make(map[string]bool)
	reserved := make(map[string]bool)
	var batch map[string]int
	for i, msg := range doc.Get("messages").Array() {
		path := "messages." + strconv.Itoa(i)
		switch msg.Get("role").String() {
		case "assistant":
			batch = make(map[string]int)
			for j, call := range msg.Get("tool_calls").Array() {
				ordinal++
				id := call.Get("id").String()
				if id == "" {
					continue
				}
				if _, exists := batch[id]; exists {
					invalid[id] = true
				}
				batch[id] = len(pairs[id])
				reserved[id] = true
				pairs[id] = append(pairs[id], pair{call: path + ".tool_calls." + strconv.Itoa(j) + ".id", name: call.Get("function.name").String(), ordinal: ordinal - 1})
			}
		case "tool":
			id := msg.Get("tool_call_id").String()
			reserved[id] = true
			j, ok := batch[id]
			if !ok || pairs[id][j].result != "" {
				invalid[id] = true
				continue
			}
			pairs[id][j].result = path + ".tool_call_id"
		default:
			batch = nil
		}
	}
	for id, occurrences := range pairs {
		if invalid[id] || (!numberByName && len(occurrences) < 2) {
			continue
		}
		complete := true
		for _, p := range occurrences {
			if p.result == "" {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		originalUsed := false
		for i, p := range occurrences {
			if i == 0 && (!numberByName || p.name == "") {
				originalUsed = true
				continue
			}
			// Use the CLI's numbered form when available. The UUID form also covers
			// a collision with a real ID already present in another tool batch.
			next := ""
			if numberByName && p.name != "" {
				// The CLI numbers Kimi calls across the whole transcript, not per function.
				next = p.name + ":" + strconv.Itoa(p.ordinal)
				if !originalUsed && (next == id || reserved[next]) {
					originalUsed = true
					continue
				}
			} else if name, n, ok := strings.Cut(id, ":"); ok && n == "0" {
				next = name + ":" + strconv.Itoa(i)
			}
			for salt := 0; next == "" || reserved[next]; salt++ {
				next = "call_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(id+"\x00"+strconv.Itoa(i)+":"+strconv.Itoa(salt))).String()
			}
			reserved[next] = true
			_ = doc.Set(p.call, next)
			_ = doc.Set(p.result, next)
			if input.chatIssuedIDs == nil {
				input.chatIssuedIDs = make(map[string]string)
			}
			input.chatIssuedIDs[next] = id
		}
	}
	if input.chatIssuedIDs != nil {
		// Pin the final batch's wire spelling too: a payload filter or override
		// must not be able to substitute an earlier occurrence of the original ID.
		raw := *input
		raw.toolResultIDs = nil
		raw.chatIssuedIDs = nil
		input.chatWireIDs, _ = raw.wireCalls(translator.FormatOpenAI, doc.Get("messages"))
	}
}
