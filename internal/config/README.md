# Config file lifecycle

`server.server-id` identifies a deployment and is not emitted by `Config` JSON.
An explicit value must be a nonzero hyphenated UUID; startup trims whitespace
and persists lowercase. Missing, null, and blank values produce a random UUIDv4
on the first valid load. Adding the ID alone does not migrate a legacy file. If
startup can read but not write the file, it uses a process-local ID for that
file and reuses it on subsequent loads. If the same file becomes writable, the
next load persists that ID. Set an explicit value when a read-only deployment
needs a stable ID across process restarts.

The first YAML document supplies runtime configuration. The whole YAML stream
must decode, and rewrites keep subsequent documents byte-for-byte. Optional
startup treats missing files, directories, and YAML decoding failures (including
empty/null documents and malformed credential weights) as standby, without an
ID or source hash. Runtime validation errors such as a management secret longer
than bcrypt's 72-byte limit still fail startup.

## File operations

- `ReadConfigFile(path string) ([]byte, error)` reads raw bytes under a shared lock.
- `ReadConfigFileSnapshot(path string) (*ConfigFileSnapshot, error)` also captures
  the original file identity. Use `snapshot.Bytes()` to get an editable copy and
  `snapshot.SourceHash()` for an ETag or watcher comparison.
- `WriteConfigFileCAS(path string, expected *ConfigFileSnapshot, replacement []byte) (*ConfigFileSnapshot, error)`
  compares file identity and the **complete** original bytes under an exclusive
  lock. The destination must already exist. On `ErrConfigFileChanged`, discard
  the prepared change or re-evaluate it against a fresh read; do not blindly retry
  the same replacement. The result describes the committed bytes, so there is no
  need to reopen the file to compute an ETag. CAS rejects a replacement that
  removes an identifying plaintext management setting but leaves a copy of its
  value. Callers still own general validation of the replacement.
- `CreateConfigFile(path string, data []byte) error` publishes complete bytes at
  a missing path. It returns `ErrConfigFileChanged` if another writer created it.
- `RemoveConfigFileCAS(path string, expected *ConfigFileSnapshot) error` removes
  a file only while it still matches the snapshot's identity and bytes.
- `EnsureServerIDInConfigBytesWithOptional(data []byte, optional bool) ([]byte, string, error)`
  applies startup validation and sets only the first document's ID. The strict
  `EnsureServerIDInConfigBytes` variant omits the optional argument. This is a
  pure byte transformation, not a persistence or upload operation. Preserve an
  existing configured ID in a replacement document if the workflow requires the
  deployment to retain that identity. Optional standby returns `(nil, "", nil)`.
- `PrepareConfigFileForUploadWithOptional(path string, optional bool) ([]byte, error)`
  performs startup rewrites and validates the snapshot for upload. Candidates
  come from both management secret paths in every document, including shadowed
  merge definitions. The check looks for substrings in the complete YAML source
  and decoded scalars, including aliases, escaped and binary values, and
  environment expansions. Only recognized management setting names, bcrypt
  values at those paths, and YAML nulls are exempt where their source spelling
  can be identified. The check fails closed: even a coincidental match for a
  short key in other text can block upload or a save that would remove the only
  identifying setting. Use a longer unique key and remove conflicting copies
  before retrying. Startup retains that identifying setting if it cannot safely
  hash it on disk, so later uploads still reject the plaintext. Validation
  cannot identify unrelated secrets without an original plaintext candidate.
  Upload exactly the returned bytes; do not re-read the file after verification.
  `(nil, nil)` is optional standby and must not be uploaded. A required rewrite
  on a read-only file is an error.

- `PrepareConfigBytesForUploadWithOptional(data []byte, optional bool) ([]byte, error)`
  performs the same preparation on remote or template bytes without writing a
  speculative change to the mirror. `VerifyConfigBytesForUpload(data []byte) error`
  validates an immutable artifact, such as a Git commit blob, without changing it.
- `ParsePreparedConfigBytes(data []byte) (*Config, error)` loads the prepared
  snapshot without reopening its backing file and records its source hash. It
  returns the standby configuration if the prepared snapshot is nil.

`ComputeSourceHash(data []byte) string` is lowercase hex SHA-256 of the exact
bytes, including any separator, trailing document, BOM, or newline. A successful
`LoadConfigOptional` records the committed snapshot's hash (the original hash if
read-only), not the hash from before a successful startup rewrite.

Management v8 validates the full YAML stream before editing the first document
with `DecodeFirstConfigDocument` and `ReplaceFirstConfigDocument`. The read view
uses `ProjectConfigServerID` to include an available ephemeral identity without
writing the file. A persisted identity, or a valid explicit identity accepted on
load, retires the ephemeral entry. Unlike startup, a v8 write uses
`EnsureManagementServerID` to replace an invalid identity before validation and
commit; other configuration errors still reject the write.

Existing files are updated in place through an `O_RDWR` descriptor without
replacing the inode. Writers check path identity before and after the write,
sync, and attempt to restore the original snapshot on failure. Process-local and
OS locks are cooperative; callers must use these helpers rather than truncating,
renaming, or independently reading the file mid-transaction. This matters on
AIX and Solaris in particular, where closing another descriptor for that same
inode in the process can release its `fcntl` lock. No lock protects against an
editor or another program that ignores the locking protocol.

## Remote mirrors

PostgreSQL and S3 seed missing configs conditionally. Each instance uses the
exact body (PostgreSQL) or the strong response ETag (S3) from its last successful
sync or publish to guard the next save. The S3 endpoint must return a strong
ETag on GET and successful PUT responses. If another instance has changed the
remote config, a stale save reports a conflict instead of overwriting it. Keep a
copy of the local edits, resynchronize, and reapply the edits before saving again.

Startup reloads the winner after a rejected conditional write. If another
instance already wrote the exact winning bytes to a shared mirror, startup
accepts it without rewriting. If a concurrent local edit differs from the winner,
even in a trailing YAML document, startup reports the conflict and leaves the
file untouched.

PostgreSQL keeps the config column as `TEXT`. It accepts only UTF-8 YAML without
NUL bytes and rejects incompatible bytes before publishing. Convert UTF-16 YAML
to UTF-8 before using it with the PostgreSQL store. The store does not transcode
files because the published config and its source hash identify exact bytes.

Git performs startup rewrites only after synchronizing and verifies the actual
candidate commit blob before publishing. A rejected startup candidate never
touches the working config. The runtime config's source hash identifies the
exact winning bytes, not a re-read of a potentially edited file.
