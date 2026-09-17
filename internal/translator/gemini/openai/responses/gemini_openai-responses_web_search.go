package responses

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ModelSupportsWebSearch checks whether the given model supports native web search,
// checking both dynamic registry capability (SupportsWebSearch) and models.json
// static definitions (native_capabilities.web_search). Explicit false wins as a veto.
func ModelSupportsWebSearch(modelID string) bool {
	info := registry.LookupModelInfo(modelID)
	infoAG := registry.LookupModelInfo(modelID, "antigravity")

	// 1. Explicit false in static definitions acts as an absolute veto.
	if info != nil && info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && !*info.NativeCapabilities.WebSearch {
		return false
	}
	if infoAG != nil && infoAG.NativeCapabilities != nil && infoAG.NativeCapabilities.WebSearch != nil && !*infoAG.NativeCapabilities.WebSearch {
		return false
	}

	// 2. Explicit true in static definitions.
	if info != nil && info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && *info.NativeCapabilities.WebSearch {
		return true
	}
	if infoAG != nil && infoAG.NativeCapabilities != nil && infoAG.NativeCapabilities.WebSearch != nil && *infoAG.NativeCapabilities.WebSearch {
		return true
	}

	// 3. Dynamic capability checks via Antigravity probes and registry flags.
	if registry.AntigravityWebSearchModelFor(modelID) != "" {
		return true
	}
	if (info != nil && info.SupportsWebSearch) || (infoAG != nil && infoAG.SupportsWebSearch) {
		return true
	}
	return false
}

// isResponsesWebSearchToolType checks whether a tool type matches OpenAI Responses web search tool.
// Official OpenAI documentation supports "web_search", "web_search_2025_08_26", and legacy
// "web_search_preview" / "web_search_preview_2025_03_11".
func isResponsesWebSearchToolType(toolType string) bool {
	switch toolType {
	case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
		return true
	default:
		return false
	}
}

// HasResponsesWebSearchTool checks if the request tools array contains an OpenAI web search tool.
func HasResponsesWebSearchTool(root gjson.Result) bool {
	tools := root.Get("tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if isResponsesWebSearchToolType(tool.Get("type").String()) {
			return true
		}
	}
	return false
}

// HasOnlyResponsesWebSearchTools checks if every tool in the tools array is a web search tool.
func HasOnlyResponsesWebSearchTools(root gjson.Result) bool {
	tools := root.Get("tools")
	if !tools.IsArray() {
		return false
	}
	hasSearch := false
	for _, tool := range tools.Array() {
		if isResponsesWebSearchToolType(tool.Get("type").String()) {
			hasSearch = true
			continue
		}
		return false
	}
	return hasSearch
}

// AllowsResponsesWebSearchToolChoice checks whether tool_choice permits web search execution.
func AllowsResponsesWebSearchToolChoice(root gjson.Result) bool {
	toolChoice := root.Get("tool_choice")
	if !toolChoice.Exists() {
		return true
	}
	if toolChoice.Type == gjson.String {
		switch toolChoice.String() {
		case "", "auto", "required":
			return true
		case "none":
			return false
		default:
			return false
		}
	}
	if toolChoice.IsObject() {
		switch toolChoice.Get("type").String() {
		case "", "auto", "required":
			return true
		case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
			return true
		case "allowed_tools":
			tools := toolChoice.Get("tools")
			if tools.IsArray() {
				for _, t := range tools.Array() {
					if isResponsesWebSearchToolType(t.Get("type").String()) {
						return true
					}
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// ExtractResponsesWebSearchQuery extracts the search query from OpenAI Responses input.
// It handles simple string inputs, array of conversation messages, direct content part arrays, and instruction fallbacks.
func ExtractResponsesWebSearchQuery(root gjson.Result) string {
	input := root.Get("input")
	if input.Type == gjson.String {
		return strings.TrimSpace(input.String())
	}
	if input.IsArray() {
		items := input.Array()
		// Check if input is a flat array of content parts (e.g. [{"type":"input_text",...}])
		var flatParts []string
		isFlatParts := true
		for _, item := range items {
			if item.Get("type").String() == "input_text" {
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					flatParts = append(flatParts, text)
				}
			} else if item.Get("role").Exists() {
				isFlatParts = false
				break
			}
		}
		if isFlatParts && len(flatParts) > 0 {
			return strings.Join(flatParts, "\n")
		}

		for i := len(items) - 1; i >= 0; i-- {
			item := items[i]
			role := item.Get("role").String()
			if role != "" && role != "user" {
				continue
			}
			content := item.Get("content")
			if content.Type == gjson.String && strings.TrimSpace(content.String()) != "" {
				return strings.TrimSpace(content.String())
			}
			if content.IsArray() {
				var textParts []string
				for _, part := range content.Array() {
					if text := strings.TrimSpace(part.Get("text").String()); text != "" {
						textParts = append(textParts, text)
					}
				}
				if len(textParts) > 0 {
					return strings.Join(textParts, "\n")
				}
			}
			if text := strings.TrimSpace(item.Get("text").String()); text != "" {
				return text
			}
		}
	}
	if instructions := root.Get("instructions").String(); strings.TrimSpace(instructions) != "" {
		return strings.TrimSpace(instructions)
	}
	return ""
}

// ExtractResponsesWebSearchAllowedDomains extracts allowed domains from tools[].filters.allowed_domains.
func ExtractResponsesWebSearchAllowedDomains(root gjson.Result) []string {
	tools := root.Get("tools")
	if !tools.IsArray() {
		return nil
	}
	for _, tool := range tools.Array() {
		if !isResponsesWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		allowedDomains := tool.Get("filters.allowed_domains")
		if !allowedDomains.IsArray() {
			continue
		}
		var domains []string
		for _, domain := range allowedDomains.Array() {
			if d := strings.TrimSpace(domain.String()); d != "" {
				domains = append(domains, d)
			}
		}
		return domains
	}
	return nil
}

// ExtractGroundingMetadata locates groundingMetadata in either direct or wrapped Gemini response.
func ExtractGroundingMetadata(root gjson.Result) gjson.Result {
	if gm := root.Get("candidates.0.groundingMetadata"); gm.Exists() {
		return gm
	}
	if gm := root.Get("response.candidates.0.groundingMetadata"); gm.Exists() {
		return gm
	}
	return gjson.Result{}
}

// ExtractGroundingQueries retrieves the list of search queries from groundingMetadata.
func ExtractGroundingQueries(groundingMetadata gjson.Result) []string {
	var queries []string
	if webSearchQueries := groundingMetadata.Get("webSearchQueries"); webSearchQueries.IsArray() {
		for _, q := range webSearchQueries.Array() {
			if str := strings.TrimSpace(q.String()); str != "" {
				queries = append(queries, str)
			}
		}
	}
	return queries
}

// ExtractGroundingSources converts groundingMetadata groundingChunks into OpenAI Responses source objects.
func ExtractGroundingSources(groundingMetadata gjson.Result) [][]byte {
	chunks := groundingMetadata.Get("groundingChunks").Array()
	if len(chunks) == 0 {
		return nil
	}
	var sources [][]byte
	seenURLs := make(map[string]bool)
	for _, chunk := range chunks {
		uri := strings.TrimSpace(chunk.Get("web.uri").String())
		if uri == "" || seenURLs[uri] {
			continue
		}
		seenURLs[uri] = true
		src := []byte(`{"type":"url","url":""}`)
		src, _ = sjson.SetBytes(src, "url", uri)
		sources = append(sources, src)
	}
	return sources
}

// BuildResponsesWebSearchCallItem formats an OpenAI Responses web_search_call output item.
func BuildResponsesWebSearchCallItem(id string, query string, queries []string, sources [][]byte) []byte {
	item := []byte(`{"id":"","type":"web_search_call","status":"completed","action":{"type":"search","query":""}}`)
	item, _ = sjson.SetBytes(item, "id", id)
	item, _ = sjson.SetBytes(item, "action.query", query)
	if len(queries) > 0 {
		item, _ = sjson.SetBytes(item, "action.queries", queries)
	}
	if len(sources) > 0 {
		item, _ = sjson.SetRawBytes(item, "action.sources", translatorcommon.JoinRawArray(sources))
	}
	return item
}

// HasValidWebGrounding checks whether groundingMetadata contains actual web search queries
// or web grounding chunks with a valid non-empty URI.
func HasValidWebGrounding(groundingMetadata gjson.Result) bool {
	if !groundingMetadata.Exists() {
		return false
	}
	if queries := groundingMetadata.Get("webSearchQueries"); queries.IsArray() && len(queries.Array()) > 0 {
		for _, q := range queries.Array() {
			if strings.TrimSpace(q.String()) != "" {
				return true
			}
		}
	}
	if chunks := groundingMetadata.Get("groundingChunks"); chunks.IsArray() && len(chunks.Array()) > 0 {
		for _, c := range chunks.Array() {
			if strings.TrimSpace(c.Get("web.uri").String()) != "" {
				return true
			}
		}
	}
	return false
}

// MergeGroundingMetadata merges incremental groundingMetadata into existing groundingMetadata,
// combining webSearchQueries, merging groundingChunks with index remapping, and merging groundingSupports.
func MergeGroundingMetadata(existingGM, newGM gjson.Result) gjson.Result {
	if (!existingGM.Exists() || len(strings.TrimSpace(existingGM.Raw)) == 0) &&
		(!newGM.Exists() || len(strings.TrimSpace(newGM.Raw)) == 0) {
		return existingGM
	}
	if !existingGM.Exists() || len(strings.TrimSpace(existingGM.Raw)) == 0 {
		existingGM = gjson.Parse("{}")
	}
	if !newGM.Exists() || len(strings.TrimSpace(newGM.Raw)) == 0 {
		return existingGM
	}

	merged := existingGM.Raw

	// 1. Merge webSearchQueries (preserving order, avoiding duplicates)
	existingQueries := ExtractGroundingQueries(existingGM)
	newQueries := ExtractGroundingQueries(newGM)
	if len(newQueries) > 0 {
		seenQuery := make(map[string]bool)
		var mergedQueries []string
		for _, q := range existingQueries {
			if !seenQuery[q] {
				seenQuery[q] = true
				mergedQueries = append(mergedQueries, q)
			}
		}
		for _, q := range newQueries {
			if !seenQuery[q] {
				seenQuery[q] = true
				mergedQueries = append(mergedQueries, q)
			}
		}
		merged, _ = sjson.Set(merged, "webSearchQueries", mergedQueries)
	}

	// 2. Merge groundingChunks with index remapping
	existingChunks := existingGM.Get("groundingChunks").Array()
	newChunks := newGM.Get("groundingChunks").Array()

	// Load cumulative remap mapping and raw chunk count if previously saved
	cumulativeRemap := make(map[int]int)
	if remapJSON := existingGM.Get("_chunkIndexRemap"); remapJSON.Exists() && remapJSON.IsObject() {
		for k, v := range remapJSON.Map() {
			if oldIdx, errAtoi := strconv.Atoi(k); errAtoi == nil {
				cumulativeRemap[oldIdx] = int(v.Int())
			}
		}
	}
	prevRawCount := 0
	if rawCountRes := existingGM.Get("_rawChunkCount"); rawCountRes.Exists() {
		prevRawCount = int(rawCountRes.Int())
	} else {
		prevRawCount = len(existingChunks)
	}
	if len(cumulativeRemap) == 0 && len(existingChunks) > 0 {
		for i := 0; i < len(existingChunks); i++ {
			cumulativeRemap[i] = i
		}
	}

	var mergedChunksRaw []string
	uriToMergedIndex := make(map[string]int)
	rawToMergedIndex := make(map[string]int)

	for i, chunk := range existingChunks {
		mergedChunksRaw = append(mergedChunksRaw, chunk.Raw)
		uri := strings.TrimSpace(chunk.Get("web.uri").String())
		if uri != "" {
			if _, exists := uriToMergedIndex[uri]; !exists {
				uriToMergedIndex[uri] = i
			}
		}
		rawToMergedIndex[chunk.Raw] = i
	}

	for i, chunk := range newChunks {
		uri := strings.TrimSpace(chunk.Get("web.uri").String())
		title := strings.TrimSpace(chunk.Get("web.title").String())
		newRawIdx := prevRawCount + i
		if uri != "" {
			if existingIdx, exists := uriToMergedIndex[uri]; exists {
				cumulativeRemap[newRawIdx] = existingIdx
				if prevRawCount == 0 {
					cumulativeRemap[i] = existingIdx
				}
				if title != "" {
					existingChunk := gjson.Parse(mergedChunksRaw[existingIdx])
					if strings.TrimSpace(existingChunk.Get("web.title").String()) == "" {
						updated, _ := sjson.Set(mergedChunksRaw[existingIdx], "web.title", title)
						mergedChunksRaw[existingIdx] = updated
					}
				}
				continue
			}
		} else if existingIdx, exists := rawToMergedIndex[chunk.Raw]; exists {
			cumulativeRemap[newRawIdx] = existingIdx
			if prevRawCount == 0 {
				cumulativeRemap[i] = existingIdx
			}
			continue
		}

		newIdx := len(mergedChunksRaw)
		mergedChunksRaw = append(mergedChunksRaw, chunk.Raw)
		if uri != "" {
			uriToMergedIndex[uri] = newIdx
		}
		rawToMergedIndex[chunk.Raw] = newIdx
		cumulativeRemap[newRawIdx] = newIdx
		if prevRawCount == 0 {
			cumulativeRemap[i] = newIdx
		}
	}

	if len(mergedChunksRaw) > 0 {
		chunksJSON := "[" + strings.Join(mergedChunksRaw, ",") + "]"
		merged, _ = sjson.SetRaw(merged, "groundingChunks", chunksJSON)
	}

	totalRawCount := prevRawCount + len(newChunks)
	if len(cumulativeRemap) > 0 {
		keys := make([]int, 0, len(cumulativeRemap))
		for k := range cumulativeRemap {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		remapPairs := make([]string, 0, len(keys))
		for _, k := range keys {
			remapPairs = append(remapPairs, fmt.Sprintf("%q:%d", strconv.Itoa(k), cumulativeRemap[k]))
		}
		merged, _ = sjson.SetRaw(merged, "_chunkIndexRemap", "{"+strings.Join(remapPairs, ",")+"}")
		merged, _ = sjson.Set(merged, "_rawChunkCount", totalRawCount)
	}

	// 3. Merge groundingSupports with remapped chunk indices
	existingSupports := existingGM.Get("groundingSupports").Array()
	newSupports := newGM.Get("groundingSupports").Array()

	supportKey := func(partIndex, startByte, endByte int64, chunkIndices []int64) string {
		sortedIndices := make([]int64, len(chunkIndices))
		copy(sortedIndices, chunkIndices)
		sort.Slice(sortedIndices, func(i, j int) bool { return sortedIndices[i] < sortedIndices[j] })
		return fmt.Sprintf("%d:%d:%d:%v", partIndex, startByte, endByte, sortedIndices)
	}

	seenSupports := make(map[string]bool)
	var mergedSupportsRaw []string

	existingChunkCount := len(existingChunks)

	remapSupportIndices := func(origIndices []gjson.Result, isExisting bool) ([]int64, bool) {
		var remappedIndices []int64
		needRewrite := false
		for _, idxRes := range origIndices {
			oldIdx := int(idxRes.Int())
			targetIdx := int64(oldIdx)
			if isExisting {
				// Existing supports hold cumulative raw stream indices.
				// If unresolved (pending), resolve strictly using cumulativeRemap.
				shouldRemap := existingChunkCount == 0 || oldIdx >= existingChunkCount
				if shouldRemap {
					if target, ok := cumulativeRemap[oldIdx]; ok {
						targetIdx = int64(target)
						if targetIdx != int64(oldIdx) {
							needRewrite = true
						}
					}
				}
			} else {
				// In Gemini's API, groundingChunkIndices in groundingSupports is always a cumulative,
				// stream-wide raw chunk index. Unify resolution through cumulativeRemap so that chunks
				// from both past frames and the current frame are correctly resolved without collision.
				if target, ok := cumulativeRemap[oldIdx]; ok {
					targetIdx = int64(target)
					if targetIdx != int64(oldIdx) {
						needRewrite = true
					}
				}
			}
			alreadyHas := false
			for _, prev := range remappedIndices {
				if prev == targetIdx {
					alreadyHas = true
					break
				}
			}
			if !alreadyHas {
				remappedIndices = append(remappedIndices, targetIdx)
			}
		}
		if len(remappedIndices) != len(origIndices) {
			needRewrite = true
		}
		return remappedIndices, needRewrite
	}

	for _, s := range existingSupports {
		partIndex := int64(0)
		if p := s.Get("segment.partIndex"); p.Exists() {
			partIndex = p.Int()
		}
		startByte := s.Get("segment.startIndex").Int()
		endByte := s.Get("segment.endIndex").Int()

		remappedIndices, needRewrite := remapSupportIndices(s.Get("groundingChunkIndices").Array(), true)
		key := supportKey(partIndex, startByte, endByte, remappedIndices)
		if seenSupports[key] {
			continue
		}
		seenSupports[key] = true

		rawSupport := s.Raw
		if needRewrite {
			indicesStrs := make([]string, len(remappedIndices))
			for i, v := range remappedIndices {
				indicesStrs[i] = strconv.FormatInt(v, 10)
			}
			rawSupport, _ = sjson.SetRaw(rawSupport, "groundingChunkIndices", "["+strings.Join(indicesStrs, ",")+"]")
		}
		mergedSupportsRaw = append(mergedSupportsRaw, rawSupport)
	}

	for _, s := range newSupports {
		partIndex := int64(0)
		if p := s.Get("segment.partIndex"); p.Exists() {
			partIndex = p.Int()
		}
		startByte := s.Get("segment.startIndex").Int()
		endByte := s.Get("segment.endIndex").Int()

		remappedIndices, needRewrite := remapSupportIndices(s.Get("groundingChunkIndices").Array(), false)
		key := supportKey(partIndex, startByte, endByte, remappedIndices)
		if seenSupports[key] {
			continue
		}
		seenSupports[key] = true

		rawSupport := s.Raw
		if needRewrite {
			indicesStrs := make([]string, len(remappedIndices))
			for i, v := range remappedIndices {
				indicesStrs[i] = strconv.FormatInt(v, 10)
			}
			rawSupport, _ = sjson.SetRaw(rawSupport, "groundingChunkIndices", "["+strings.Join(indicesStrs, ",")+"]")
		}
		mergedSupportsRaw = append(mergedSupportsRaw, rawSupport)
	}

	if len(mergedSupportsRaw) > 0 {
		supportsJSON := "[" + strings.Join(mergedSupportsRaw, ",") + "]"
		merged, _ = sjson.SetRaw(merged, "groundingSupports", supportsJSON)
	}

	// 4. Preserve/update searchEntryPoint if present
	if sep := newGM.Get("searchEntryPoint"); sep.Exists() {
		merged, _ = sjson.SetRaw(merged, "searchEntryPoint", sep.Raw)
	}

	// 5. Preserve/update retrievalQueries if present
	if rq := newGM.Get("retrievalQueries"); rq.Exists() && !existingGM.Get("retrievalQueries").Exists() {
		merged, _ = sjson.SetRaw(merged, "retrievalQueries", rq.Raw)
	}

	return gjson.Parse(merged)
}

// MergeCitationAnnotations merges two slices of URL citation annotations,
// deduplicating by URL and rune offsets, and updating titles if the existing citation had an empty title.
func MergeCitationAnnotations(existing, late [][]byte) [][]byte {
	if len(existing) == 0 {
		return late
	}
	if len(late) == 0 {
		return existing
	}
	result := make([][]byte, 0, len(existing)+len(late))
	keyToIndex := make(map[string]int)
	for _, a := range existing {
		url := gjson.GetBytes(a, "url").String()
		start := gjson.GetBytes(a, "start_index").Int()
		end := gjson.GetBytes(a, "end_index").Int()
		key := fmt.Sprintf("%s:%d:%d", url, start, end)
		if _, exists := keyToIndex[key]; !exists {
			keyToIndex[key] = len(result)
			result = append(result, a)
		}
	}
	for _, a := range late {
		url := gjson.GetBytes(a, "url").String()
		start := gjson.GetBytes(a, "start_index").Int()
		end := gjson.GetBytes(a, "end_index").Int()
		key := fmt.Sprintf("%s:%d:%d", url, start, end)
		if idx, exists := keyToIndex[key]; exists {
			if gjson.GetBytes(result[idx], "title").String() == "" {
				if lateTitle := gjson.GetBytes(a, "title").String(); lateTitle != "" {
					result[idx] = a
				}
			}
		} else {
			keyToIndex[key] = len(result)
			result = append(result, a)
		}
	}
	return result
}

// byteOffsetToRuneOffset converts a UTF-8 byte offset in text to a 0-based rune (character) offset.
func byteOffsetToRuneOffset(text string, byteOffset int64) int64 {
	if byteOffset <= 0 {
		return 0
	}
	textBytes := []byte(text)
	if byteOffset >= int64(len(textBytes)) {
		return int64(utf8.RuneCount(textBytes))
	}
	return int64(utf8.RuneCount(textBytes[:byteOffset]))
}

// GeminiPartMapping records the location of a Gemini content part within an OpenAI response message.
type GeminiPartMapping struct {
	PartIndex      int
	MessageIndex   int
	StartRuneInMsg int64
	PartText       string
}

// MessageRuneRange represents a citation range within a specific message.
type MessageRuneRange struct {
	MessageIndex int
	StartIndex   int64
	EndIndex     int64
}

// mapByteOffsetsToRuneRanges translates startByte and endByte across a slice of GeminiPartMapping
// using cumulative byte offsets, returning a slice of MessageRuneRange covering all affected messages.
func mapByteOffsetsToRuneRanges(mappings []GeminiPartMapping, startByte, endByte int64) []MessageRuneRange {
	if len(mappings) == 0 {
		return nil
	}
	if startByte < 0 {
		startByte = 0
	}
	if startByte >= endByte {
		return nil
	}

	type mappingSpan struct {
		m        GeminiPartMapping
		cumStart int64
		cumEnd   int64
	}
	spans := make([]mappingSpan, len(mappings))
	var cumBytes int64
	for i, m := range mappings {
		pBytes := int64(len([]byte(m.PartText)))
		spans[i] = mappingSpan{
			m:        m,
			cumStart: cumBytes,
			cumEnd:   cumBytes + pBytes,
		}
		cumBytes += pBytes
	}

	if startByte >= cumBytes {
		return nil
	}
	if endByte > cumBytes {
		endByte = cumBytes
	}

	var ranges []MessageRuneRange
	for _, span := range spans {
		overlapStart := startByte
		if span.cumStart > overlapStart {
			overlapStart = span.cumStart
		}
		overlapEnd := endByte
		if span.cumEnd < overlapEnd {
			overlapEnd = span.cumEnd
		}
		if overlapStart >= overlapEnd {
			continue
		}

		relStartByte := overlapStart - span.cumStart
		relEndByte := overlapEnd - span.cumStart

		partStartRune := byteOffsetToRuneOffset(span.m.PartText, relStartByte)
		partEndRune := byteOffsetToRuneOffset(span.m.PartText, relEndByte)
		if partEndRune <= partStartRune || partStartRune < 0 {
			continue
		}

		startRune := span.m.StartRuneInMsg + partStartRune
		endRune := span.m.StartRuneInMsg + partEndRune

		n := len(ranges)
		if n > 0 && ranges[n-1].MessageIndex == span.m.MessageIndex && ranges[n-1].EndIndex == startRune {
			ranges[n-1].EndIndex = endRune
		} else {
			ranges = append(ranges, MessageRuneRange{
				MessageIndex: span.m.MessageIndex,
				StartIndex:   startRune,
				EndIndex:     endRune,
			})
		}
	}
	return ranges
}

// mapByteOffsetsToRuneRange translates startByte and endByte across a slice of GeminiPartMapping
// using cumulative byte offsets, returning targetMsgIndex, startRune, endRune of the first matched range.
func mapByteOffsetsToRuneRange(mappings []GeminiPartMapping, startByte, endByte int64) (int, int64, int64, bool) {
	ranges := mapByteOffsetsToRuneRanges(mappings, startByte, endByte)
	if len(ranges) == 0 {
		return 0, 0, 0, false
	}
	return ranges[0].MessageIndex, ranges[0].StartIndex, ranges[0].EndIndex, true
}

// BuildResponsesURLCitationsForMessages extracts url_citation annotations from groundingMetadata,
// correctly translating part-level byte offsets to message-level rune offsets based on GeminiPartMapping.
// It returns a map from MessageIndex to the slice of citation items for that message.
func BuildResponsesURLCitationsForMessages(groundingMetadata gjson.Result, mappings []GeminiPartMapping, messageTexts []string) map[int][][]byte {
	chunks := groundingMetadata.Get("groundingChunks").Array()
	supports := groundingMetadata.Get("groundingSupports").Array()
	if len(supports) == 0 || len(chunks) == 0 {
		return nil
	}

	// Coalesce adjacent mappings that share the same PartIndex and MessageIndex
	var coalescedMappings []GeminiPartMapping
	for _, m := range mappings {
		n := len(coalescedMappings)
		if n > 0 && coalescedMappings[n-1].PartIndex == m.PartIndex && coalescedMappings[n-1].MessageIndex == m.MessageIndex {
			coalescedMappings[n-1].PartText += m.PartText
		} else {
			coalescedMappings = append(coalescedMappings, m)
		}
	}
	mappings = coalescedMappings

	result := make(map[int][][]byte)
	seen := make(map[string]bool)

	for _, support := range supports {
		segment := support.Get("segment")
		hasPartIndex := segment.Get("partIndex").Exists()
		partIndex := int(segment.Get("partIndex").Int())
		startByte := segment.Get("startIndex").Int()
		endByte := segment.Get("endIndex").Int()

		var ranges []MessageRuneRange

		if hasPartIndex {
			var partMappings []GeminiPartMapping
			for _, m := range mappings {
				if m.PartIndex == partIndex {
					partMappings = append(partMappings, m)
				}
			}
			if len(partMappings) == 0 && partIndex == 0 && len(mappings) == 1 {
				partMappings = mappings
			}
			ranges = mapByteOffsetsToRuneRanges(partMappings, startByte, endByte)
		} else {
			if len(mappings) > 0 {
				ranges = mapByteOffsetsToRuneRanges(mappings, startByte, endByte)
			} else if len(messageTexts) > 0 {
				msgText := messageTexts[0]
				startRune := byteOffsetToRuneOffset(msgText, startByte)
				endRune := byteOffsetToRuneOffset(msgText, endByte)
				if endRune > startRune && startRune >= 0 {
					ranges = []MessageRuneRange{
						{
							MessageIndex: 0,
							StartIndex:   startRune,
							EndIndex:     endRune,
						},
					}
				}
			}
		}

		if len(ranges) == 0 {
			continue
		}

		indices := support.Get("groundingChunkIndices").Array()
		for _, idxResult := range indices {
			idx := int(idxResult.Int())
			if idx < 0 || idx >= len(chunks) {
				continue
			}
			chunk := chunks[idx]
			uri := strings.TrimSpace(chunk.Get("web.uri").String())
			title := strings.TrimSpace(chunk.Get("web.title").String())
			if uri == "" {
				continue
			}
			for _, r := range ranges {
				key := fmt.Sprintf("%d:%s:%d:%d", r.MessageIndex, uri, r.StartIndex, r.EndIndex)
				if seen[key] {
					continue
				}
				seen[key] = true

				cite := []byte(`{"type":"url_citation","url":"","title":"","start_index":0,"end_index":0}`)
				cite, _ = sjson.SetBytes(cite, "url", uri)
				cite, _ = sjson.SetBytes(cite, "title", title)
				cite, _ = sjson.SetBytes(cite, "start_index", r.StartIndex)
				cite, _ = sjson.SetBytes(cite, "end_index", r.EndIndex)
				result[r.MessageIndex] = append(result[r.MessageIndex], cite)
			}
		}
	}
	return result
}

// BuildResponsesURLCitations extracts url_citation annotations from groundingMetadata groundingSupports.
// Optional text parameter enables converting UTF-8 byte offsets to Unicode character (rune) offsets.
func BuildResponsesURLCitations(groundingMetadata gjson.Result, text ...string) [][]byte {
	var fullText string
	if len(text) > 0 {
		fullText = text[0]
	}
	var mappings []GeminiPartMapping
	if fullText != "" {
		mappings = []GeminiPartMapping{
			{
				PartIndex:      0,
				MessageIndex:   0,
				StartRuneInMsg: 0,
				PartText:       fullText,
			},
		}
	}
	resMap := BuildResponsesURLCitationsForMessages(groundingMetadata, mappings, text)
	if c, ok := resMap[0]; ok && len(c) > 0 {
		return c
	}
	for _, c := range resMap {
		if len(c) > 0 {
			return c
		}
	}
	return nil
}
