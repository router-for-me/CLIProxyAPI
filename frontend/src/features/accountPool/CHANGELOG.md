# Changelog

## Summary
Implemented the foundation layer for the account pool frontend feature: shared TypeScript interfaces, a typed API service covering members, leaders, proxies, and groups, and client-side parsers for the `----`-delimited batch import text format.

## Completed Tasks
- Task 1: Created `accountPool.ts` API service with all required methods and exported interfaces
- Task 2: Added `export * from './accountPool'` to the services barrel `index.ts`
- Task 3: Created `parsers.ts` with `parseAccountLines` and `parseProxyLines` and their associated types

## Files Changed
- `frontend/src/services/api/accountPool.ts`: New file — typed API client for `/v0/management/account-pool` endpoints (members, leaders, proxies, groups) plus shared interfaces
- `frontend/src/services/api/index.ts`: Added `export * from './accountPool'` at the end of the barrel
- `frontend/src/features/accountPool/parsers.ts`: New file — `parseAccountLines` (3- or 4-field format) and `parseProxyLines` (1- or 2-field format) with `ParseError` reporting

## Verification
- `npx tsc --noEmit --project tsconfig.app.json 2>&1 | grep -E "(accountPool|parsers)"`: pass — zero errors from the new files; only pre-existing errors in unrelated files appear
- Manual check: All methods in `accountPoolApi` match the specified HTTP verbs and URL paths; interfaces match the specified field names and types; parser delimiter `----` and field-count logic matches specification

## Follow-ups
- None
