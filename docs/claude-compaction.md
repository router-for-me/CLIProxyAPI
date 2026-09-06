# Claude compaction bridge

Claude requests compaction through its normal messages endpoint. The bridge
translates the transcript and custom summary instructions into Codex input,
appends a compaction_trigger item, and dispatches through CPA's ordinary Responses
execution. CPA handles upstream routing, credentials, transport and response
assembly.

The bridge returns only the compaction item's encrypted_content as Claude text,
including for SSE clients. It adds no capsule wrapper or cache reference. On
replay, a complete Fernet-shaped ciphertext line is removed from the summary
message and restored as a Responses compaction input item. The newest raw block
replaces the preceding conversation window; messages after that boundary remain.
Quoted or inline ciphertext examples are left as ordinary text.

This recognition checks transport shape, not decryptability or origin. Raw
ciphertext carries no explicit type tag, so a standalone valid-looking ciphertext
line in ordinary prose is ambiguous and will be interpreted as compaction state.
The upstream validates the encrypted state.

Credential selection uses CPA's normal router. No auth ID is stored or pinned.
There is no synthetic 200k rejection; Claude uses its configured window and the
upstream enforces the model's actual context limit.

Compaction requires no server cache or persistent volume. Only raw ciphertext is
supported; legacy inline capsules and KV cache references are not decoded.

Three manual compact/resume cycles with real Luna OAuth output and unmodified
Claude Code 2.1.211 established that ciphertext text is replayed unchanged and each
replacement supersedes the previous block. This does not establish automatic
compaction or Claude Desktop behavior.
