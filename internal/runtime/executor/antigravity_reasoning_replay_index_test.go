package executor

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// antigravityReplayItemContextHashForTest stamps an item with the production
// context fingerprint for contentIndex, going through the request index exactly
// as production does.
func antigravityReplayItemContextHashForTest(item, payload []byte, contentIndex int) []byte {
	return antigravitySetReplayItemContextHashValue(item, newAntigravityReplayRequestIndex(payload).contextFingerprint(contentIndex))
}

func legacyAntigravityReasoningReplayItemsFromRequest(payload []byte) [][]byte {
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return nil
	}
	items := make([][]byte, 0)
	contents.ForEach(func(contentKey, content gjson.Result) bool {
		if !strings.EqualFold(strings.TrimSpace(content.Get("role").String()), "model") {
			return true
		}
		contentIndex := int(contentKey.Int())
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		partArray := parts.Array()
		functionCallOccurrences := make(map[string]int)
		for partIndex, part := range partArray {
			signature := antigravityNativePartThoughtSignature(part)
			if !antigravityHasNativeThoughtSignature(signature) {
				signature = ""
			}
			if functionCall := part.Get("functionCall"); functionCall.Exists() {
				key := antigravityFunctionCallKey(functionCall.Get("name").String(), functionCall.Get("args").Raw, "")
				occurrence := functionCallOccurrences[key]
				if key != "" {
					functionCallOccurrences[key] = occurrence + 1
				}
				if item := buildAntigravityFunctionCallPartItem(contentIndex, partIndex, occurrence, functionCall, signature); len(item) > 0 {
					items = append(items, legacyAntigravitySetReplayItemContextHash(item, payload, contentIndex))
				}
				continue
			}
			if signature == "" {
				continue
			}
			targetPart := part
			targetPartIndex := partIndex
			kind, fingerprint := antigravityReplayPartFingerprint(targetPart)
			if fingerprint == "" && partIndex > 0 {
				targetPartIndex = partIndex - 1
				targetPart = partArray[targetPartIndex]
				kind, fingerprint = antigravityReplayPartFingerprint(targetPart)
			}
			if fingerprint == "" {
				continue
			}
			item := buildAntigravityThoughtSignatureItem(contentIndex, targetPartIndex, signature, kind, fingerprint)
			item, _ = sjson.SetBytes(item, "targetOccurrence", antigravityReplayPartOccurrence(partArray, targetPartIndex, kind, fingerprint))
			items = append(items, legacyAntigravitySetReplayItemContextHash(item, payload, contentIndex))
		}
		return true
	})
	return items
}

func legacyFilterAntigravityReasoningReplayItemsForRequestWithSchemas(payload []byte, items [][]byte, toolSchemas map[string]any) [][]byte {
	filtered := make([][]byte, 0, len(items))
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		switch strings.TrimSpace(itemResult.Get("type").String()) {
		case "function_call_part":
			signature := strings.TrimSpace(itemResult.Get("thoughtSignature").String())
			if contentIndex, partIndex, foundCall := legacyAntigravityFunctionCallPartLocationForReplayWithSchemas(payload, itemResult, toolSchemas); foundCall {
				part := gjson.GetBytes(payload, fmt.Sprintf("request.contents.%d.parts.%d", contentIndex, partIndex))
				currentID := strings.TrimSpace(part.Get("functionCall.id").String())
				nativeID := strings.TrimSpace(itemResult.Get("call_id").String())
				needsNativeRestore := currentID != nativeID || !bytes.Equal(
					antigravityCanonicalReplayJSON([]byte(part.Get("functionCall.args").Raw)),
					antigravityCanonicalReplayJSON([]byte(itemResult.Get("args").Raw)),
				)
				if !needsNativeRestore && (signature == "" || antigravityHasNativeThoughtSignature(part.Get("thoughtSignature").String())) {
					continue
				}
				break
			}
			if _, _, foundProvenance := legacyAntigravityFunctionCallProvenanceLocation(payload, itemResult, toolSchemas); foundProvenance {
				break
			}
			callID := strings.TrimSpace(itemResult.Get("call_id").String())
			if callID == "" {
				continue
			}
			responseIndex, _, foundResponse := legacyAntigravityFunctionResponseContentIndexForReplay(payload, itemResult)
			if !foundResponse {
				continue
			}
			contextMatches := legacyAntigravityReplayItemContextMatches(payload, itemResult, responseIndex)
			if !contextMatches && responseIndex > 0 {
				previousRole := gjson.GetBytes(payload, fmt.Sprintf("request.contents.%d.role", responseIndex-1)).String()
				contextMatches = strings.EqualFold(strings.TrimSpace(previousRole), "model") && legacyAntigravityReplayItemContextMatches(payload, itemResult, responseIndex-1)
			}
			if !contextMatches {
				continue
			}
		case "thought_signature":
			if legacyAntigravityRequestHasThoughtSignatureAt(payload, itemResult) {
				continue
			}
		default:
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func legacyApplyAntigravityReasoningReplayItems(payload []byte, items [][]byte, toolSchemas map[string]any) ([]byte, bool) {
	updated := payload
	changed := false
	for _, item := range items {
		eligible := legacyFilterAntigravityReasoningReplayItemsForRequestWithSchemas(updated, [][]byte{item}, toolSchemas)
		if len(eligible) != 1 {
			continue
		}
		next, applied := legacyInsertAntigravityReasoningReplayItemsWithSchemas(updated, eligible, toolSchemas)
		if !applied {
			continue
		}
		updated = next
		changed = true
	}
	return updated, changed
}

func TestAntigravityReplayContextFingerprintsMatchLegacy(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "empty", payload: []byte(`{}`)},
		{name: "system without contents", payload: []byte(`{"request":{"systemInstruction":{"parts":[{"text":"system"}]}}}`)},
		{name: "malformed contents", payload: []byte(`{"request":{"systemInstruction":{},"contents":`)},
		{name: "empty contents", payload: []byte(`{"request":{"contents":[]}}`)},
		{
			name: "system tools and signatures",
			payload: []byte(`{
				"request": {
					"systemInstruction": {"parts":[{"text":"system"}]},
					"tools": [{"functionDeclarations":[{"name":"lookup","parameters":{"type":"object"}}]}],
					"toolConfig": {"functionCallingConfig":{"mode":"AUTO"}},
					"contents": [
						{"role":"user","parts":[{"text":"hello"}]},
						{"role":"model","parts":[{"thought":true,"text":"think","thoughtSignature":"sig-a"},{"functionCall":{"id":"call-1","name":"lookup","args":{"z":1,"a":2}},"extra_content":{"google":{"thought_signature":"sig-b"}}}]},
						{"role":"model"},
						{"role":"user","parts":null}
					]
				}
			}`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := newAntigravityReplayRequestIndex(test.payload)
			for beforeContentIndex := -1; beforeContentIndex <= len(index.contents)+1; beforeContentIndex++ {
				want := legacyAntigravityReplayContextFingerprint(test.payload, beforeContentIndex)
				if got := index.contextFingerprint(beforeContentIndex); got != want {
					t.Fatalf("contextFingerprint(%d) = %q, want %q", beforeContentIndex, got, want)
				}
			}
		})
	}
}

func TestAntigravityReasoningReplayItemsFromIndexMatchLegacy(t *testing.T) {
	payload := []byte(`{
		"request": {
			"systemInstruction":{"parts":[{"text":"system"}]},
			"contents":[
				{"role":"user","parts":[{"text":"hello"}]},
				{"role":"model","parts":[
					{"thought":true,"text":"same","thoughtSignature":"sig-thought"},
					{"text":"same","thoughtSignature":"sig-text"},
					{"functionCall":{"id":"call-1","name":"lookup","args":{"value":1}},"thoughtSignature":"sig-call"},
					{"functionCall":{"id":"call-2","name":"lookup","args":{"value":1}}}
				]},
				{"role":"model","parts":[{"text":"same","thoughtSignature":"sig-text-2"}]}
			]
		}
	}`)

	want := legacyAntigravityReasoningReplayItemsFromRequest(payload)
	got := antigravityReasoningReplayItemsFromRequest(payload)
	if len(got) != len(want) {
		t.Fatalf("items = %d, want %d", len(got), len(want))
	}
	for itemIndex := range want {
		if !bytes.Equal(got[itemIndex], want[itemIndex]) {
			t.Fatalf("item %d differs\n got: %s\nwant: %s", itemIndex, got[itemIndex], want[itemIndex])
		}
	}
}

func TestAntigravityReasoningReplayItemsNilnessMatchesLegacy(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "missing contents", payload: []byte(`{}`)},
		{name: "malformed contents", payload: []byte(`{"request":{"contents":`)},
		{name: "contents not an array", payload: []byte(`{"request":{"contents":{"role":"model"}}}`)},
		{name: "empty contents", payload: []byte(`{"request":{"contents":[]}}`)},
		{name: "no model turn", payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := legacyAntigravityReasoningReplayItemsFromRequest(test.payload)
			got := antigravityReasoningReplayItemsFromRequest(test.payload)
			if (want == nil) != (got == nil) {
				t.Fatalf("nil-ness differs: legacy nil=%t, indexed nil=%t", want == nil, got == nil)
			}
			if len(got) != len(want) {
				t.Fatalf("items = %d, want %d", len(got), len(want))
			}
		})
	}
}

func TestFilterAntigravityReasoningReplayItemsWithIndexMatchesLegacy(t *testing.T) {
	payload := []byte(`{
		"request":{"contents":[
			{"role":"user","parts":[{"text":"hello"}]},
			{"role":"model","parts":[
				{"text":"answer","thoughtSignature":"sig-text"},
				{"functionCall":{"id":"call-1","name":"lookup","args":{"value":1}},"thoughtSignature":"sig-call"}
			]},
			{"role":"model","parts":[{"functionResponse":{"id":"call-1","name":"lookup","response":{"result":"ok"}}}]}
		]}
	}`)
	items := legacyAntigravityReasoningReplayItemsFromRequest(payload)
	withoutSignatures, errDelete := sjson.DeleteBytes(payload, "request.contents.1.parts.0.thoughtSignature")
	if errDelete != nil {
		t.Fatal(errDelete)
	}
	withoutSignatures, errDelete = sjson.DeleteBytes(withoutSignatures, "request.contents.1.parts.1.thoughtSignature")
	if errDelete != nil {
		t.Fatal(errDelete)
	}

	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "already present", payload: payload},
		{name: "missing signatures", payload: withoutSignatures},
		{name: "missing call", payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := legacyFilterAntigravityReasoningReplayItemsForRequestWithSchemas(test.payload, items, nil)
			got := filterAntigravityReasoningReplayItemsForRequestWithSchemas(test.payload, items, nil)
			if len(got) != len(want) {
				t.Fatalf("filtered items = %d, want %d", len(got), len(want))
			}
			for itemIndex := range want {
				if !bytes.Equal(got[itemIndex], want[itemIndex]) {
					t.Fatalf("item %d differs\n got: %s\nwant: %s", itemIndex, got[itemIndex], want[itemIndex])
				}
			}
		})
	}
}

func TestAntigravityReplayRequestIndexRandomizedDifferential(t *testing.T) {
	const randomSeed = 20260810
	randomSource := rand.New(rand.NewSource(randomSeed))
	basePayload := syntheticAntigravityReplayBenchmarkPayload(256, 8)
	items := legacyAntigravityReasoningReplayItemsFromRequest(basePayload)
	if len(items) != 8 {
		t.Fatalf("items = %d, want 8", len(items))
	}

	for caseIndex := range 100 {
		payload := bytes.Clone(basePayload)
		mutationCount := 1 + randomSource.Intn(4)
		for range mutationCount {
			turn := randomSource.Intn(8)
			callContentIndex := 1 + turn*2
			responseContentIndex := callContentIndex + 1
			var errSet error
			switch randomSource.Intn(7) {
			case 0:
				payload, errSet = sjson.DeleteBytes(payload, fmt.Sprintf("request.contents.%d.parts.0.thoughtSignature", callContentIndex))
			case 1:
				payload, errSet = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.parts.0.functionCall.id", callContentIndex), fmt.Sprintf("changed-%d", turn))
			case 2:
				payload, errSet = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.parts.0.functionCall.args.turn", callContentIndex), turn+100)
			case 3:
				payload, errSet = sjson.DeleteBytes(payload, fmt.Sprintf("request.contents.%d.parts.0.functionCall.id", callContentIndex))
			case 4:
				payload, errSet = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.parts.0.functionResponse.id", responseContentIndex), fmt.Sprintf("changed-%d", turn))
			case 5:
				payload, errSet = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.role", responseContentIndex), "user")
			case 6:
				payload, errSet = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.parts.0.functionCall.id", callContentIndex), "call-0")
			}
			if errSet != nil {
				t.Fatalf("case %d mutation failed: %v", caseIndex, errSet)
			}
		}

		wantFiltered := legacyFilterAntigravityReasoningReplayItemsForRequestWithSchemas(payload, items, nil)
		gotFiltered := filterAntigravityReasoningReplayItemsForRequestWithSchemas(payload, items, nil)
		if len(gotFiltered) != len(wantFiltered) {
			t.Fatalf("seed=%d case=%d filtered=%d want=%d", randomSeed, caseIndex, len(gotFiltered), len(wantFiltered))
		}
		for itemIndex := range wantFiltered {
			if !bytes.Equal(gotFiltered[itemIndex], wantFiltered[itemIndex]) {
				t.Fatalf("seed=%d case=%d filtered item %d differs", randomSeed, caseIndex, itemIndex)
			}
		}

		wantPayload, wantChanged := legacyApplyAntigravityReasoningReplayItems(payload, items, nil)
		gotPayload, gotChanged := applyAntigravityReasoningReplayItems(payload, items, nil)
		if gotChanged != wantChanged || !bytes.Equal(gotPayload, wantPayload) {
			t.Fatalf("seed=%d case=%d apply differs: changed=%t want=%t", randomSeed, caseIndex, gotChanged, wantChanged)
		}
	}
}

func TestAntigravityReasoningReplayAccumulatorUsesIndexedContextHash(t *testing.T) {
	payload := []byte(`{
		"request": {
			"systemInstruction":{"parts":[{"text":"system"}]},
			"contents":[{"role":"user","parts":[{"text":"hello"}]}]
		}
	}`)
	scope := antigravityReasoningReplayScope{modelName: "gemini-test", sessionKey: "session:test"}
	accumulator := newAntigravityReasoningReplayAccumulator(scope, payload)
	if accumulator == nil {
		t.Fatal("accumulator is nil")
	}
	wantContextHash := legacyAntigravityReplayContextFingerprint(payload, 1)
	if accumulator.responseContextHash != wantContextHash {
		t.Fatalf("response context hash = %q, want %q", accumulator.responseContextHash, wantContextHash)
	}
	accumulator.observeResponsePayload([]byte(`{
		"response":{"candidates":[{
			"content":{"parts":[{"functionCall":{"id":"call-1","name":"lookup","args":{"value":1}},"thoughtSignature":"sig-call"}]},
			"finishReason":"STOP"
		}]}
	}`))
	if len(accumulator.items) != 1 {
		t.Fatalf("items = %d, want 1", len(accumulator.items))
	}
	if got := gjson.GetBytes(accumulator.items[0], "contextHash").String(); got != wantContextHash {
		t.Fatalf("item context hash = %q, want %q", got, wantContextHash)
	}
}

func TestApplyAntigravityReasoningReplayItemsRebuildsIndexAfterMutation(t *testing.T) {
	items := [][]byte{
		[]byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"name":"Read","call_id":"id1","args":{"file_path":"/a"},"thoughtSignature":"sig-first"}`),
		[]byte(`{"type":"function_call_part","contentIndex":3,"partIndex":0,"name":"Write","call_id":"id2","args":{"file_path":"/b"},"thoughtSignature":"sig-second"}`),
	}
	payload := []byte(`{
		"request":{"contents":[
			{"role":"user","parts":[{"text":"hi"}]},
			{"role":"model","parts":[{"functionResponse":{"id":"id1","name":"Read","response":{"result":"ok"}}}]},
			{"role":"user","parts":[{"text":"next"}]},
			{"role":"model","parts":[{"functionResponse":{"id":"id2","name":"Write","response":{"result":"ok"}}}]}
		]}
	}`)

	want, wantChanged := legacyApplyAntigravityReasoningReplayItems(payload, items, nil)
	got, gotChanged := applyAntigravityReasoningReplayItems(payload, items, nil)
	if gotChanged != wantChanged {
		t.Fatalf("changed = %t, want %t", gotChanged, wantChanged)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("payload differs\n got: %s\nwant: %s", got, want)
	}
}

var antigravityReplayBenchmarkItems [][]byte

func BenchmarkAntigravityReasoningReplayItemsFromRequest(b *testing.B) {
	payload := syntheticAntigravityReplayBenchmarkPayload(1<<20, 32)

	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			antigravityReplayBenchmarkItems = legacyAntigravityReasoningReplayItemsFromRequest(payload)
		}
	})
	b.Run("indexed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			antigravityReplayBenchmarkItems = antigravityReasoningReplayItemsFromRequest(payload)
		}
	})
}

func BenchmarkFilterAntigravityReasoningReplayItems(b *testing.B) {
	payload := syntheticAntigravityReplayBenchmarkPayload(1<<20, 32)
	items := antigravityReasoningReplayItemsFromRequest(payload)
	if len(items) == 0 {
		b.Fatal("benchmark generated no replay items")
	}

	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			antigravityReplayBenchmarkItems = legacyFilterAntigravityReasoningReplayItemsForRequestWithSchemas(payload, items, nil)
		}
	})
	b.Run("indexed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			antigravityReplayBenchmarkItems = filterAntigravityReasoningReplayItemsForRequestWithSchemas(payload, items, nil)
		}
	})
}

func syntheticAntigravityReplayBenchmarkPayload(inlineBytes, turns int) []byte {
	var payload strings.Builder
	payload.Grow(inlineBytes + turns*256)
	payload.WriteString(`{"request":{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"application/octet-stream","data":"`)
	payload.WriteString(strings.Repeat("a", inlineBytes))
	payload.WriteString(`"}}]}`)
	for turn := range turns {
		fmt.Fprintf(
			&payload,
			`,{"role":"model","parts":[{"functionCall":{"id":"call-%d","name":"lookup","args":{"turn":%d}},"thoughtSignature":"sig-%d"}]}`,
			turn,
			turn,
			turn,
		)
		fmt.Fprintf(
			&payload,
			`,{"role":"model","parts":[{"functionResponse":{"id":"call-%d","name":"lookup","response":{"result":"ok"}}}]}`,
			turn,
		)
	}
	payload.WriteString(`]}}`)
	return []byte(payload.String())
}

// syntheticAntigravityReplayMixedPayload builds a history whose model turns mix
// thought parts, text parts and function calls, so the extracted ledger contains
// both thought_signature and function_call_part items.
func syntheticAntigravityReplayMixedPayload(turns int) []byte {
	var payload strings.Builder
	payload.WriteString(`{"request":{"systemInstruction":{"parts":[{"text":"sys"}]},"contents":[`)
	payload.WriteString(`{"role":"user","parts":[{"text":"start"}]}`)
	for turn := range turns {
		fmt.Fprintf(&payload,
			`,{"role":"model","parts":[`+
				`{"thought":true,"text":"reason-%d","thoughtSignature":"tsig-%d"},`+
				`{"text":"say-%d","thoughtSignature":"xsig-%d"},`+
				`{"functionCall":{"id":"call-%d","name":"lookup","args":{"turn":%d}},"thoughtSignature":"csig-%d"}`+
				`]}`,
			turn, turn, turn, turn, turn, turn, turn)
		fmt.Fprintf(&payload,
			`,{"role":"user","parts":[{"functionResponse":{"id":"call-%d","name":"lookup","response":{"result":"ok-%d"}}}]}`,
			turn, turn)
	}
	payload.WriteString(`]}}`)
	return []byte(payload.String())
}

func TestAntigravityReplayMergeRandomizedDifferential(t *testing.T) {
	const randomSeed = 20260811
	const turns = 6
	randomSource := rand.New(rand.NewSource(randomSeed))
	basePayload := syntheticAntigravityReplayMixedPayload(turns)

	items := legacyAntigravityReasoningReplayItemsFromRequest(basePayload)
	thoughtItems, callItems := 0, 0
	for _, item := range items {
		switch gjson.GetBytes(item, "type").String() {
		case "thought_signature":
			thoughtItems++
		case "function_call_part":
			callItems++
		}
	}
	if thoughtItems == 0 || callItems == 0 {
		t.Fatalf("ledger must mix item kinds: thought=%d call=%d", thoughtItems, callItems)
	}

	applied := 0
	for caseIndex := range 300 {
		payload := bytes.Clone(basePayload)
		for range 1 + randomSource.Intn(5) {
			turn := randomSource.Intn(turns)
			modelIndex := 1 + turn*2
			responseIndex := modelIndex + 1
			part := randomSource.Intn(3)
			var errSet error
			switch randomSource.Intn(9) {
			case 0:
				payload, errSet = sjson.DeleteBytes(payload, fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", modelIndex, part))
			case 1:
				payload, errSet = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.parts.2.functionCall.args.turn", modelIndex), turn+50)
			case 2:
				payload, errSet = sjson.DeleteBytes(payload, fmt.Sprintf("request.contents.%d.parts.2.functionCall.id", modelIndex))
			case 3:
				payload, errSet = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.parts.1.text", modelIndex), "drifted")
			case 4:
				payload, errSet = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.role", responseIndex), "model")
			case 5:
				payload, errSet = sjson.DeleteBytes(payload, fmt.Sprintf("request.contents.%d.parts.%d", modelIndex, part))
			case 6:
				payload, errSet = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.parts.0.thought", modelIndex), false)
			case 7:
				payload, errSet = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.parts.2.functionCall.id", modelIndex), "call-0")
			case 8:
				payload, errSet = sjson.SetBytes(payload, "request.toolConfig.functionCallingConfig.mode", "ANY")
			}
			if errSet != nil {
				t.Fatalf("case %d mutation failed: %v", caseIndex, errSet)
			}
		}

		wantPayload, wantChanged := legacyApplyAntigravityReasoningReplayItems(payload, items, nil)
		gotPayload, gotChanged := applyAntigravityReasoningReplayItems(payload, items, nil)
		if gotChanged != wantChanged {
			t.Fatalf("seed=%d case=%d changed=%t want=%t", randomSeed, caseIndex, gotChanged, wantChanged)
		}
		if !bytes.Equal(gotPayload, wantPayload) {
			t.Fatalf("seed=%d case=%d payload differs\n got: %s\nwant: %s", randomSeed, caseIndex, gotPayload, wantPayload)
		}
		if wantChanged {
			applied++
		}
	}
	if applied == 0 {
		t.Fatal("no case applied a replay item; the differential proved nothing")
	}
	t.Logf("cases=300 casesThatApplied=%d ledgerItems=%d (thought=%d call=%d)", applied, len(items), thoughtItems, callItems)
}

// TestAntigravityReplayNonArrayPartsFailsClosed pins an INTENTIONAL behavior
// change made when the merge path moved onto the request index.
//
// gjson's Result.Array() returns a one-element slice for a value that exists but
// is neither null nor an array, so the pre-index fallback scans could "locate" a
// functionCall inside a parts OBJECT. The index only walks parts when IsArray(),
// which is what the primary ID lookup always did, so malformed parts now fail
// closed consistently instead of depending on which branch ran.
func TestAntigravityReplayNonArrayPartsFailsClosed(t *testing.T) {
	payload := []byte(`{"request":{"contents":[` +
		`{"role":"user","parts":[{"text":"hi"}]},` +
		`{"role":"model","parts":{"functionCall":{"name":"lookup","args":{"value":1}}}}` +
		`]}}`)
	items := [][]byte{
		[]byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"name":"lookup","call_id":"call-1","args":{"value":1},"thoughtSignature":"sig-x"}`),
	}

	if kept := filterAntigravityReasoningReplayItemsForRequestWithSchemas(payload, items, nil); len(kept) != 0 {
		t.Fatalf("malformed parts must not yield an eligible item, kept=%d", len(kept))
	}
	got, changed := applyAntigravityReasoningReplayItems(payload, items, nil)
	if changed || !bytes.Equal(got, payload) {
		t.Fatalf("malformed parts must not be mutated: changed=%t body=%s", changed, got)
	}
	// The legacy oracle accepted the item at the filter layer but could not write
	// it either, so the observable end state was already identical.
	if _, legacyChanged := legacyApplyAntigravityReasoningReplayItems(payload, items, nil); legacyChanged {
		t.Fatal("legacy oracle unexpectedly mutated malformed parts")
	}
}

// TestAntigravityReplayLegacyItemWithoutTargetHash covers the positional
// fallback used by pre-targetHash cache entries. Such an item carries no proof
// of which part owns the signature, so it must attach to the LAST semantic part
// of the model content (the streamed chunks collapse into one part on replay),
// and only when the context fingerprint still matches.
func TestAntigravityReplayLegacyItemWithoutTargetHash(t *testing.T) {
	payload := []byte(`{"request":{"contents":[` +
		`{"role":"user","parts":[{"text":"ask"}]},` +
		`{"role":"model","parts":[{"text":"chunk-a"},{"text":"chunk-b"},{"text":"chunk-c"}]}` +
		`]}}`)
	const signature = "legacy-positional-signature-12345"
	// partIndex 7 is out of range on purpose: legacy entries pointed at a
	// streamed signature-only part that no longer exists.
	item := buildAntigravityThoughtSignatureItem(1, 7, signature, "", "")
	item = antigravityReplayItemContextHashForTest(item, payload, 1)
	if gjson.GetBytes(item, "targetHash").Exists() {
		t.Fatal("this test must exercise the no-targetHash path")
	}

	got, changed := applyAntigravityReasoningReplayItems(payload, [][]byte{item}, nil)
	if !changed {
		t.Fatalf("legacy positional item was not applied: %s", got)
	}
	if sig := gjson.GetBytes(got, "request.contents.1.parts.2.thoughtSignature").String(); sig != signature {
		t.Fatalf("signature must attach to the LAST semantic part, got parts.2=%q body=%s", sig, got)
	}
	for _, path := range []string{"request.contents.1.parts.0.thoughtSignature", "request.contents.1.parts.1.thoughtSignature"} {
		if gjson.GetBytes(got, path).Exists() {
			t.Fatalf("signature leaked to %s: %s", path, got)
		}
	}

	want, wantChanged := legacyApplyAntigravityReasoningReplayItems(payload, [][]byte{item}, nil)
	if wantChanged != changed || !bytes.Equal(want, got) {
		t.Fatalf("legacy oracle disagrees\n got: %s\nwant: %s", got, want)
	}

	// Context drift must reject the positional guess entirely.
	drifted, errSet := sjson.SetBytes(payload, "request.contents.0.parts.0.text", "different question")
	if errSet != nil {
		t.Fatal(errSet)
	}
	driftedOut, driftedChanged := applyAntigravityReasoningReplayItems(drifted, [][]byte{item}, nil)
	if driftedChanged || !bytes.Equal(driftedOut, drifted) {
		t.Fatalf("context drift must reject a positional legacy item: changed=%t body=%s", driftedChanged, driftedOut)
	}
}

// BenchmarkApplyAntigravityReasoningReplayItems measures the WRITE path, where
// every ledger item actually mutates the payload. This is the worst case for the
// request index because it is rebuilt after each mutation.
func BenchmarkApplyAntigravityReasoningReplayItems(b *testing.B) {
	const turns = 32
	base := syntheticAntigravityReplayBenchmarkPayload(1<<20, turns)
	items := antigravityReasoningReplayItemsFromRequest(base)
	if len(items) != turns {
		b.Fatalf("items = %d, want %d", len(items), turns)
	}
	payload := base
	for turn := range turns {
		var err error
		payload, err = sjson.DeleteBytes(payload, fmt.Sprintf("request.contents.%d.parts.0.thoughtSignature", 1+turn*2))
		if err != nil {
			b.Fatal(err)
		}
	}
	if _, changed := applyAntigravityReasoningReplayItems(payload, items, nil); !changed {
		b.Fatal("benchmark payload applies nothing")
	}

	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = legacyApplyAntigravityReasoningReplayItems(payload, items, nil)
		}
	})
	b.Run("indexed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = applyAntigravityReasoningReplayItems(payload, items, nil)
		}
	})
}
