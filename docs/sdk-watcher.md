# SDK Watcher Integration

The SDK service exposes a watcher integration that surfaces granular auth updates without forcing a full reload. This document explains the queue contract, how the service consumes updates, and how high-frequency change bursts are handled.

## Update Queue Contract

- `watcher.AuthUpdate` represents a single credential change. `Action` may be `add`, `modify`, or `delete`, and `ID` carries the credential identifier. For `add`/`modify` the `Auth` payload contains a fully populated clone of the credential; `delete` may omit `Auth`.
- `WatcherWrapper.SetAuthUpdateQueue(chan<- watcher.AuthUpdate)` wires the queue produced by the SDK service into the watcher. The queue must be created before the watcher starts.
- The service builds the queue via `ensureAuthUpdateQueue`, using a buffered channel (`capacity=256`) and a dedicated consumer goroutine (`consumeAuthUpdates`). The consumer drains bursts by looping through the backlog before reacquiring the select loop.

## Watcher Behaviour

- `internal/watcher/watcher.go` keeps a shadow snapshot of auth state (`currentAuths`). Each filesystem or configuration event triggers a recomputation and a diff against the previous snapshot to produce minimal `AuthUpdate` entries that mirror adds, edits, and removals.
- Updates are coalesced per credential identifier. If multiple changes occur before dispatch (e.g., write followed by delete), only the final action is sent downstream.
- The watcher runs an internal dispatch loop that buffers pending updates in memory and forwards them asynchronously to the queue. Producers never block on channel capacity; they just enqueue into the in-memory buffer and signal the dispatcher. Dispatch cancellation happens when the watcher stops, guaranteeing goroutines exit cleanly.

## High-Frequency Change Handling

- The dispatch loop and service consumer run independently, preventing filesystem watchers from blocking even when many updates arrive at once.
- Back-pressure is absorbed in two places:
  - The dispatch buffer (map + order slice) coalesces repeated updates for the same credential until the consumer catches up.
  - The service channel capacity (256) combined with the consumer drain loop ensures several bursts can be processed without oscillation.
- If the queue is saturated for an extended period, updates continue to be merged, so the latest state is eventually applied without replaying redundant intermediate states.

## Usage Checklist

1. Instantiate the SDK service (builder or manual construction).
2. Call `ensureAuthUpdateQueue` before starting the watcher to allocate the shared channel.
3. When the `WatcherWrapper` is created, call `SetAuthUpdateQueue` with the service queue, then start the watcher.
4. Provide a reload callback that handles configuration updates; auth deltas will arrive via the queue and are applied by the service automatically through `handleAuthUpdate`.

Following this flow keeps auth changes responsive while avoiding full reloads for every edit.
