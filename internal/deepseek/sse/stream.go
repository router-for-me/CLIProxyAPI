package sse

import (
	"bufio"
	"context"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	parsedLineBufferSize = 128
	lineReaderBufferSize = 64 * 1024
)

type AccumulateConfig struct {
	Enabled        bool
	MinChars       int
	MaxWait        time.Duration
	FlushOnFinish  bool
	WordBoundary   bool
	FlushOnNewline bool
}

var productionAccumulate = AccumulateConfig{
	Enabled:        true,
	MinChars:       16,
	MaxWait:        10 * time.Millisecond,
	FlushOnFinish:  true,
	WordBoundary:   false,
	FlushOnNewline: true,
}

func StartParsedLinePump(ctx context.Context, body io.Reader, thinkingEnabled bool, initialType string) (<-chan LineResult, <-chan error) {
	return startParsedLinePumpWithConfig(ctx, body, thinkingEnabled, initialType, productionAccumulate)
}

func startParsedLinePumpWithConfig(ctx context.Context, body io.Reader, thinkingEnabled bool, initialType string, cfg AccumulateConfig) (<-chan LineResult, <-chan error) {
	out := make(chan LineResult, parsedLineBufferSize)
	done := make(chan error, 1)

	go func() {
		defer close(out)

		reader := bufio.NewReaderSize(body, lineReaderBufferSize)
		currentType := initialType

		var pumpErr error

		var textBuffer strings.Builder
		var thinkingBuffer strings.Builder
		var toolDetectionThinkingBuffer strings.Builder
		var textPendingType string
		var thinkingPendingType string
		var anyFlushed bool
		var pendingResponseMessageID int

		scanCh := make(chan []byte, parsedLineBufferSize)
		scanDone := make(chan error, 1)

		go func() {
			for {
				line, err := reader.ReadBytes('\n')
				if len(line) > 0 {
					copied := append([]byte(nil), line...)
					select {
					case scanCh <- copied:
					case <-ctx.Done():
						close(scanCh)
						scanDone <- ctx.Err()
						return
					}
				}
				if err != nil {
					close(scanCh)
					if err == io.EOF {
						err = nil
					}
					scanDone <- err
					return
				}
			}
		}()

		maxWaitTimer := time.NewTimer(0)
		if !maxWaitTimer.Stop() {
			<-maxWaitTimer.C
		}
		maxWaitActive := false

		resetMaxWait := func() {
			if maxWaitActive {
				if !maxWaitTimer.Stop() {
					select {
					case <-maxWaitTimer.C:
					default:
					}
				}
			}
			maxWaitTimer.Reset(cfg.MaxWait)
			maxWaitActive = true
		}

		stopMaxWait := func() {
			if maxWaitActive {
				if !maxWaitTimer.Stop() {
					select {
					case <-maxWaitTimer.C:
					default:
					}
				}
				maxWaitActive = false
			}
		}

		defer stopMaxWait()

		shouldFlushImmediate := func(text string) bool {
			if cfg.FlushOnNewline && strings.ContainsAny(text, "\n\r") {
				return true
			}
			return false
		}

		hasBufferedData := func() bool {
			return textBuffer.Len() > 0 || thinkingBuffer.Len() > 0 || toolDetectionThinkingBuffer.Len() > 0
		}

		flushBuffer := func(force bool) {
			if !cfg.Enabled {
				return
			}

			textChars := utf8.RuneCountInString(textBuffer.String())
			thinkingChars := utf8.RuneCountInString(thinkingBuffer.String())

			shouldFlush := force ||
				!anyFlushed ||
				textChars >= cfg.MinChars ||
				(thinkingChars > 0 && textChars >= 50)

			if !shouldFlush {
				return
			}

			anyFlushed = true

			var parts []ContentPart

			if thinkingChars > 0 {
				parts = append(parts, ContentPart{Text: thinkingBuffer.String(), Type: thinkingPendingType})
				thinkingBuffer.Reset()
			}

			if textChars > 0 {
				parts = append(parts, ContentPart{Text: textBuffer.String(), Type: textPendingType})
				textBuffer.Reset()
			}

			if len(parts) > 0 || toolDetectionThinkingBuffer.Len() > 0 {
				var detectionParts []ContentPart
				if toolDetectionThinkingBuffer.Len() > 0 {
					detectionParts = append(detectionParts, ContentPart{Text: toolDetectionThinkingBuffer.String(), Type: "thinking"})
					toolDetectionThinkingBuffer.Reset()
				}

				result := LineResult{
					Parsed:                     true,
					Stop:                       false,
					Parts:                      parts,
					ToolDetectionThinkingParts: detectionParts,
					NextType:                   currentType,
					ResponseMessageID:          pendingResponseMessageID,
				}
				pendingResponseMessageID = 0
				select {
				case out <- result:
				case <-ctx.Done():
					pumpErr = ctx.Err()
					return
				}
			}

			if hasBufferedData() {
				resetMaxWait()
			} else {
				stopMaxWait()
			}
		}

		processLine := func(result LineResult) bool {
			currentType = result.NextType
			if result.ResponseMessageID > 0 {
				pendingResponseMessageID = result.ResponseMessageID
			}

			if result.Stop {
				if cfg.Enabled && cfg.FlushOnFinish {
					for _, p := range result.ToolDetectionThinkingParts {
						toolDetectionThinkingBuffer.WriteString(p.Text)
					}
					if textBuffer.Len() > 0 || len(result.Parts) > 0 || toolDetectionThinkingBuffer.Len() > 0 {
						for _, p := range result.Parts {
							if p.Type == "thinking" {
								thinkingBuffer.WriteString(p.Text)
								thinkingPendingType = "thinking"
							} else {
								textBuffer.WriteString(p.Text)
								textPendingType = p.Type
							}
						}
						flushBuffer(true)
					}
				} else if !cfg.Enabled {
					var filteredParts []ContentPart
					for _, p := range result.Parts {
						if p.Type == "thinking" && !thinkingEnabled {
							continue
						}
						filteredParts = append(filteredParts, p)
					}
					result.Parts = filteredParts
				}
				if result.ErrorMessage != "" || result.ContentFilter {
					select {
					case out <- result:
					case <-ctx.Done():
						pumpErr = ctx.Err()
						return false
					}
				} else {
					stopResult := LineResult{
						Parsed:            true,
						Stop:              true,
						NextType:          currentType,
						ResponseMessageID: pendingResponseMessageID,
					}
					pendingResponseMessageID = 0
					select {
					case out <- stopResult:
					case <-ctx.Done():
						pumpErr = ctx.Err()
						return false
					}
				}
				return true
			}

			if !result.Parsed {
				return true
			}

			if cfg.Enabled {
				for _, p := range result.ToolDetectionThinkingParts {
					toolDetectionThinkingBuffer.WriteString(p.Text)
				}
				for _, p := range result.Parts {
					if p.Type == "thinking" {
						if textBuffer.Len() > 0 {
							flushBuffer(true)
						}
						thinkingBuffer.WriteString(p.Text)
						thinkingPendingType = "thinking"
					} else {
						textBuffer.WriteString(p.Text)
						textPendingType = p.Type
						if shouldFlushImmediate(p.Text) {
							flushBuffer(true)
						}
					}
				}
				if utf8.RuneCountInString(textBuffer.String()) >= cfg.MinChars {
					flushBuffer(false)
				}
				if hasBufferedData() && !maxWaitActive {
					resetMaxWait()
				}
			} else {
				var parts []ContentPart
				for _, p := range result.Parts {
					if p.Type == "thinking" && !thinkingEnabled {
						continue
					}
					parts = append(parts, p)
				}
				if len(parts) > 0 || len(result.ToolDetectionThinkingParts) > 0 {
					filteredResult := LineResult{
						Parsed:                     true,
						Stop:                       false,
						Parts:                      parts,
						ToolDetectionThinkingParts: result.ToolDetectionThinkingParts,
						NextType:                   currentType,
					}
					select {
					case out <- filteredResult:
					case <-ctx.Done():
						pumpErr = ctx.Err()
						return false
					}
				}
			}
			return true
		}

		for {
			select {
			case <-ctx.Done():
				pumpErr = ctx.Err()
				goto done

			case line, ok := <-scanCh:
				if !ok {
					scanCh = nil
					err := <-scanDone
					if err != nil {
						pumpErr = err
					}
					goto done
				}
				result := ParseDeepSeekContentLine(line, thinkingEnabled, currentType)
				if !processLine(result) {
					goto done
				}

			case err, ok := <-scanDone:
				if !ok || scanCh == nil {
					goto done
				}
				if err != nil {
					pumpErr = err
				}
				for line := range scanCh {
					result := ParseDeepSeekContentLine(line, thinkingEnabled, currentType)
					if !processLine(result) {
						goto done
					}
				}
				goto done

			case <-maxWaitTimer.C:
				maxWaitActive = false
				if hasBufferedData() {
					flushBuffer(true)
				}
			}
		}

	done:
		stopMaxWait()
		if cfg.Enabled {
			flushBuffer(true)
		}

		if pumpErr != nil {
			done <- pumpErr
		} else {
			done <- nil
		}
	}()
	return out, done
}
