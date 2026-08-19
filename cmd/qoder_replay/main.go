// Command qoder_replay replays a captured Qoder request JSON file against the
// live upstream, using stored credentials for COSY signing. Supports binary
// search mode to isolate the message that triggers a WAF 405.
//
// Usage:
//
//	qoder_replay -auth <auth-file> -req <req-file>
//	qoder_replay -auth <auth-file> -req <req-file> -bisect
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	qoderauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

func main() {
	authFile := flag.String("auth", "", "Path to qoder auth JSON file")
	reqFile := flag.String("req", "", "Path to captured request JSON file")
	bisect := flag.Bool("bisect", false, "Binary search for the offending message")
	chatURL := flag.String("url", "", "Override chat URL (default: auto based on -encode flag)")
	encode := flag.Bool("encode", true, "Wrap and encode body (Encode=1 mode)")
	flag.Parse()

	if *chatURL == "" {
		if *encode {
			*chatURL = qoderauth.QoderChatURLEncoded
		} else {
			*chatURL = qoderauth.QoderChatURL
		}
	}

	if *authFile == "" || *reqFile == "" {
		fmt.Fprintln(os.Stderr, "usage: qoder_replay -auth <auth-file> -req <req-file> [-bisect]")
		os.Exit(1)
	}

	// Load auth
	authData, err := os.ReadFile(*authFile)
	if err != nil {
		fatalf("read auth: %v", err)
	}
	var storage qoderauth.QoderTokenStorage
	if err := json.Unmarshal(authData, &storage); err != nil {
		fatalf("parse auth: %v", err)
	}

	// Load request
	reqData, err := os.ReadFile(*reqFile)
	if err != nil {
		fatalf("read req: %v", err)
	}
	var reqBody map[string]interface{}
	if err := json.Unmarshal(reqData, &reqBody); err != nil {
		fatalf("parse req: %v", err)
	}

	msgs, _ := reqBody["messages"].([]interface{})
	fmt.Printf("Loaded request with %d messages\n", len(msgs))

	if !*bisect {
		status, body := send(&storage, reqBody, *chatURL, *encode)
		fmt.Printf("Status: %d\n", status)
		fmt.Printf("Body: %s\n", truncate(string(body), 500))
		return
	}

	// Binary search
	fmt.Printf("\nBisecting %d messages...\n\n", len(msgs))

	// Verify full set fails
	status, _ := send(&storage, withMessages(reqBody, msgs), *chatURL, *encode)
	if status != 405 {
		fmt.Printf("WARNING: full request returned %d (not 405), bisect may be unreliable\n", status)
	}

	lo, hi := 0, len(msgs)
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		subset := msgs[:mid]
		fmt.Printf("Testing msgs[0:%d]... ", mid)
		status, _ := send(&storage, withMessages(reqBody, subset), *chatURL, *encode)
		fmt.Printf("-> %d\n", status)
		if status == 405 {
			hi = mid
		} else {
			lo = mid
		}
	}

	fmt.Printf("\nOffending message index: %d\n", lo)
	m, _ := msgs[lo].(map[string]interface{})
	if m == nil {
		fmt.Println("(could not parse message)")
		return
	}
	fmt.Printf("role: %s\n", m["role"])
	content, _ := m["content"].(string)
	fmt.Printf("content: %s\n", truncate(content, 300))
	if tcs, ok := m["tool_calls"].([]interface{}); ok {
		for _, tc := range tcs {
			tcMap, _ := tc.(map[string]interface{})
			if tcMap == nil {
				continue
			}
			fn, _ := tcMap["function"].(map[string]interface{})
			if fn == nil {
				continue
			}
			fmt.Printf("tool_call: %s\n  args: %s\n", fn["name"], truncate(fmt.Sprintf("%v", fn["arguments"]), 500))
		}
	}
}

func withMessages(base map[string]interface{}, msgs []interface{}) map[string]interface{} {
	clone := make(map[string]interface{}, len(base))
	for k, v := range base {
		clone[k] = v
	}
	clone["messages"] = msgs
	// Fresh IDs so each bisect call is independent
	clone["request_id"] = uuid.New().String()
	clone["request_set_id"] = uuid.New().String()
	clone["chat_record_id"] = uuid.New().String()
	clone["session_id"] = uuid.New().String()
	if biz, ok := clone["business"].(map[string]interface{}); ok {
		bizClone := make(map[string]interface{}, len(biz))
		for k, v := range biz {
			bizClone[k] = v
		}
		bizClone["id"] = uuid.New().String()
		bizClone["begin_at"] = time.Now().UnixMilli()
		clone["business"] = bizClone
	}
	return clone
}

func send(storage *qoderauth.QoderTokenStorage, reqBody map[string]interface{}, chatURL string, encode bool) (int, []byte) {
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		fatalf("marshal: %v", err)
	}

	var sendBytes []byte
	if encode {
		sendBytes = []byte(helps.QoderEncodeBody(bodyBytes))
	} else {
		sendBytes = bodyBytes
	}

	req, err := http.NewRequest("POST", chatURL, bytes.NewReader(sendBytes))
	if err != nil {
		fatalf("new request: %v", err)
	}

	headers, err := qoderauth.BuildAuthHeaders(
		sendBytes,
		chatURL,
		qoderauth.CosyCredentials{
			UserID:    storage.UserID,
			AuthToken: storage.Token,
			Name:      storage.Name,
			Email:     storage.Email,
			MachineID: storage.MachineID,
		},
	)
	if err != nil {
		fatalf("build auth headers: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	headers.Apply(req)
	// Extract model key from request body for X-Model-Key header
	if modelKey, ok := reqBody["model_config"].(map[string]interface{}); ok {
		if key, ok := modelKey["key"].(string); ok && key != "" {
			req.Header.Set("X-Model-Key", key)
		}
		if src, ok := modelKey["source"].(string); ok && src != "" {
			req.Header.Set("X-Model-Source", src)
		} else {
			req.Header.Set("X-Model-Source", "system")
		}
	}
	req.Header.Set("Accept-Encoding", "identity")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "request error: %v\n", err)
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	// For 200 SSE responses, just read the first chunk to confirm success.
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, buf[:n]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
