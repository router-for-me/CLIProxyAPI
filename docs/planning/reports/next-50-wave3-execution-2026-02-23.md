# Next 50 Wave 3 Execution (Items 21-30)

- Source batch: `docs/planning/reports/next-50-work-items-2026-02-23.md`
- Board updated: `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
- Scope: `CP2K-0051`, `CP2K-0052`, `CP2K-0053`, `CP2K-0054`, `CP2K-0056`, `CP2K-0059`, `CP2K-0060`, `CP2K-0062`, `CP2K-0063`, `CP2K-0064`

## Status Summary

- `implemented`: 7
- `in_progress`: 3 (`CP2K-0051`, `CP2K-0062`, `CP2K-0063`)

## Evidence Notes

- `CP2K-0052` (`#105`): auth file change noise handling evidence in watcher paths + lane reports.
- `CP2K-0053` (`#102`): incognito-mode controls and troubleshooting guidance present.
- `CP2K-0054` (`#101`): Z.ai `/models` path handling covered in OpenAI models fetcher logic/tests.
- `CP2K-0056` (`#96`): auth-unavailable docs/troubleshooting guidance exists.
- `CP2K-0059` (`#90`): token collision mitigation (`profile_arn` empty) is covered by synthesizer tests.
- `CP2K-0060` (`#89`): ValidationException metadata/origin handling evidenced in code/docs.
- `CP2K-0064` (`#83`): event stream fatal handling evidenced in lane docs and executor paths.
- `CP2K-0051`, `CP2K-0062`, `CP2K-0063`: partial evidence only; explicit proof slices still required.

## Commands Run

- `go test ./pkg/llmproxy/runtime/executor -run 'TestResolveOpenAIModelsURL|TestFetchOpenAIModels_UsesVersionedPath' -count=1` (blocked by local Go build cache file-missing error under `~/Library/Caches/go-build`)
- `go test ./pkg/llmproxy/watcher/synthesizer -run TestConfigSynthesizer_SynthesizeKiroKeys_UsesRefreshTokenForIDWhenProfileArnMissing -count=1` (blocked by same Go cache failure)
- `go test ./pkg/llmproxy/translator/kiro/openai -run TestBuildAssistantMessageFromOpenAI_DefaultContentWhenEmptyWithoutTools -count=1` (blocked by same Go cache failure)
