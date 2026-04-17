package logging

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnforceRequestLogRetentionCompressesAndDeletes(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 17, 10, 0, 0, 0, time.Local)

	compressDate := now.AddDate(0, 0, -2)
	deleteDate := now.AddDate(0, 0, -31)

	plainPath := filepath.Join(dir, "request-"+compressDate.Format(requestLogDateLayout)+".log")
	oldPath := filepath.Join(dir, "request-"+deleteDate.Format(requestLogDateLayout)+".log")

	if err := os.WriteFile(plainPath, []byte("plain"), 0o644); err != nil {
		t.Fatalf("write plain request log: %v", err)
	}
	if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("write old request log: %v", err)
	}
	if err := gzipFile(oldPath); err != nil {
		t.Fatalf("gzip old request log: %v", err)
	}

	policy := RequestLogPolicy{
		AggregateDaily:    true,
		CompressAfterDays: 1,
		DeleteAfterDays:   30,
	}
	if err := EnforceRequestLogRetention(dir, policy, now); err != nil {
		t.Fatalf("enforce retention: %v", err)
	}

	if _, err := os.Stat(plainPath); !os.IsNotExist(err) {
		t.Fatalf("expected plain log to be compressed, stat err=%v", err)
	}
	if _, err := os.Stat(plainPath + ".gz"); err != nil {
		t.Fatalf("expected compressed log to exist: %v", err)
	}
	if _, err := os.Stat(oldPath + ".gz"); !os.IsNotExist(err) {
		t.Fatalf("expected expired archive to be deleted, stat err=%v", err)
	}
}

func TestExtractRequestRecordByIDSupportsPlainAndGzip(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "request-2026-04-17.log")

	recordA, err := newRequestRecordBuffer(requestRecordMeta{
		RequestID:  "req-a",
		Timestamp:  time.Date(2026, 4, 17, 9, 0, 0, 0, time.Local),
		URL:        "/v1/chat/completions",
		Method:     "POST",
		StatusCode: 200,
		RecordType: requestLogTypeNormal,
	}, func(w io.Writer) error {
		_, errWrite := w.Write([]byte("payload-a"))
		return errWrite
	})
	if err != nil {
		t.Fatalf("build record A: %v", err)
	}
	recordB, err := newRequestRecordBuffer(requestRecordMeta{
		RequestID:  "req-b",
		Timestamp:  time.Date(2026, 4, 17, 9, 1, 0, 0, time.Local),
		URL:        "/v1/responses",
		Method:     "POST",
		StatusCode: 500,
		RecordType: requestLogTypeError,
	}, func(w io.Writer) error {
		_, errWrite := w.Write([]byte("payload-b"))
		return errWrite
	})
	if err != nil {
		t.Fatalf("build record B: %v", err)
	}

	if err := os.WriteFile(filePath, bytes.Join([][]byte{recordA, recordB}, nil), 0o644); err != nil {
		t.Fatalf("write aggregated request log: %v", err)
	}

	gotPlain, err := ExtractRequestRecordByID(filePath, "req-b")
	if err != nil {
		t.Fatalf("extract plain record: %v", err)
	}
	if !bytes.Contains(gotPlain, []byte("Request-ID: req-b")) || !bytes.Contains(gotPlain, []byte("payload-b")) {
		t.Fatalf("plain record did not contain expected payload: %s", string(gotPlain))
	}

	if err := gzipFile(filePath); err != nil {
		t.Fatalf("gzip aggregated request log: %v", err)
	}
	gotGzip, err := ExtractRequestRecordByID(filePath+".gz", "req-a")
	if err != nil {
		t.Fatalf("extract gzip record: %v", err)
	}
	if !bytes.Contains(gotGzip, []byte("Request-ID: req-a")) || !bytes.Contains(gotGzip, []byte("payload-a")) {
		t.Fatalf("gzip record did not contain expected payload: %s", string(gotGzip))
	}
}
