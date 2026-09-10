package handlers

import (
	"bytes"
	"fmt"
	"strings"
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

func TestSSEJSONValidationPreservesFrameDelimiters(t *testing.T) {
	const first = `data: {"type":"response.created"}`
	const second = `data: {"type":"response.completed"}`
	want := first + "\n\n" + second + "\n\n"
	for _, ending := range []string{"\r\n", "\n", "\r"} {
		wire := first + ending + ending + second + ending + ending
		// Exercise every two-chunk boundary, including valid JSON immediately
		// before CR, between CR and LF, and between the two delimiter line endings.
		for split := 1; split < len(wire); split++ {
			t.Run(fmt.Sprintf("ending=%q/split=%d", ending, split), func(t *testing.T) {
				state := &sseJSONValidationState{}
				var output strings.Builder
				for _, chunk := range []string{wire[:split], wire[split:]} {
					got, err := state.AddChunk([]byte(chunk))
					if err != nil {
						t.Fatal(err)
					}
					output.Write(got)
				}
				if err := state.Finish(); err != nil {
					t.Fatal(err)
				}
				if got := output.String(); got != want {
					t.Fatalf("output = %q, want two separate frames %q", got, want)
				}
			})
		}
		t.Run(fmt.Sprintf("ending=%q/bytewise", ending), func(t *testing.T) {
			state := &sseJSONValidationState{}
			var output strings.Builder
			for i := range len(wire) {
				got, err := state.AddChunk([]byte(wire[i : i+1]))
				if err != nil {
					t.Fatal(err)
				}
				output.Write(got)
			}
			if err := state.Finish(); err != nil {
				t.Fatal(err)
			}
			if got := output.String(); got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
		})
	}
}
