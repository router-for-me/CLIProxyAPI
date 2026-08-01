package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

func TestPendingStreamErrorReturnsBufferedError(t *testing.T) {
	wantErr := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("upstream failed before sending any payload"),
	}
	// The producer buffers the terminal error and then closes both channels, so a
	// consumer that observes the closed data channel can still drain the error here.
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- wantErr
	close(errs)

	gotErr, ok := PendingStreamError(errs)
	if !ok {
		t.Fatal("expected a pending stream error")
	}
	if gotErr != wantErr {
		t.Fatalf("pending error = %v, want %v", gotErr, wantErr)
	}
}

func TestPendingStreamErrorReportsNoErrorForClosedChannel(t *testing.T) {
	errs := make(chan *interfaces.ErrorMessage, 1)
	close(errs)

	if gotErr, ok := PendingStreamError(errs); ok {
		t.Fatalf("pending error = %v, want none", gotErr)
	}
}

func TestPendingStreamErrorReportsNoErrorForOpenChannel(t *testing.T) {
	errs := make(chan *interfaces.ErrorMessage, 1)

	if gotErr, ok := PendingStreamError(errs); ok {
		t.Fatalf("pending error = %v, want none", gotErr)
	}
}

func TestPendingStreamErrorReportsNoErrorForNilChannel(t *testing.T) {
	if gotErr, ok := PendingStreamError(nil); ok {
		t.Fatalf("pending error = %v, want none", gotErr)
	}
}
