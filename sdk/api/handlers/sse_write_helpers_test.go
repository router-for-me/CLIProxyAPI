package handlers

import (
	"bytes"
	"testing"
)

func TestWriteSSEDataFrame(t *testing.T) {
	var out bytes.Buffer

	if ok := WriteSSEDataFrame(&out, []byte(`{"ok":true}`)); !ok {
		t.Fatal("expected write success")
	}

	if got, want := out.String(), "data: {\"ok\":true}\n\n"; got != want {
		t.Fatalf("unexpected SSE data frame.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestWriteSSEEventDataFrameWithLeadingNewline(t *testing.T) {
	var out bytes.Buffer

	if ok := WriteSSEEventDataFrameWithLeadingNewline(&out, "error", []byte(`{"type":"error"}`)); !ok {
		t.Fatal("expected write success")
	}

	if got, want := out.String(), "\nevent: error\ndata: {\"type\":\"error\"}\n\n"; got != want {
		t.Fatalf("unexpected SSE event frame.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestWriteRawSSEChunkPreservesCRLFTerminator(t *testing.T) {
	var out bytes.Buffer

	if ok := WriteRawSSEChunk(&out, []byte("event: ping\r\n")); !ok {
		t.Fatal("expected write success")
	}

	if got, want := out.String(), "event: ping\r\n\r\n"; got != want {
		t.Fatalf("unexpected raw SSE chunk.\nGot:  %q\nWant: %q", got, want)
	}
}
