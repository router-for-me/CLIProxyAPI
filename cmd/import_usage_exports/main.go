// Command import_usage_exports imports one or more legacy usage export files
// into the management usage import endpoint.
//
// Usage:
//
// 	go run ./cmd/import_usage_exports --server-url http://localhost:8317 --management-key <key> exports/*.json
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usageimport"
)

type importRequest struct {
	Version int                      `json:"version"`
	Usage   usage.StatisticsSnapshot `json:"usage"`
}

type importResponse struct {
	Added          int64  `json:"added"`
	Skipped        int64  `json:"skipped"`
	TotalRequests  int64  `json:"total_requests"`
	FailedRequests int64  `json:"failed_requests"`
	Error          string `json:"error"`
}

func main() {
	var serverURL string
	var managementKey string
	var timeout time.Duration
	var dryRun bool

	flag.StringVar(&serverURL, "server-url", "http://localhost:8317", "CLIProxyAPI base URL")
	flag.StringVar(&managementKey, "management-key", "", "Management key for /v0/management/usage/import")
	flag.DurationVar(&timeout, "timeout", 60*time.Second, "HTTP timeout")
	flag.BoolVar(&dryRun, "dry-run", false, "Only merge and report counts without importing")
	flag.Parse()

	if strings.TrimSpace(managementKey) == "" && !dryRun {
		fmt.Fprintln(os.Stderr, "error: --management-key is required unless --dry-run is set")
		os.Exit(1)
	}

	inputs := flag.Args()
	if len(inputs) == 0 {
		fmt.Fprintln(os.Stderr, "error: provide one or more export files, globs, or directories")
		os.Exit(1)
	}

	paths, err := usageimport.ResolveInputPaths(inputs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Resolved %d input files\n", len(paths))

	if dryRun {
		payloads := make([]usageimport.ExportPayload, 0, len(paths))
		for _, path := range paths {
			payload, err := usageimport.ReadPayloadFile(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error reading %s: %v\n", path, err)
				os.Exit(1)
			}
			payloads = append(payloads, payload)
		}

		merged, summary, err := usageimport.MergePayloads(context.Background(), payloads)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Source requests: %d\n", summary.SourceRequests)
		fmt.Printf("Unique merged requests: %d\n", merged.TotalRequests)
		fmt.Printf("Deduplicated overlaps: %d\n", summary.DeduplicatedRecords)
		fmt.Println("Dry run complete. No import requests were sent.")
		return
	}

	totalAdded := int64(0)
	totalSkipped := int64(0)
	lastTotalRequests := int64(0)
	lastFailedRequests := int64(0)
	for _, path := range paths {
		payload, err := usageimport.ReadPayloadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading %s: %v\n", path, err)
			os.Exit(1)
		}

		resp, err := importSnapshot(serverURL, managementKey, timeout, payload.Usage)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error importing %s: %v\n", path, err)
			os.Exit(1)
		}

		totalAdded += resp.Added
		totalSkipped += resp.Skipped
		lastTotalRequests = resp.TotalRequests
		lastFailedRequests = resp.FailedRequests

		fmt.Printf("Imported %s -> added=%d skipped=%d total_requests=%d\n", path, resp.Added, resp.Skipped, resp.TotalRequests)
	}

	fmt.Printf("Imported %d files successfully\n", len(paths))
	fmt.Printf("Server added total: %d\n", totalAdded)
	fmt.Printf("Server skipped total: %d\n", totalSkipped)
	fmt.Printf("Server total requests: %d\n", lastTotalRequests)
	fmt.Printf("Server failed requests: %d\n", lastFailedRequests)
}

func importSnapshot(serverURL, managementKey string, timeout time.Duration, snapshot usage.StatisticsSnapshot) (importResponse, error) {
	requestBody, err := json.Marshal(importRequest{Version: 1, Usage: snapshot})
	if err != nil {
		return importResponse{}, fmt.Errorf("marshal import request: %w", err)
	}

	endpoint := strings.TrimRight(strings.TrimSpace(serverURL), "/") + "/v0/management/usage/import"
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return importResponse{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Management-Key", managementKey)

	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return importResponse{}, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return importResponse{}, fmt.Errorf("read response: %w", err)
	}

	var result importResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return importResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if result.Error != "" {
			return importResponse{}, fmt.Errorf("server returned %d: %s", resp.StatusCode, result.Error)
		}
		return importResponse{}, fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return result, nil
}
