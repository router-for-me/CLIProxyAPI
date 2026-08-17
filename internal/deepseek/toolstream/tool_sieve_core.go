package toolstream

import "github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/toolcall"

func ProcessChunk(state *State, chunk string, toolNames []string) []Event {
	if state == nil {
		return nil
	}
	if chunk != "" {
		state.pending.WriteString(chunk)
	}
	events := make([]Event, 0, 2)
	if len(state.pendingToolCalls) > 0 {
		events = append(events, Event{ToolCalls: state.pendingToolCalls})
		state.pendingToolRaw = ""
		state.pendingToolCalls = nil
	}

	for {
		if state.capturing {
			if state.pending.Len() > 0 {
				state.capture.WriteString(state.pending.String())
				state.pending.Reset()
			}
			prefix, calls, suffix, ready := consumeToolCapture(state, toolNames)
			if !ready {
				break
			}
			captured := state.capture.String()
			state.capture.Reset()
			state.capturing = false
			state.resetIncrementalToolState()
			if len(calls) > 0 {
				if prefix != "" {
					state.noteText(prefix)
					events = append(events, Event{Content: prefix})
				}
				if suffix != "" {
					state.pending.WriteString(suffix)
				}
				_ = captured
				state.pendingToolCalls = calls
				continue
			}
			if prefix != "" {
				state.noteText(prefix)
				events = append(events, Event{Content: prefix})
			}
			if suffix != "" {
				state.pending.WriteString(suffix)
			}
			continue
		}

		pending := state.pending.String()
		if pending == "" {
			break
		}
		start := findToolSegmentStart(state, pending)
		if start == holdToolSegmentStart {
			break
		}
		if start >= 0 {
			prefix := pending[:start]
			if prefix != "" {
				resetMarkdownSpan := shouldResetUnclosedMarkdownPrefix(state, prefix, pending[start:])
				state.noteText(prefix)
				if resetMarkdownSpan {
					state.markdownCodeSpanTicks = 0
				}
				events = append(events, Event{Content: prefix})
			}
			state.pending.Reset()
			state.capture.WriteString(pending[start:])
			state.capturing = true
			state.resetIncrementalToolState()
			continue
		}

		safe, hold := splitSafeContentForToolDetection(state, pending)
		if safe == "" {
			break
		}
		state.pending.Reset()
		state.pending.WriteString(hold)
		state.noteText(safe)
		events = append(events, Event{Content: safe})
	}

	return events
}

func Flush(state *State, toolNames []string) []Event {
	if state == nil {
		return nil
	}
	events := ProcessChunk(state, "", toolNames)
	if state.pending.Len() > 0 && state.markdownCodeSpanTicks > 0 {
		// At end of stream, an unmatched backtick is literal Markdown text.
		// Re-scan pending content so a real tool call after that stray
		// backtick is not permanently hidden by inline-code state.
		state.markdownCodeSpanTicks = 0
		events = append(events, ProcessChunk(state, "", toolNames)...)
	}
	if len(state.pendingToolCalls) > 0 {
		events = append(events, Event{ToolCalls: state.pendingToolCalls})
		state.pendingToolRaw = ""
		state.pendingToolCalls = nil
	}
	if state.capturing {
		consumedPrefix, consumedCalls, consumedSuffix, ready := consumeToolCapture(state, toolNames)
		if ready {
			if consumedPrefix != "" {
				state.noteText(consumedPrefix)
				events = append(events, Event{Content: consumedPrefix})
			}
			if len(consumedCalls) > 0 {
				events = append(events, Event{ToolCalls: consumedCalls})
			}
			if consumedSuffix != "" {
				state.noteText(consumedSuffix)
				events = append(events, Event{Content: consumedSuffix})
			}
		} else {
			content := state.capture.String()
			if content != "" {
				recovered := toolcall.SanitizeLooseCDATA(content)
				if recovered != content {
					if prefix, calls, suffix, recoveredReady := consumeXMLToolCapture(recovered, toolNames); recoveredReady && len(calls) > 0 {
						if prefix != "" {
							state.noteText(prefix)
							events = append(events, Event{Content: prefix})
						}
						events = append(events, Event{ToolCalls: calls})
						if suffix != "" {
							state.noteText(suffix)
							events = append(events, Event{Content: suffix})
						}
					} else {
						// If capture never resolved into a real tool call, release
						// the buffered text instead of swallowing it.
						state.noteText(content)
						events = append(events, Event{Content: content})
					}
				} else {
					// If capture never resolved into a real tool call, release the
					// buffered text instead of swallowing it.
					state.noteText(content)
					events = append(events, Event{Content: content})
				}
			}
		}
		state.capture.Reset()
		state.capturing = false
		state.resetIncrementalToolState()
	}
	if state.pending.Len() > 0 {
		content := state.pending.String()
		// If pending never resolved into a real tool call, release it as text.
		state.noteText(content)
		events = append(events, Event{Content: content})
		state.pending.Reset()
	}
	return events
}

func splitSafeContentForToolDetection(state *State, s string) (safe, hold string) {
	if s == "" {
		return "", ""
	}
	if xmlIdx := findPartialXMLToolTagStart(s); xmlIdx >= 0 {
		if insideCodeFenceWithState(state, s[:xmlIdx]) {
			return s, ""
		}
		markdown := markdownCodeSpanStateAt(state, s[:xmlIdx])
		if markdown.ticks > 0 {
			if markdownCodeSpanCloses(s[xmlIdx:], markdown.ticks) {
				return s, ""
			}
			if markdown.fromPrior {
				return "", s
			}
		}
		if xmlIdx > 0 {
			return s[:xmlIdx], s[xmlIdx:]
		}
		return "", s
	}
	return s, ""
}

const holdToolSegmentStart = -2

func findToolSegmentStart(state *State, s string) int {
	if s == "" {
		return -1
	}
	offset := 0
	for {
		tag, ok := toolcall.FindToolMarkupTagOutsideIgnored(s, offset)
		if !ok {
			return -1
		}
		start := includeDuplicateLeadingLessThan(s, tag.Start)
		if insideCodeFenceWithState(state, s[:start]) {
			offset = tag.End + 1
			continue
		}
		markdown := markdownCodeSpanStateAt(state, s[:start])
		if markdown.ticks == 0 {
			return start
		}
		if markdownCodeSpanCloses(s[start:], markdown.ticks) {
			offset = tag.End + 1
			continue
		}
		if markdown.fromPrior {
			return holdToolSegmentStart
		}
		return start
	}
}

type markdownCodeSpanScan struct {
	ticks     int
	fromPrior bool
}

func markdownCodeSpanStateAt(state *State, text string) markdownCodeSpanScan {
	ticks := 0
	fromPrior := false
	if state != nil && state.markdownCodeSpanTicks > 0 {
		ticks = state.markdownCodeSpanTicks
		fromPrior = true
	}
	for i := 0; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		run := countBacktickRun(text, i)
		if ticks == 0 {
			if run >= 3 && atMarkdownFenceLineStart(text, i) {
				i += run
				continue
			}
			if state != nil && insideCodeFenceWithState(state, text[:i]) {
				i += run
				continue
			}
			ticks = run
			fromPrior = false
		} else if run == ticks {
			ticks = 0
			fromPrior = false
		}
		i += run
	}
	return markdownCodeSpanScan{ticks: ticks, fromPrior: fromPrior}
}

func markdownCodeSpanCloses(text string, ticks int) bool {
	if ticks <= 0 {
		return false
	}
	for i := 0; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		run := countBacktickRun(text, i)
		if run == ticks {
			return true
		}
		i += run
	}
	return false
}

func shouldResetUnclosedMarkdownPrefix(state *State, prefix, suffix string) bool {
	markdown := markdownCodeSpanStateAt(state, prefix)
	return markdown.ticks > 0 && !markdown.fromPrior && !markdownCodeSpanCloses(suffix, markdown.ticks)
}

func includeDuplicateLeadingLessThan(s string, idx int) int {
	for idx > 0 && s[idx-1] == '<' {
		idx--
	}
	return idx
}

func consumeToolCapture(state *State, toolNames []string) (prefix string, calls []toolcall.ParsedToolCall, suffix string, ready bool) {
	captured := state.capture.String()
	if captured == "" {
		return "", nil, "", false
	}

	// XML tool call extraction only.
	if xmlPrefix, xmlCalls, xmlSuffix, xmlReady := consumeXMLToolCapture(captured, toolNames); xmlReady {
		return xmlPrefix, xmlCalls, xmlSuffix, true
	}
	// If XML tags are present but block is incomplete, keep buffering.
	if hasOpenXMLToolTag(captured) {
		return "", nil, "", false
	}
	if shouldKeepBareInvokeCapture(captured) {
		return "", nil, "", false
	}
	return captured, nil, "", true
}
