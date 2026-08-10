# Log Uploader Safe I/O Optimization Design

## Context

Production timing on 2026-08-10 showed that a representative uploader run spent about 61 seconds computing the archive SHA-256 after the archive was built, another 62 seconds re-reading the same archive before multipart upload, and about 118 seconds finalizing the successful upload and deleting local source data. The Codex normalization format is intentionally out of scope and must remain byte-for-byte compatible with the current implementation.

The current extra I/O exists for valid safety reasons:

- The prepared archive identity must be fixed before upload.
- A same-size archive replacement must be rejected before the first TOS request.
- Multipart requests must carry the MD5 digest of each part.
- Source logs must never be deleted if their content changed after archive construction.
- A crash during source or archive cleanup must converge safely after restart.
- The uploaded/sealed state must be durable before any local data is deleted.

This design removes redundant reads and state writes without weakening those invariants.

## Goals

1. Eliminate one complete post-build read of every compressed archive.
2. Reduce successful source cleanup from two complete reads per source file to one.
3. Combine non-critical cleanup state saves while preserving the mandatory pre-delete upload commit.
4. Preserve all existing archive, state, TOS, audit, and Supabase data formats.
5. Preserve same-size mutation detection, multipart Content-MD5, conflict recovery, and crash recovery.
6. Add stage-level timing and byte-count logs so production improvements are measurable.

## Non-Goals

- Do not change Codex JSONL normalization, including `metadata.stream.raw_sse`, field order, escaping, or double-encoded JSON strings.
- Do not change multipart concurrency, part size, retry count, liveness deadlines, or TOS endpoints.
- Do not change `state.json` schema or migrate existing state.
- Do not make Supabase delivery asynchronous in this change.
- Do not weaken source deletion checks or rely only on path, size, or modification time.
- Do not optimize the scheduler or `scanSources` in this change.

## Safety Invariants

The implementation must continue to satisfy all of the following:

1. The SHA-256 stored in `prepared_hours` is the digest of the compressed bytes produced by the archive builder.
2. The archive is re-opened and fully verified against the prepared size and SHA-256 immediately before upload.
3. Multipart part MD5 values are computed from the same final verified archive bytes used by upload section readers.
4. No TOS request is made if the prepared archive was replaced or changed, including a same-size replacement.
5. The sealed hour, uploaded object, uploaded source list, and Supabase outbox entry are durably committed before source or local archive deletion starts.
6. A source file is deleted only after a full SHA-256 check of the isolated pending file matches the prepared source identity.
7. Any mismatch, read error, rename error, or uncertain path state retains data and returns an error instead of deleting it.
8. Every crash point is restart-safe and cannot cause an already sealed hour to be uploaded again.

## Design 1: Compute Compressed Archive SHA-256 While Writing

### Current flow

The archive builder writes compressed bytes to disk and closes and syncs the file. `processBatch` then opens the completed archive and reads every byte with `fileSHA256`. Later, the TOS uploader opens the archive and reads every byte again to verify the prepared SHA-256 and calculate multipart MD5 values.

### New flow

The archive builder will write Zstandard output through a writer that sends the exact compressed byte stream to both:

- the destination archive file; and
- a SHA-256 hash.

After the encoder closes and the file and parent directory are synchronized, the builder returns an archive result containing:

- archive path;
- JSONL byte count;
- compressed byte count;
- compressed archive SHA-256.

`processBatch` will use that returned SHA-256 directly when constructing `preparedHour`. It will no longer call `fileSHA256` after `buildArchive`.

The TOS `openVerifiedArchive` path remains unchanged. It still performs the final pre-upload full read, verifies the prepared size and SHA-256, and computes all multipart part MD5 values before the first TOS request. This final read is necessary because a normal mutable filesystem cannot prove that a closed path was not replaced after archive construction.

### Integrity checks

- The compressed byte count returned by the writer must equal the final file size.
- A failure from the destination writer, encoder close, file sync, file close, rename, directory sync, or stat aborts the batch.
- If the path changes after archive construction, the unchanged TOS verification rejects it before upload.
- Dry-run archives use the same write-through checksum path so the builder has one behavior.

### Logging

The existing `archive built` and `archive checksum computed` message text remains available for operational filters. The checksum log will identify that the digest was produced during archive writing and will no longer represent a separate full-file scan. The archive build log will include checksum bytes and total build duration.

## Design 2: Rename First, Then Verify a Source Once

### Current flow

For every successful source cleanup, the uploader:

1. hashes the original source path;
2. renames it to a deterministic `delete-pending` path;
3. hashes the pending path again; and
4. unlinks it.

The two paths refer to the same inode on a same-filesystem rename, so the successful path reads all source bytes twice.

### New flow

For a source without an existing pending file:

1. Resolve and validate the original and deterministic pending paths.
2. Stat the original file.
3. If size or modification time already differs from the uploaded identity, remove only the stale uploaded-source state entry and leave the source file for a future archive.
4. Create the pending directory and atomically rename the source to the pending path.
5. Record the pending path in the in-memory uploaded-source entry.
6. Open the pending file, hash it once, and verify size and modification time before and after the read.
7. If the identity matches, unlink the pending file and remove the uploaded-source state entry.
8. If the identity does not match, restore the pending file to its original path when the original path is vacant, remove the stale uploaded-source entry, and let the file be reprocessed.
9. If restoration is unsafe because the original path now exists, retain the deterministic pending file and state entry and return an error.

For a pending file recovered after restart, the uploader starts directly at step 6. It never needs to reconstruct or trust an arbitrary pending path.

### Why rename-first is safe

The original source path is not trusted as proof of identity. Rename only isolates the exact filesystem object that will be verified. A replacement that appeared between archive construction and cleanup is moved to the pending path but fails the SHA-256 check and is not deleted. A writer that changes the pending inode during hashing is detected by the existing before/after size and modification-time checks. The final unlink occurs only after the full SHA-256 matches.

The settled-file rule remains the operational protection against a writer appending after the final check, which is the same assumption used by the current second pending-path hash.

## Design 3: Preserve One Mandatory Commit and Combine Cleanup Saves

### Mandatory durable commit

The uploader continues to save the complete sealed upload state before any cleanup:

- remove the prepared hour;
- add the sealed hour;
- add the uploaded object;
- add all uploaded source identities;
- persist the Supabase outbox event when enabled.

The save still uses the existing temporary file, file sync, rename, and parent-directory sync sequence. If it fails before publication, no local source or archive is deleted.

### Combined cleanup commit

After the mandatory commit:

1. Run source cleanup and mutate the in-memory uploaded-source map.
2. Run local archive cleanup and clear archive paths in memory.
3. Save the resulting state once if either cleanup changed state.
4. Append the final audit record.

The current separate save after source cleanup and separate save after archive cleanup are replaced by this single combined save.

If the process crashes after physical deletion but before the combined save, the mandatory sealed state prevents re-upload. On restart:

- a missing original and pending source removes the stale uploaded-source entry;
- a deterministic pending source resumes verification and deletion;
- a missing local archive clears its stored archive path;
- the converged state is saved normally.

Supabase delivery remains synchronous and keeps its existing outbox and error semantics. Reordering or background delivery is deferred to a separate design because it changes scheduling and operational behavior.

## Crash and Failure Behavior

| Failure point | Durable state | Filesystem state | Recovery behavior |
|---|---|---|---|
| Archive write or sync fails | Hour not prepared | Temporary archive may exist | Batch fails; temporary archive cleanup follows existing behavior |
| Archive changes after build | Prepared SHA describes built bytes | Archive path has different bytes | Final TOS verification rejects before any request |
| Mandatory upload-state save fails | Prepared hour remains unless publication occurred | Sources and archive remain | No deletion; existing save-result logic decides in-memory rollback |
| Crash before source rename | Sealed upload is durable | Original source remains | Retry stages and verifies it |
| Crash after rename, before hash | Sealed upload is durable | Deterministic pending file exists | Restart detects pending path and hashes once |
| Pending hash mismatch | Sealed upload is durable | Pending file retained or safely restored | Data is retained and returned for reprocessing/manual recovery |
| Crash after source unlink, before combined save | Sealed upload is durable | Source is absent | Restart removes stale uploaded-source cleanup metadata |
| Crash after archive unlink, before combined save | Sealed upload is durable | Local archive is absent | Restart clears stale archive path |
| Combined cleanup save fails | Sealed upload is durable | Some cleanup may already be complete | Run reports cleanup error; restart converges idempotently |

## Observability

Add structured durations and byte counts without logging paths, tokens, checksums, or other sensitive values:

- archive write/checksum duration and compressed bytes;
- final archive verification duration and verified bytes;
- source cleanup duration, verified bytes, staged count, deleted count, restored count, and error count;
- mandatory uploaded-state commit duration;
- Supabase delivery duration and sanitized result category;
- archive cleanup duration;
- combined cleanup-state save duration;
- total post-upload finalize duration.

These fields must make it possible to compare production runs before and after deployment without changing existing message text used by log filters.

## Testing Strategy

All implementation work follows test-driven development. Tests must fail for the intended missing behavior before production code changes.

### Archive tests

- The SHA-256 returned by archive construction equals `fileSHA256` of the completed compressed file.
- Compressed byte count equals the final file size.
- A same-size archive replacement after construction is still rejected before the first TOS request.
- Multipart Content-MD5 tests, object metadata tests, 409/412 conflict recovery tests, and prepared resume tests remain unchanged and pass.
- Archive write, encoder close, sync, rename, and stat failures do not produce a prepared hour.

### Source cleanup tests

- Successful source cleanup performs exactly one complete checksum read.
- A same-size, same-mtime content replacement is staged, detected, and never deleted.
- A changed size or modification time is left at the original path for reprocessing without a checksum read.
- Crash-equivalent state with only a deterministic pending file resumes safely.
- Pending mismatch restores the source when safe.
- Pending mismatch with a newly occupied original path retains both files and reports an error.
- Missing original and pending files converge by removing stale cleanup metadata.

### State and crash tests

- No source or archive deletion occurs when the mandatory upload-state commit fails before publication.
- Source and archive cleanup cause at most one final state save.
- Failure of the combined cleanup save leaves a restart-safe sealed state.
- Restart after physical source and archive deletion converges without re-upload.
- Audit statuses and Supabase outbox contents retain their current schema and meaning.

### Verification commands

- `gofmt -w .`
- focused `go test` commands for `internal/loguploader` tests added or changed by each TDD cycle;
- `go test ./...`;
- `go build -o test-output ./cmd/server && rm test-output`.

## Expected Performance Impact

For each uploaded archive, one complete compressed-file read is removed. Based on the observed production run, this should remove about 61 seconds from a batch of similar size.

For source cleanup, successful verified bytes fall from approximately `2 × SourceBytes` to `1 × SourceBytes`. The exact wall-clock improvement depends on raw source size, file count, filesystem cache, and Supabase latency, but the disk-read component should be approximately halved. Combining cleanup state saves removes one complete JSON serialization and durability cycle per successful batch.

## Rollout

1. Run focused unit tests and the full repository suite.
2. Build the server using the repository-required compile check.
3. Deploy without changing uploader configuration or state.
4. Compare stage durations and verified byte counts for at least two completed hourly batches.
5. Roll back the binary normally if unexpected behavior occurs; no state migration or archive-format rollback is required.
