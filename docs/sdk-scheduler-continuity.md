# Optional scheduler continuity

The optional `pluginapi.Capabilities.SchedulerContinuity` capability lets an active
scheduler return an immediate pre-admission decision for Codex requests. The host
owns the wait; plugins must not sleep inside this callback. Schedulers that do not
advertise this capability keep the existing admission and retry behavior.

## Contract

Implement `Continuity(context.Context, pluginapi.SchedulerContinuityRequest)` in
addition to the regular scheduler. RPC plugins advertise `scheduler_continuity`
and handle `scheduler.continuity`. Each request includes the lifecycle `request_id`,
current routing options, and current credential candidates. Candidate metadata
retains the public `auth_index`; it does not carry credential documents.

The response supports these actions:

- `ready`: continue through normal credential admission.
- `disabled`: stop continuity handling and use normal admission.
- `wait`: wait for `retry_after_ms`, then rebuild the candidate set and ask again.

A wait requires `max_wait_ms` between 1 second and 24 hours. Polling intervals are
clamped to 1 second through 5 minutes. The first wait establishes a request-wide
deadline. A later response may shorten that deadline but cannot extend it.
Cancellation ends the wait without sending another upstream attempt.

For post-429 recovery, `reason` must be one of the exported classification constants
`pluginapi.ContinuityReasonFiveHourExhausted` (`five_hour_exhausted`) or
`pluginapi.ContinuityReasonFiveHourRecheck` (`five_hour_recheck`). These values
positively classify an observed five-hour restriction. Other reasons may describe
pre-admission policy waits, but do not authorize replay of a rejected request.

The candidate set honors disabled credentials, request eligibility, model support,
and pinned credential identity. Current native quota cooldowns remain visible so
the scheduler can recheck them; unrelated authentication or overload failures do
not become quota-recovery candidates. CLIProxyAPIHome and non-Codex requests keep
their existing paths.

## Streaming and retry boundaries

During an actual pre-admission wait, Responses HTTP streams can send SSE comments
and Responses WebSockets can send pings. Enabling the capability alone does not
commit an HTTP response or start heartbeats. Ordinary non-streaming requests wait
without streaming output.

After a confirmed upstream 429 before usable output, the host briefly reconciles
asynchronous usage observations. It retries only after the scheduler positively
classifies five-hour exhaustion, waits, and subsequently returns `ready`. This
does not add replay after output has started and does not retry arbitrary errors.
An exhausted credential referenced by `previous_response_id` still requires the
existing reconnect/full-context replay path when its state cannot move safely.

The host supplies request lifecycle identity so admission decisions can be linked
to terminal callbacks. Quota policy, observation freshness, reservation accounting,
and weekly-only eligibility remain the scheduler's responsibility.
