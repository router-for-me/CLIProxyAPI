#!/usr/bin/env bash
set -euo pipefail

echo "[distributed-critical-paths] validating filesystem-sensitive paths"
go test -count=1 -run '^(TestMultiSourceSecret_FileHandling|TestMultiSourceSecret_CacheBehavior|TestMultiSourceSecret_Concurrency|TestAmpModule_OnConfigUpdated_CacheInvalidation)$' ./pkg/llmproxy/api/modules/amp

echo "[distributed-critical-paths] validating ops endpoint route registration"
go test -count=1 -run '^TestRegisterManagementRoutes$' ./pkg/llmproxy/api/modules/amp

echo "[distributed-critical-paths] validating compute/cache-sensitive paths"
go test -count=1 -run '^(TestEnsureCacheControl|TestCacheControlOrder|TestCountOpenAIChatTokens|TestCountClaudeChatTokens)$' ./pkg/llmproxy/runtime/executor

echo "[distributed-critical-paths] validating queue telemetry to provider metrics path"
go test -count=1 -run '^TestBuildProviderMetricsFromSnapshot_FailoverAndQueueTelemetry$' ./pkg/llmproxy/usage

echo "[distributed-critical-paths] validating signature cache primitives"
go test -count=1 -run '^(TestCacheSignature_BasicStorageAndRetrieval|TestCacheSignature_ExpirationLogic)$' ./pkg/llmproxy/cache

echo "[distributed-critical-paths] all targeted checks passed"
