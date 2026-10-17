package helps

import (
	"bufio"
	"bytes"
	"io"
	"reflect"
	"testing"
)

type sseByteReader struct{ io.Reader }

func (r sseByteReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:1]) }

func TestCodexSSELineEndingsAcrossReads(t *testing.T) {
	for _, input := range []string{"first\r\rsecond\r", "first\n\nsecond\n", "first\r\n\r\nsecond\r\n", "first\r\n\rsecond\n", "first\r\rsecond"} {
		scanner := bufio.NewScanner(sseByteReader{bytes.NewBufferString(input)})
		scanner.Split(newCodexSSELineSplitter())
		var got []string
		for scanner.Scan() {
			got = append(got, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []string{"first", "", "second"}) {
			t.Fatalf("input %q: lines = %#v", input, got)
		}
	}
}

func TestCodexSSECRDispatchDoesNotWaitForLookahead(t *testing.T) {
	split := newCodexSSELineSplitter()
	advance, token, err := split([]byte("data: {}\r"), false)
	if advance != 9 || string(token) != "data: {}" || err != nil {
		t.Fatalf("CR did not dispatch immediately: advance=%d token=%q err=%v", advance, token, err)
	}
	advance, token, err = split([]byte("\n"), false)
	if advance != 1 || token != nil || err != nil {
		t.Fatalf("split CRLF manufactured a line: advance=%d token=%q err=%v", advance, token, err)
	}
}
