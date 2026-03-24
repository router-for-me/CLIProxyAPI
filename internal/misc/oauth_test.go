package misc

import (
	"errors"
	"testing"
	"time"
)

func TestAsyncPromptReturnsInput(t *testing.T) {
	messageCh := make(chan string, 1)
	inputCh, errCh := AsyncPrompt(func(message string) (string, error) {
		messageCh <- message
		return "callback-url", nil
	}, "prompt")

	select {
	case got := <-inputCh:
		if got != "callback-url" {
			t.Fatalf("input = %q, want %q", got, "callback-url")
		}
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async prompt input")
	}

	select {
	case gotMessage := <-messageCh:
		if gotMessage != "prompt" {
			t.Fatalf("prompt message = %q, want %q", gotMessage, "prompt")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for prompt message capture")
	}
}

func TestAsyncPromptReturnsError(t *testing.T) {
	wantErr := errors.New("prompt failed")
	inputCh, errCh := AsyncPrompt(func(string) (string, error) {
		return "", wantErr
	}, "prompt")

	select {
	case got := <-inputCh:
		t.Fatalf("unexpected input: %q", got)
	case err := <-errCh:
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async prompt error")
	}
}

func TestAsyncPromptDoesNotBlockCaller(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	inputCh, errCh := AsyncPrompt(func(string) (string, error) {
		close(started)
		<-release
		return "callback-url", nil
	}, "prompt")

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("prompt goroutine did not start")
	}

	select {
	case got := <-inputCh:
		t.Fatalf("unexpected input before release: %q", got)
	case err := <-errCh:
		t.Fatalf("unexpected error before release: %v", err)
	default:
	}

	close(release)

	select {
	case got := <-inputCh:
		if got != "callback-url" {
			t.Fatalf("input = %q, want %q", got, "callback-url")
		}
	case err := <-errCh:
		t.Fatalf("unexpected error after release: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async prompt input after release")
	}
}
