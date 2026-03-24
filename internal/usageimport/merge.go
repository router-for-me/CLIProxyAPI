package usageimport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

type ExportPayload struct {
	Version    int                      `json:"version"`
	ExportedAt time.Time                `json:"exported_at"`
	Usage      usage.StatisticsSnapshot `json:"usage"`
}

type MergeSummary struct {
	Files               int
	SourceRequests      int64
	MergedRequests      int64
	DeduplicatedRecords int64
	Added               int64
	Skipped             int64
}

func ReadPayloadFile(path string) (ExportPayload, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ExportPayload{}, fmt.Errorf("read file: %w", err)
	}
	var payload ExportPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ExportPayload{}, fmt.Errorf("decode json: %w", err)
	}
	if payload.Version != 0 && payload.Version != 1 {
		return ExportPayload{}, fmt.Errorf("unsupported version %d", payload.Version)
	}
	return payload, nil
}

func MergePayloads(ctx context.Context, payloads []ExportPayload) (usage.StatisticsSnapshot, MergeSummary, error) {
	store := usage.NewMemoryStatisticsStore(usage.NewRequestStatistics())
	summary := MergeSummary{Files: len(payloads)}

	for i, payload := range payloads {
		if payload.Version != 0 && payload.Version != 1 {
			return usage.StatisticsSnapshot{}, summary, fmt.Errorf("payload %d has unsupported version %d", i, payload.Version)
		}
		summary.SourceRequests += payload.Usage.TotalRequests
		result, err := store.Import(ctx, payload.Usage)
		if err != nil {
			return usage.StatisticsSnapshot{}, summary, fmt.Errorf("merge payload %d: %w", i, err)
		}
		summary.Added += result.Added
		summary.Skipped += result.Skipped
	}

	snapshot, err := store.Export(ctx)
	if err != nil {
		return usage.StatisticsSnapshot{}, summary, fmt.Errorf("export merged snapshot: %w", err)
	}
	if summary.SourceRequests >= snapshot.TotalRequests {
		summary.DeduplicatedRecords = summary.SourceRequests - snapshot.TotalRequests
	}
	summary.MergedRequests = snapshot.TotalRequests
	return snapshot, summary, nil
}

func ResolveInputPaths(inputs []string) ([]string, error) {
	seen := make(map[string]struct{})
	resolved := make([]string, 0, len(inputs))

	for _, input := range inputs {
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		matches, err := expandInput(input)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			resolved = append(resolved, match)
		}
	}

	sort.Strings(resolved)
	return resolved, nil
}

func expandInput(input string) ([]string, error) {
	if hasGlobMeta(input) {
		matches, err := filepath.Glob(input)
		if err != nil {
			return nil, fmt.Errorf("expand glob %q: %w", input, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no files matched %q", input)
		}
		files := make([]string, 0, len(matches))
		for _, match := range matches {
			expanded, err := expandInput(match)
			if err != nil {
				return nil, err
			}
			files = append(files, expanded...)
		}
		return files, nil
	}

	info, err := os.Stat(input)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", input, err)
	}

	if !info.IsDir() {
		absPath, err := filepath.Abs(input)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", input, err)
		}
		return []string{absPath}, nil
	}

	entries, err := os.ReadDir(input)
	if err != nil {
		return nil, fmt.Errorf("read directory %q: %w", input, err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		fullPath := filepath.Join(input, entry.Name())
		absPath, err := filepath.Abs(fullPath)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", fullPath, err)
		}
		files = append(files, absPath)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("directory %q does not contain any .json files", input)
	}
	sort.Strings(files)
	return files, nil
}

func hasGlobMeta(input string) bool {
	return strings.ContainsAny(input, "*?[")
}
