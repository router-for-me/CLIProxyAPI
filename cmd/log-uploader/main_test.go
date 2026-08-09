package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/loguploader"
)

type fakeCLIService struct {
	historyCalls int
	historyDry   bool
	summary      loguploader.SupabaseHistorySummary
}

func (service *fakeCLIService) Run(context.Context, bool) error { return nil }

func (service *fakeCLIService) RunOnce(context.Context, bool) error { return nil }

func (service *fakeCLIService) MigrateLegacyState(context.Context, string, string, bool) error {
	return nil
}

func (service *fakeCLIService) SyncSupabaseHistory(_ context.Context, dryRun bool) (loguploader.SupabaseHistorySummary, error) {
	service.historyCalls++
	service.historyDry = dryRun
	return service.summary, nil
}

func TestRunCLISyncSupabaseHistoryDoesNotCreateTOSUploader(t *testing.T) {
	service := &fakeCLIService{summary: loguploader.SupabaseHistorySummary{Pending: 2, SourceBytes: 123}}
	tosCalls := 0
	deps := cliDependencies{
		loadConfig: func(string) (loguploader.Config, error) {
			return loguploader.Config{
				Upload:   loguploader.UploadConfig{Enabled: true},
				Supabase: loguploader.SupabaseConfig{Enabled: true},
			}, nil
		},
		loadEnv: func(string) error { return nil },
		newTOSUploader: func(loguploader.UploadConfig) (loguploader.ObjectUploader, error) {
			tosCalls++
			return nil, errors.New("TOS must not be initialized")
		},
		newService: func(loguploader.Config, loguploader.ObjectUploader) (cliService, error) {
			return service, nil
		},
	}
	var stdout bytes.Buffer
	errRun := runCLI([]string{"--config", "test.yaml", "--sync-supabase-history", "--dry-run"}, &stdout, deps)
	if errRun != nil {
		t.Fatalf("run history CLI: %v", errRun)
	}
	if tosCalls != 0 || service.historyCalls != 1 || !service.historyDry {
		t.Fatalf("history dispatch: tos_calls=%d history_calls=%d dry_run=%t", tosCalls, service.historyCalls, service.historyDry)
	}
	if got := strings.TrimSpace(stdout.String()); got != `{"records":0,"pending":2,"live_managed":0,"already_checkpointed":0,"attempted":0,"inserted":0,"duplicate":0,"checkpointed":0,"source_count":0,"source_bytes":123,"batch_jsonl_bytes":0,"batch_compressed_bytes":0,"duplicate_records":0,"truncated_tails":0}` {
		t.Fatalf("history summary output = %q", got)
	}
}

func TestRunCLIRejectsHistoryModeConflictsBeforeCreatingDependencies(t *testing.T) {
	tests := [][]string{
		{"--sync-supabase-history", "--once"},
		{"--sync-supabase-history", "--migrate-legacy-manifest", "manifest.jsonl", "--migrate-legacy-archives", "archives"},
		{"--sync-supabase-history", "--migrate-legacy-trust-local"},
	}
	for _, args := range tests {
		loadCalls := 0
		deps := cliDependencies{
			loadConfig: func(string) (loguploader.Config, error) {
				loadCalls++
				return loguploader.Config{}, nil
			},
			loadEnv: func(string) error { return nil },
			newTOSUploader: func(loguploader.UploadConfig) (loguploader.ObjectUploader, error) {
				t.Fatal("conflicting mode initialized TOS")
				return nil, nil
			},
			newService: func(loguploader.Config, loguploader.ObjectUploader) (cliService, error) {
				t.Fatal("conflicting mode initialized service")
				return nil, nil
			},
		}
		errRun := runCLI(args, &bytes.Buffer{}, deps)
		if errRun == nil || !strings.Contains(errRun.Error(), "cannot be combined") {
			t.Fatalf("args %v conflict error = %v", args, errRun)
		}
		if loadCalls != 0 {
			t.Fatalf("args %v loaded config %d times before conflict rejection", args, loadCalls)
		}
	}
}
