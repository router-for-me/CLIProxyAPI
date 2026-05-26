package executor

import (
	"bytes"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"
)

var codexEncoderCache sync.Map

func cachedTokenizerForCodexModel(model string) (tokenizer.Codec, error) {
	if enc, ok := codexEncoderCache.Load(model); ok {
		return enc.(tokenizer.Codec), nil
	}
	enc, err := tokenizerForCodexModel(model)
	if err != nil {
		return nil, err
	}
	actual, _ := codexEncoderCache.LoadOrStore(model, enc)
	return actual.(tokenizer.Codec), nil
}

type codexInputTurn struct {
	startIdx int
	tokens   int64
}

func codexInputTokenCeiling(modelID string) int {
	m := registry.LookupStaticModelInfo(modelID)
	if m == nil {
		return 0
	}
	if m.InputTokenLimit > 0 {
		return m.InputTokenLimit
	}
	if m.ContextLength > 0 && m.MaxCompletionTokens > 0 {
		return m.ContextLength - m.MaxCompletionTokens
	}
	return 0
}

func autoTrimEnabled(cfg *config.Config) bool {
	return cfg == nil || cfg.AutoTrimInput == nil || *cfg.AutoTrimInput
}

func trimCodexInputIfNeeded(cfg *config.Config, body []byte, baseModel string) []byte {
	if !autoTrimEnabled(cfg) {
		return body
	}
	ceiling := codexInputTokenCeiling(baseModel)
	if ceiling <= 0 {
		return body
	}
	if len(body) < ceiling {
		return body
	}

	enc, err := cachedTokenizerForCodexModel(baseModel)
	if err != nil {
		return body
	}

	root := gjson.ParseBytes(body)
	inputItems := root.Get("input")
	if !inputItems.IsArray() {
		return body
	}
	items := inputItems.Array()
	if len(items) <= 1 {
		return body
	}

	fixedTokens := countCodexFixedTokens(enc, root)
	turns := groupCodexInputTurns(items, enc)
	if len(turns) <= 1 {
		return body
	}

	var inputTokens int64
	for _, t := range turns {
		inputTokens += t.tokens
	}
	total := fixedTokens + inputTokens
	if total <= int64(ceiling) {
		return body
	}

	target := total - int64(ceiling)
	var shed int64
	dropCount := 0
	for dropCount < len(turns)-1 && shed < target {
		shed += turns[dropCount].tokens
		dropCount++
	}
	if dropCount == 0 {
		return body
	}

	firstKeep := turns[dropCount].startIdx

	keptCallIDs := make(map[string]struct{})
	for i := firstKeep; i < len(items); i++ {
		if items[i].Get("type").String() == "function_call" {
			if id := items[i].Get("call_id").String(); id != "" {
				keptCallIDs[id] = struct{}{}
			}
		}
	}

	var buf bytes.Buffer
	buf.WriteByte('[')
	first := true
	var orphaned int
	for i := firstKeep; i < len(items); i++ {
		if items[i].Get("type").String() == "function_call_output" {
			if id := items[i].Get("call_id").String(); id != "" {
				if _, ok := keptCallIDs[id]; !ok {
					orphaned++
					continue
				}
			}
		}
		if !first {
			buf.WriteByte(',')
		}
		buf.WriteString(items[i].Raw)
		first = false
	}
	buf.WriteByte(']')

	trimmed, err := sjson.SetRawBytes(body, "input", buf.Bytes())
	if err != nil {
		log.Warnf("codex input trim: failed to rebuild input array: %v", err)
		return body
	}
	if orphaned > 0 {
		log.Infof("codex input trimmed: dropped %d/%d turns (%d tokens, %d orphaned outputs), %d -> ~%d tokens (ceiling %d)",
			dropCount, len(turns), shed, orphaned, total, total-shed, ceiling)
	} else {
		log.Infof("codex input trimmed: dropped %d/%d turns (%d tokens), %d -> ~%d tokens (ceiling %d)",
			dropCount, len(turns), shed, total, total-shed, ceiling)
	}
	return trimmed
}

func countCodexFixedTokens(enc tokenizer.Codec, root gjson.Result) int64 {
	var segments []string
	if inst := strings.TrimSpace(root.Get("instructions").String()); inst != "" {
		segments = append(segments, inst)
	}
	if tools := root.Get("tools"); tools.IsArray() {
		for _, tool := range tools.Array() {
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				segments = append(segments, name)
			}
			if desc := strings.TrimSpace(tool.Get("description").String()); desc != "" {
				segments = append(segments, desc)
			}
			if params := tool.Get("parameters"); params.Exists() {
				val := params.Raw
				if params.Type == gjson.String {
					val = params.String()
				}
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					segments = append(segments, trimmed)
				}
			}
		}
	}
	if textFormat := root.Get("text.format"); textFormat.Exists() {
		if name := strings.TrimSpace(textFormat.Get("name").String()); name != "" {
			segments = append(segments, name)
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			val := schema.Raw
			if schema.Type == gjson.String {
				val = schema.String()
			}
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				segments = append(segments, trimmed)
			}
		}
	}
	if len(segments) == 0 {
		return 0
	}
	count, err := enc.Count(strings.Join(segments, "\n"))
	if err != nil {
		return 0
	}
	return int64(count)
}

func groupCodexInputTurns(items []gjson.Result, enc tokenizer.Codec) []codexInputTurn {
	var turns []codexInputTurn
	i := 0
	for i < len(items) {
		start := i
		if items[i].Get("type").String() == "function_call" {
			i++
			for i < len(items) && items[i].Get("type").String() == "function_call_output" {
				i++
			}
		} else {
			i++
		}

		var tokens int64
		for j := start; j < i; j++ {
			tokens += countSingleCodexItemTokens(enc, items[j])
		}
		turns = append(turns, codexInputTurn{startIdx: start, tokens: tokens})
	}
	return turns
}

func countSingleCodexItemTokens(enc tokenizer.Codec, item gjson.Result) int64 {
	var segments []string
	switch item.Get("type").String() {
	case "message":
		content := item.Get("content")
		if content.IsArray() {
			for _, part := range content.Array() {
				if text := strings.TrimSpace(part.Get("text").String()); text != "" {
					segments = append(segments, text)
				}
			}
		}
	case "function_call":
		if name := strings.TrimSpace(item.Get("name").String()); name != "" {
			segments = append(segments, name)
		}
		if args := strings.TrimSpace(item.Get("arguments").String()); args != "" {
			segments = append(segments, args)
		}
	case "function_call_output":
		if out := strings.TrimSpace(item.Get("output").String()); out != "" {
			segments = append(segments, out)
		}
	default:
		if text := strings.TrimSpace(item.Get("text").String()); text != "" {
			segments = append(segments, text)
		}
	}
	if len(segments) == 0 {
		return 0
	}
	text := strings.Join(segments, "\n")
	count, err := enc.Count(text)
	if err != nil {
		return int64(len(text) / 4)
	}
	return int64(count)
}
