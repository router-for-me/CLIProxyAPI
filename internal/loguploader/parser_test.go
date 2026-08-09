package loguploader

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteJSONLRecordStreamsLargeMultibyteLog(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	root := filepath.Join(t.TempDir(), "keys")
	timestamp := time.Date(2026, time.July, 15, 1, 23, 45, 123000000, location)
	prefix := "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== REQUEST BODY ===\n" +
		`{"model":"claude-opus-4","input":"` + "\n"

	// Put the first byte of a three-byte rune at byte 65535. This forces the
	// streaming encoder to preserve a rune split across its 64 KiB read boundary.
	const boundary = 64 << 10
	paddingSize := boundary - 1 - len(prefix)
	if paddingSize <= 0 {
		t.Fatalf("test prefix unexpectedly exceeds streaming boundary: %d", len(prefix))
	}
	rawLog := prefix + strings.Repeat("a", paddingSize) + "界\"\\\n下一行\u2028end"
	modTime := timestamp.Add(-24 * time.Hour)
	path := mustWriteLog(t, root, "panda", "v1-responses-2026-07-15T012345-large.log", rawLog, modTime)

	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat source log: %v", errStat)
	}
	source, errInspect := inspectSourceLog(root, path, info, location)
	if errInspect != nil {
		t.Fatalf("inspect source log: %v", errInspect)
	}

	var output bytes.Buffer
	written, errWrite := writeJSONLRecord(&output, source)
	if errWrite != nil {
		t.Fatalf("write JSONL record: %v", errWrite)
	}
	if written != int64(output.Len()) {
		t.Fatalf("reported bytes = %d, actual bytes = %d", written, output.Len())
	}
	if !validJSONL(output.Bytes()) {
		t.Fatalf("streamed output is not valid JSONL")
	}

	var record struct {
		SchemaVersion int       `json:"schema_version"`
		KeyName       string    `json:"key_name"`
		Model         string    `json:"model"`
		SourceFile    string    `json:"source_file"`
		SourceSize    int64     `json:"source_size_bytes"`
		Timestamp     time.Time `json:"timestamp"`
		RawLog        string    `json:"raw_log"`
	}
	if errUnmarshal := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); errUnmarshal != nil {
		t.Fatalf("decode JSONL record: %v", errUnmarshal)
	}
	if record.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", record.SchemaVersion)
	}
	if record.KeyName != "panda" {
		t.Errorf("key_name = %q, want panda", record.KeyName)
	}
	if record.Model != "claude-opus-4" {
		t.Errorf("model = %q, want claude-opus-4", record.Model)
	}
	if record.SourceFile != filepath.ToSlash(filepath.Join("panda", filepath.Base(path))) {
		t.Errorf("source_file = %q", record.SourceFile)
	}
	if record.SourceSize != int64(len(rawLog)) {
		t.Errorf("source_size_bytes = %d, want %d", record.SourceSize, len(rawLog))
	}
	if !record.Timestamp.Equal(timestamp) {
		t.Errorf("timestamp = %s, want %s", record.Timestamp, timestamp)
	}
	if record.RawLog != rawLog {
		t.Fatalf("raw_log did not round-trip: got %d bytes, want %d", len(record.RawLog), len(rawLog))
	}
}

func TestInspectSourceLogTimestampAndModelFallbacks(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	root := filepath.Join(t.TempDir(), "keys")
	fallback := time.Date(2026, time.July, 16, 8, 9, 10, 0, location)

	tests := []struct {
		name      string
		filename  string
		contents  string
		wantTime  time.Time
		wantModel string
	}{
		{
			name:      "header timestamp takes precedence",
			filename:  "v1-responses-2026-07-14T010203-header.log",
			contents:  "Timestamp: 2026-07-15T03:04:05.123+08:00\n{\"model\":\"gpt-5.6-sol\"}\n",
			wantTime:  time.Date(2026, time.July, 15, 3, 4, 5, 123000000, location),
			wantModel: "gpt-5.6-sol",
		},
		{
			name:      "filename timestamp",
			filename:  "v1-responses-2026-07-14T234033-file.log",
			contents:  "{\"model\":\"claude-opus-4\"}\n",
			wantTime:  time.Date(2026, time.July, 14, 23, 40, 33, 0, location),
			wantModel: "claude-opus-4",
		},
		{
			name:      "modtime and unknown model fallback",
			filename:  "unstructured.log",
			contents:  "plain log without request metadata\n",
			wantTime:  fallback,
			wantModel: "unknown",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := mustWriteLog(t, root, sanitizeName(test.name, "case"), test.filename, test.contents, fallback)
			info, errStat := os.Stat(path)
			if errStat != nil {
				t.Fatalf("stat source: %v", errStat)
			}
			source, errInspect := inspectSourceLog(root, path, info, location)
			if errInspect != nil {
				t.Fatalf("inspect source: %v", errInspect)
			}
			if wantArchiveHour := fallback.Truncate(time.Hour); !source.ArchiveHour.Equal(wantArchiveHour) {
				t.Errorf("archive hour = %s, want completion hour %s", source.ArchiveHour, wantArchiveHour)
			}
			if !source.Timestamp.Equal(test.wantTime) {
				t.Errorf("timestamp = %s, want %s", source.Timestamp, test.wantTime)
			}
			if source.Model != test.wantModel {
				t.Errorf("model = %q, want %q", source.Model, test.wantModel)
			}
		})
	}
}

func TestArchiveFilenameAndHumanSize(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	hour := time.Date(2026, time.July, 15, 1, 0, 0, 0, location)
	if got, want := makeArchiveFilename(hour, providerCodex, 2<<30), "2026-07-15-01-codex56sol-2G.jsonl.zst"; got != want {
		t.Errorf("archive filename = %q, want %q", got, want)
	}
	if got, want := makeArchiveFilename(hour, providerCodex, 1536), "2026-07-15-01-codex56sol-1.5K.jsonl.zst"; got != want {
		t.Errorf("archive filename = %q, want %q", got, want)
	}
	if got, want := makeArchiveFilename(hour, providerClaude, 2<<30), "2026-07-15-01-fable5-2G.jsonl.zst"; got != want {
		t.Errorf("claude archive filename = %q, want %q", got, want)
	}
	if got, want := makeArchiveFilename(hour, providerGrok, 2<<30), "2026-07-15-01-grok45-2G.jsonl.zst"; got != want {
		t.Errorf("grok archive filename = %q, want %q", got, want)
	}

	tests := []struct {
		size int64
		want string
	}{
		{0, "0B"},
		{999, "999B"},
		{1024, "1K"},
		{1536, "1.5K"},
		{5 << 20, "5M"},
		{5 << 30 / 2, "2.5G"},
		{2 << 40, "2T"},
	}
	for _, test := range tests {
		if got := humanSize(test.size); got != test.want {
			t.Errorf("humanSize(%d) = %q, want %q", test.size, got, test.want)
		}
	}
}

func TestWriteJSONLRecordRedactsSensitiveHeaders(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	root := filepath.Join(t.TempDir(), "keys")
	timestamp := time.Date(2026, time.July, 15, 1, 23, 45, 0, location)
	rawLog := "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== HEADERS ===\n" +
		"Authorization: Bearer secret-user-api-key\n" +
		"Cookie: session=secret-cookie\n" +
		"X-Access-Token: secret-access-token\n" +
		"Private-Token: secret-private-token\n" +
		"X-Client-Secret: secret-client-value\n" +
		"X-OpenAI-Api-Key: secret-openai-key\n" +
		"X-ApiKey: secret-compact-key\n" +
		"Content-Type: application/json\n\n" +
		`{"model":"claude-opus-4"}` + "\n"
	path := mustWriteLog(t, root, "panda", "redaction.log", rawLog, timestamp.Add(-time.Hour))
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat source log: %v", errStat)
	}
	source, errInspect := inspectSourceLog(root, path, info, location)
	if errInspect != nil {
		t.Fatalf("inspect source log: %v", errInspect)
	}

	var output bytes.Buffer
	if _, errWrite := writeJSONLRecord(&output, source); errWrite != nil {
		t.Fatalf("write JSONL record: %v", errWrite)
	}
	var record struct {
		SensitiveHeadersRedacted bool   `json:"sensitive_headers_redacted"`
		RawLog                   string `json:"raw_log"`
	}
	if errUnmarshal := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); errUnmarshal != nil {
		t.Fatalf("decode JSONL record: %v", errUnmarshal)
	}
	if !record.SensitiveHeadersRedacted {
		t.Errorf("sensitive_headers_redacted = false")
	}
	for _, secret := range []string{
		"secret-user-api-key",
		"secret-cookie",
		"secret-access-token",
		"secret-private-token",
		"secret-client-value",
		"secret-openai-key",
		"secret-compact-key",
	} {
		if strings.Contains(record.RawLog, secret) {
			t.Fatalf("sensitive header value %q leaked into JSONL", secret)
		}
	}
	if strings.Count(record.RawLog, "[REDACTED]") != 7 {
		t.Fatalf("sensitive header value leaked into JSONL")
	}
	if !strings.Contains(record.RawLog, "Authorization: [REDACTED]") || !strings.Contains(record.RawLog, "Cookie: [REDACTED]") {
		t.Errorf("redaction markers missing from raw_log: %q", record.RawLog)
	}
	if !strings.Contains(record.RawLog, "Content-Type: application/json") {
		t.Errorf("non-sensitive header was unexpectedly changed")
	}
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, errLoad := time.LoadLocation(name)
	if errLoad != nil {
		t.Fatalf("load location %q: %v", name, errLoad)
	}
	return location
}

func mustWriteLog(t *testing.T, root, keyName, filename, contents string, modTime time.Time) string {
	t.Helper()
	directory := filepath.Join(root, keyName)
	if errMkdir := os.MkdirAll(directory, 0o750); errMkdir != nil {
		t.Fatalf("create log directory: %v", errMkdir)
	}
	path := filepath.Join(directory, filename)
	if errWrite := os.WriteFile(path, []byte(contents), 0o640); errWrite != nil {
		t.Fatalf("write source log: %v", errWrite)
	}
	if errTimes := os.Chtimes(path, modTime, modTime); errTimes != nil {
		t.Fatalf("set source modtime: %v", errTimes)
	}
	return path
}

func TestInspectSourceLogUsesPolicyV1AbsoluteHourBoundary(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Kathmandu")
	completionTime := time.Date(2026, time.July, 15, 1, 37, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	path := mustWriteLog(t, root, "panda", "v1-responses-policy-v1-hour.log",
		requestLog(completionTime, "gpt-5.6-sol", "policy v1 hour"), completionTime)
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat source log: %v", errStat)
	}

	source, errInspect := inspectSourceLog(root, path, info, location)
	if errInspect != nil {
		t.Fatalf("inspect source log: %v", errInspect)
	}
	wantHour := completionTime.In(location).Truncate(time.Hour)
	if wantHour.Minute() == 0 {
		t.Fatalf("policy-v1 absolute hour unexpectedly used local HH:00: %s", wantHour)
	}
	if !source.ArchiveHour.Equal(wantHour) || source.ArchiveHour.Location() != location {
		t.Fatalf("archive hour = %s (%s), want policy-v1 instant %s in %s", source.ArchiveHour, source.ArchiveHour.Location(), wantHour, location)
	}
}

func TestClassifyProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
		want  string
	}{
		{"claude-sonnet-4-20250514", providerClaude},
		{"claude-opus-4-20250514", providerClaude},
		{"Claude-Sonnet-4-20250514", providerClaude},
		{"claude-fable-5-dd-o4-tpg", providerCodex},
		{"claude-fable-5-dd-inimig-4o-tpg", providerCodex},
		{"grok-4.5", providerGrok},
		{"grok-4.5-mini", providerGrok},
		{"Grok-4.5", providerGrok},
		{"xai/grok-4.5", providerGrok},
		{"xai/grok-4.5-mini", providerGrok},
		{"XAI/Grok-4.5", providerGrok},
		{"gpt-4o", providerCodex},
		{"o3", providerCodex},
		{"unknown", providerCodex},
		{"", providerCodex},
	}
	for _, test := range tests {
		if got := classifyProvider(test.model); got != test.want {
			t.Errorf("classifyProvider(%q) = %q, want %q", test.model, got, test.want)
		}
	}
}
