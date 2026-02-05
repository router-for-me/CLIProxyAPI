package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/codex-lite/internal/auth"
)

const (
	CodexBaseURL = "https://chatgpt.com/backend-api/codex"
)

type Executor struct {
	client *http.Client
}

func NewExecutor(timeout time.Duration) *Executor {
	return &Executor{
		client: &http.Client{Timeout: timeout},
	}
}

func (e *Executor) Execute(ctx context.Context, token *auth.TokenStorage, body []byte) ([]byte, error) {
	url := CodexBaseURL + "/responses"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.setHeaders(req, token)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(data))
	}
	return io.ReadAll(resp.Body)
}

func (e *Executor) ExecuteStream(ctx context.Context, token *auth.TokenStorage, body []byte) (<-chan []byte, error) {
	url := CodexBaseURL + "/responses"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.setHeaders(req, token)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(data))
	}

	out := make(chan []byte)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(nil, 10*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) > 0 {
				out <- append([]byte(nil), line...)
			}
		}
	}()
	return out, nil
}

func (e *Executor) setHeaders(req *http.Request, token *auth.TokenStorage) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Openai-Beta", "responses=experimental")
	req.Header.Set("Originator", "codex_cli_rs")
	if token.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", token.AccountID)
	}
}
