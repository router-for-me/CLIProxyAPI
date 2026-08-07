package loguploader

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	metadataReadLimit = 1 << 20

	providerCodex  = "codex"
	providerClaude = "fable5"
	providerGrok   = "grok45"

	archiveNameLabel            = "codex56sol"
	claudeArchiveNameLabel      = "fable5"
	grokArchiveNameLabel        = "grok45"
	archiveNamingPolicy         = "provider-jsonl-size-v2"
	legacyArchiveNamingPolicy   = "codex56sol-jsonl-size-v1"
	legacyArchiveNameLabel      = "all-models"
	legacyAllModelsNamingPolicy = "all-models-jsonl-size-v1"
)

var (
	fileTimePattern  = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})T(\d{2})(\d{2})(\d{2})`)
	modelPattern     = regexp.MustCompile(`(?i)"model"\s*:\s*"([^"\\]+)"`)
	timestampPattern = regexp.MustCompile(`(?m)^Timestamp:\s*([^\r\n]+)`)
)

type sourceLog struct {
	Path        string
	Relative    string
	KeyName     string
	Model       string
	Provider    string
	Timestamp   time.Time
	ArchiveHour time.Time
	Size        int64
	ModTime     time.Time
	Fingerprint string
	SHA256      string
}

type jsonlRecordHeader struct {
	SchemaVersion   int       `json:"schema_version"`
	KeyName         string    `json:"key_name"`
	Model           string    `json:"model"`
	SourceFile      string    `json:"source_file"`
	SourceSize      int64     `json:"source_size_bytes"`
	Timestamp       time.Time `json:"timestamp"`
	SecretsRedacted bool      `json:"sensitive_headers_redacted"`
}

func inspectSourceLog(root, path string, info os.FileInfo, location *time.Location) (sourceLog, error) {
	relative, errRelative := filepath.Rel(root, path)
	if errRelative != nil {
		return sourceLog{}, fmt.Errorf("resolve relative log path: %w", errRelative)
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 2 {
		return sourceLog{}, fmt.Errorf("log is not under a key_name directory: %s", relative)
	}

	file, errOpen := os.Open(path)
	if errOpen != nil {
		return sourceLog{}, fmt.Errorf("open source log: %w", errOpen)
	}
	prefix, errRead := io.ReadAll(io.LimitReader(file, metadataReadLimit))
	if errClose := file.Close(); errClose != nil && errRead == nil {
		errRead = errClose
	}
	if errRead != nil {
		return sourceLog{}, fmt.Errorf("read source log metadata: %w", errRead)
	}

	timestamp := extractTimestamp(prefix, filepath.Base(path), location, info.ModTime())
	archiveHour := info.ModTime().In(location).Truncate(time.Hour)
	model := "unknown"
	if match := modelPattern.FindSubmatch(prefix); len(match) == 2 {
		model = string(match[1])
	}
	return sourceLog{
		Path:        path,
		Relative:    filepath.ToSlash(relative),
		KeyName:     parts[0],
		Model:       model,
		Provider:    classifyProvider(model),
		Timestamp:   timestamp,
		ArchiveHour: archiveHour,
		Size:        info.Size(),
		ModTime:     info.ModTime(),
		Fingerprint: fmt.Sprintf("%s|%d|%d", filepath.ToSlash(relative), info.Size(), info.ModTime().UnixNano()),
	}, nil
}

func extractTimestamp(prefix []byte, filename string, location *time.Location, fallback time.Time) time.Time {
	if match := timestampPattern.FindSubmatch(prefix); len(match) == 2 {
		if parsed, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(match[1]))); errParse == nil {
			return parsed.In(location)
		}
	}
	if match := fileTimePattern.FindStringSubmatch(filename); len(match) == 5 {
		value := match[1] + "T" + match[2] + ":" + match[3] + ":" + match[4]
		if parsed, errParse := time.ParseInLocation("2006-01-02T15:04:05", value, location); errParse == nil {
			return parsed
		}
	}
	return fallback.In(location)
}

func writeJSONLRecord(dst io.Writer, source sourceLog) (int64, error) {
	written, _, errWrite := writeJSONLRecordWithHash(dst, source)
	return written, errWrite
}

func writeJSONLRecordWithHash(dst io.Writer, source sourceLog) (int64, string, error) {
	if source.Provider == providerCodex {
		return writeCodexNormalizedRecord(dst, source)
	}
	header := jsonlRecordHeader{
		SchemaVersion:   1,
		KeyName:         source.KeyName,
		Model:           source.Model,
		SourceFile:      source.Relative,
		SourceSize:      source.Size,
		Timestamp:       source.Timestamp,
		SecretsRedacted: true,
	}
	prefix, errMarshal := json.Marshal(header)
	if errMarshal != nil {
		return 0, "", fmt.Errorf("marshal JSONL header: %w", errMarshal)
	}
	if len(prefix) == 0 || prefix[len(prefix)-1] != '}' {
		return 0, "", fmt.Errorf("invalid JSONL header")
	}

	counter := &countingWriter{writer: dst}
	if _, errWrite := counter.Write(prefix[:len(prefix)-1]); errWrite != nil {
		return counter.count, "", errWrite
	}
	if _, errWrite := io.WriteString(counter, `,"raw_log":"`); errWrite != nil {
		return counter.count, "", errWrite
	}

	file, errOpen := os.Open(source.Path)
	if errOpen != nil {
		return counter.count, "", fmt.Errorf("open source log for JSONL: %w", errOpen)
	}
	hash := sha256.New()
	errEscape := writeRedactedEscapedJSONString(counter, io.TeeReader(file, hash))
	errClose := file.Close()
	if errEscape != nil {
		return counter.count, "", fmt.Errorf("encode raw log as JSON string: %w", errEscape)
	}
	if errClose != nil {
		return counter.count, "", fmt.Errorf("close source log: %w", errClose)
	}
	info, errStat := os.Stat(source.Path)
	if errStat != nil {
		return counter.count, "", fmt.Errorf("verify source log after reading: %w", errStat)
	}
	if info.Size() != source.Size || !info.ModTime().Equal(source.ModTime) {
		return counter.count, "", fmt.Errorf("source log changed while it was being converted: %s", source.Relative)
	}
	if _, errWrite := io.WriteString(counter, `"}`+"\n"); errWrite != nil {
		return counter.count, "", errWrite
	}
	return counter.count, fmt.Sprintf("%x", hash.Sum(nil)), nil
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(data []byte) (int, error) {
	n, errWrite := w.writer.Write(data)
	w.count += int64(n)
	return n, errWrite
}

func writeRedactedEscapedJSONString(dst io.Writer, src io.Reader) error {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		errRedact := redactSensitiveHeaders(writer, src)
		_ = writer.CloseWithError(errRedact)
		done <- errRedact
	}()

	errEscape := writeEscapedJSONString(dst, reader)
	if errEscape != nil {
		_ = reader.CloseWithError(errEscape)
	} else {
		_ = reader.Close()
	}
	errRedact := <-done
	return errors.Join(errEscape, errRedact)
}

func redactSensitiveHeaders(dst io.Writer, src io.Reader) error {
	reader := bufio.NewReaderSize(src, 64<<10)
	writer := bufio.NewWriterSize(dst, 64<<10)
	defer func() {
		_ = writer.Flush()
	}()

	const maxHeaderNameBytes = 128
	const (
		linePrefix = iota
		lineCopy
		lineRedact
	)
	prefix := make([]byte, 0, maxHeaderNameBytes)
	lineState := linePrefix
	buffer := make([]byte, 64<<10)
	for {
		n, errRead := reader.Read(buffer)
		data := buffer[:n]
		for index := 0; index < len(data); {
			switch lineState {
			case linePrefix:
				value := data[index]
				index++
				prefix = append(prefix, value)
				switch {
				case value == '\n':
					if _, errWrite := writer.Write(prefix); errWrite != nil {
						return errWrite
					}
					prefix = prefix[:0]
				case value == ':':
					if sensitiveHeaderName(string(prefix[:len(prefix)-1])) {
						if _, errWrite := writer.Write(prefix); errWrite != nil {
							return errWrite
						}
						if _, errWrite := writer.WriteString(" [REDACTED]"); errWrite != nil {
							return errWrite
						}
						lineState = lineRedact
					} else {
						if _, errWrite := writer.Write(prefix); errWrite != nil {
							return errWrite
						}
						lineState = lineCopy
					}
					prefix = prefix[:0]
				case len(prefix) >= maxHeaderNameBytes:
					if _, errWrite := writer.Write(prefix); errWrite != nil {
						return errWrite
					}
					prefix = prefix[:0]
					lineState = lineCopy
				}
			case lineCopy:
				newline := bytes.IndexByte(data[index:], '\n')
				if newline < 0 {
					if _, errWrite := writer.Write(data[index:]); errWrite != nil {
						return errWrite
					}
					index = len(data)
					continue
				}
				end := index + newline + 1
				if _, errWrite := writer.Write(data[index:end]); errWrite != nil {
					return errWrite
				}
				index = end
				lineState = linePrefix
			case lineRedact:
				newline := bytes.IndexByte(data[index:], '\n')
				if newline < 0 {
					index = len(data)
					continue
				}
				if errWrite := writer.WriteByte('\n'); errWrite != nil {
					return errWrite
				}
				index += newline + 1
				lineState = linePrefix
			}
		}
		if errRead != nil {
			if errRead != io.EOF {
				return errRead
			}
			if lineState == linePrefix && len(prefix) > 0 {
				if _, errWrite := writer.Write(prefix); errWrite != nil {
					return errWrite
				}
			}
			return writer.Flush()
		}
	}
}

func sensitiveHeaderName(value string) bool {
	lowerName := strings.ToLower(strings.TrimSpace(value))
	if lowerName == "cookie" || lowerName == "set-cookie" {
		return true
	}
	return strings.Contains(lowerName, "authorization") ||
		strings.Contains(lowerName, "api-key") ||
		strings.Contains(lowerName, "apikey") ||
		strings.Contains(lowerName, "token") ||
		strings.Contains(lowerName, "secret")
}

func writeEscapedJSONString(dst io.Writer, src io.Reader) error {
	writer := bufio.NewWriterSize(dst, 64<<10)
	buffer := make([]byte, 64<<10)
	pending := make([]byte, 0, utf8.UTFMax)

	for {
		n, errRead := src.Read(buffer)
		data := make([]byte, 0, len(pending)+n)
		data = append(data, pending...)
		data = append(data, buffer[:n]...)
		pending = pending[:0]
		start := 0
		for index := 0; index < len(data); {
			value := data[index]
			if value < utf8.RuneSelf {
				if value >= 0x20 && value != '\\' && value != '"' {
					index++
					continue
				}
				if _, errWrite := writer.Write(data[start:index]); errWrite != nil {
					return errWrite
				}
				var escaped string
				switch value {
				case '\\':
					escaped = `\\`
				case '"':
					escaped = `\"`
				case '\b':
					escaped = `\b`
				case '\f':
					escaped = `\f`
				case '\n':
					escaped = `\n`
				case '\r':
					escaped = `\r`
				case '\t':
					escaped = `\t`
				default:
					const hex = "0123456789abcdef"
					escaped = string([]byte{'\\', 'u', '0', '0', hex[value>>4], hex[value&0x0f]})
				}
				if _, errWrite := writer.WriteString(escaped); errWrite != nil {
					return errWrite
				}
				index++
				start = index
				continue
			}

			if !utf8.FullRune(data[index:]) && errRead == nil {
				if _, errWrite := writer.Write(data[start:index]); errWrite != nil {
					return errWrite
				}
				pending = append(pending, data[index:]...)
				start = len(data)
				break
			}
			r, size := utf8.DecodeRune(data[index:])
			if r == utf8.RuneError && size == 1 {
				if _, errWrite := writer.Write(data[start:index]); errWrite != nil {
					return errWrite
				}
				if _, errWrite := writer.WriteString(`\ufffd`); errWrite != nil {
					return errWrite
				}
				index++
				start = index
				continue
			}
			if r == '\u2028' || r == '\u2029' {
				if _, errWrite := writer.Write(data[start:index]); errWrite != nil {
					return errWrite
				}
				if _, errWrite := fmt.Fprintf(writer, `\u%04x`, r); errWrite != nil {
					return errWrite
				}
				index += size
				start = index
				continue
			}
			index += size
		}
		if _, errWrite := writer.Write(data[start:]); errWrite != nil {
			return errWrite
		}
		if errRead != nil {
			if errRead == io.EOF {
				return writer.Flush()
			}
			return errRead
		}
	}
}

func sanitizeName(value, fallback string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return fallback
	}
	return strings.ToLower(builder.String())
}

func classifyProvider(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	// Clients may send vendor-prefixed names such as "xai/grok-4.5".
	if idx := strings.LastIndex(lower, "/"); idx >= 0 && idx+1 < len(lower) {
		lower = lower[idx+1:]
	}
	if strings.HasPrefix(lower, "claude-") && !strings.HasPrefix(lower, "claude-fable-5-dd-") {
		return providerClaude
	}
	if strings.HasPrefix(lower, "grok-") {
		return providerGrok
	}
	return providerCodex
}

func archiveNameLabelForProvider(provider string) string {
	switch provider {
	case providerClaude:
		return claudeArchiveNameLabel
	case providerGrok:
		return grokArchiveNameLabel
	default:
		return archiveNameLabel
	}
}

func makeArchiveFilename(hour time.Time, provider string, size int64) string {
	return fmt.Sprintf("%s-%s-%s.jsonl.zst", hour.Format("2006-01-02-15"), archiveNameLabelForProvider(provider), humanSize(size))
}

func humanSize(size int64) string {
	units := []struct {
		bytes  int64
		suffix string
	}{{1 << 40, "T"}, {1 << 30, "G"}, {1 << 20, "M"}, {1 << 10, "K"}}
	for _, unit := range units {
		if size >= unit.bytes {
			value := float64(size) / float64(unit.bytes)
			formatted := strconv.FormatFloat(value, 'f', 2, 64)
			formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
			return formatted + unit.suffix
		}
	}
	return strconv.FormatInt(size, 10) + "B"
}

func validJSONL(data []byte) bool {
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) > 0 && !json.Valid(line) {
			return false
		}
	}
	return true
}
