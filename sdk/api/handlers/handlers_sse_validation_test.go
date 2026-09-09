package handlers

import (
	"bytes"
	"testing"
)

func TestSSEJSONValidationSplitCRLF(t *testing.T) {
	const firstLine = "data: {\"type\":\"response.completed\","
	const secondLine = "data: \"response\":{\"status\":\"completed\"}}"
	want := []byte(firstLine + "\n" + secondLine + "\n\n")

	for _, tt := range []struct {
		name   string
		chunks []string
	}{
		{"unsplit", []string{firstLine + "\r\n" + secondLine + "\r\n\r\n"}},
		{"split data line", []string{firstLine + "\r", "\n" + secondLine + "\r\n\r\n"}},
		{"empty chunk between CR and LF", []string{firstLine + "\r", "", "\n" + secondLine + "\r\n\r\n"}},
		{"LF in its own chunk", []string{firstLine + "\r", "\n", secondLine + "\r\n\r\n"}},
		{"bare CR", []string{firstLine + "\r", secondLine + "\r\r"}},
		{"LF", []string{firstLine + "\n", secondLine + "\n\n"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state := &sseJSONValidationState{}
			var got []byte
			for i, chunk := range tt.chunks {
				output, err := state.AddChunk([]byte(chunk))
				if err != nil {
					t.Fatalf("AddChunk(%d): %v", i, err)
				}
				got = append(got, output...)
			}
			if err := state.Finish(); err != nil {
				t.Fatalf("Finish(): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("output = %q, want %q", got, want)
			}
		})
	}
}
