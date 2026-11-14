# CLIProxyAPI standalone fork

This fork follows the official router-for-me CLIProxyAPI. Its supported build
adds the small DataCosmos routing publication contract and the provider behavior
required by verified consumers. The official v8 management API remains the
management surface. See [the API contract](docs/management-api-v8.md).

- **main** is an exact upstream mirror. The scheduled workflow on the default
  branch fast-forwards it only. If it contains fork history, preserve that
  history and reconcile the ref explicitly before declaring it a mirror.
- **dc-use** is the integration and release branch. Bring the selected upstream
  tag into it with a reviewed merge PR. Keep a named delta with its consumer and
  evidence, and delete it when the official code provides the same behavior.
- **contrib/** starts from the official base and carries only the proposed
  upstream change. Open those PRs against the official repository; never send
  the integration branch upstream.
- **super-fork/** holds third-party code imports and large experiments.
  Integration takes only proven, reviewed changes from these branches; the
  mirror does not take merges from them.

A stable fork build uses the actual integrated upstream release plus the next
local revision, such as `v8.0.71-dc1`. Record the upstream tag and its peeled
commit in `.fork-upstream.env`. Dispatch from the qualified `dc-use` tip;
the archive matrix and the fork GHCR image use the verified tagged commit.
The Linux GLIBC archives enable native plugins; the portable archives have a
`_no-plugin` suffix. The release dispatcher waits for the exact-tag GHCR image before starting
the archive workflow. Publish the archive set and its checksums from a draft
after the matrix passes. Set `CLI_PROXY_IMAGE` to the qualified tag or digest
when running Compose. Preserve
historical tags and divergent refs. Only adopt a new artifact after verifying
its published bytes and its behavior in the installed consumer.

OpenCode Zen and Go registration takes exact canonical IDs from each gateway's
public `/v1/models` catalog and confirms the configured wire protocol against
the per-model `api` declaration. The gateway may label a Responses route
`type: chat`; that label does not select the wire format. Catalog membership
allows discovery only: AI Hub must still qualify the actual route and
credential through inference before publication.

The routing inventory reports time-bound non-quota blocks as a suspension with
reason `cooldown` and a resume instant. It keeps the credential unselectable
during that interval without copying provider error text into the inventory.
Quota cooldown remains a quota fact.
