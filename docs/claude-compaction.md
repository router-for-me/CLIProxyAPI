# Claude compaction bridge

Claude requests compaction through its normal messages endpoint. CPA translates
that request to Codex `/responses/compact`, stores the returned window on the
server, and returns a short opaque reference as the Claude text response. On
continuation CPA removes that reference from the messages and prepends the saved
window to Responses input. Encrypted state is never emitted in new Claude
conversation transcripts. Existing inline capsules remain readable.

Each compaction creates an immutable reference so simultaneous agents and branched
conversations cannot overwrite each other's windows. Credential selection uses
the normal CPA router, including when reading older capsules with an `auth_id`.

CPA reports estimated live usage but imposes no synthetic 200k rejection. Claude
uses its configured compaction window and the upstream enforces its context limit.

## Storage

Home deployments use shared KV keys under `cpa:claude:compaction:`. Standalone
servers use `WRITABLE_PATH/cliproxyapi/claude-compaction`, or the operating system's
user cache directory followed by `cliproxyapi/claude-compaction` when
`WRITABLE_PATH` is unset. Files have mode 0600 and are atomically published.

Persist this directory when replacing containers. For example, mount a persistent
volume at `/data` and set `WRITABLE_PATH=/data`. Standalone replicas serving the
same conversations must share that directory. Home instances must share KV.

Entries are limited to 8 MiB each and retained without automatic expiry because
old transcripts and conversation forks can still reference them. Include them in
backups; remove entries only when the corresponding conversations are no longer
needed. Storage usage grows with the number of compactions. A missing entry
returns an explicit error before inference instead of silently dropping context.
