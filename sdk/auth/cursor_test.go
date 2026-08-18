package auth

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	log "github.com/sirupsen/logrus"
)

var cursorLoginOutputMu sync.Mutex

func TestCursorLoginWritesInstructionsToStdoutWithFileLogging(t *testing.T) {
	output := captureCursorLoginOutput(t, true)
	assertCursorLoginInstructions(t, output)
}

func TestCursorLoginWritesInstructionsOnceWithStandardLogging(t *testing.T) {
	output := captureCursorLoginOutput(t, false)
	assertCursorLoginInstructions(t, output)
}

func captureCursorLoginOutput(t *testing.T, loggingToFile bool) string {
	t.Helper()

	cursorLoginOutputMu.Lock()
	t.Cleanup(cursorLoginOutputMu.Unlock)

	t.Setenv("WRITABLE_PATH", t.TempDir())
	previousLogOutput := log.StandardLogger().Out
	t.Cleanup(func() {
		if err := logging.ConfigureLogOutput(&config.Config{}); err != nil {
			t.Errorf("restore log output: %v", err)
		}
		log.SetOutput(previousLogOutput)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return captureStdout(t, func() {
		if err := logging.ConfigureLogOutput(&config.Config{LoggingToFile: loggingToFile}); err != nil {
			t.Fatalf("configure log output: %v", err)
		}

		_, err := NewCursorAuthenticator().Login(ctx, &config.Config{}, &LoginOptions{NoBrowser: true})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Login() error = %v, want context canceled", err)
		}
	})
}

func captureStdout(t *testing.T, action func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}

	previousStdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = previousStdout
	})

	action()

	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	os.Stdout = previousStdout

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stdout reader: %v", err)
	}
	return string(output)
}

func assertCursorLoginInstructions(t *testing.T, output string) {
	t.Helper()

	const (
		startingPrompt = "Starting Cursor authentication..."
		urlPrompt      = "Please visit this URL to log in: "
		waitingPrompt  = "Waiting for Cursor authorization..."
	)

	for _, prompt := range []string{startingPrompt, urlPrompt, waitingPrompt} {
		if count := strings.Count(output, prompt); count != 1 {
			t.Fatalf("stdout contains %q %d times, want once:\n%s", prompt, count, output)
		}
	}

	startingIndex := strings.Index(output, startingPrompt)
	urlIndex := strings.Index(output, urlPrompt)
	waitingIndex := strings.Index(output, waitingPrompt)
	if !(startingIndex < urlIndex && urlIndex < waitingIndex) {
		t.Fatalf("stdout prompt order is wrong:\n%s", output)
	}

	urlLine := output[urlIndex+len(urlPrompt):]
	loginURL := strings.TrimSpace(strings.SplitN(urlLine, "\n", 2)[0])
	parsedURL, err := url.Parse(loginURL)
	if err != nil {
		t.Fatalf("parse login URL %q: %v", loginURL, err)
	}
	if parsedURL.Scheme != "https" || parsedURL.Host != "cursor.com" || parsedURL.Path != "/loginDeepControl" {
		t.Fatalf("unexpected login URL %q", loginURL)
	}
	query := parsedURL.Query()
	if query.Get("challenge") == "" || query.Get("uuid") == "" || query.Get("mode") != "login" || query.Get("redirectTarget") != "cli" {
		t.Fatalf("incomplete login URL %q", loginURL)
	}
	if count := strings.Count(output, loginURL); count != 1 {
		t.Fatalf("stdout contains the complete login URL %d times, want once:\n%s", count, output)
	}

	lowerOutput := strings.ToLower(output)
	for _, secret := range []string{"verifier=", "access_token", "refresh_token", "authorization:"} {
		if strings.Contains(lowerOutput, secret) {
			t.Fatalf("stdout exposed %q:\n%s", secret, output)
		}
	}
}
