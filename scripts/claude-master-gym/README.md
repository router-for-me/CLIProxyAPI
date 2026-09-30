# Claude Master two-lane manual gym

This gym runs two independent native Claude Code sessions in one tmux session.
Each lane uses the exact `claude-sonnet-5-5` model, receives `/effort ultracode`
as an interactive slash command, and is asked to fan out to exactly two
read-only subagents. The launchers enable Claude Master's numeric diagnostics,
native debug files, and terminal capture so routing, streaming, and subagent
behavior can be inspected without printing credentials.

The two lanes must use disjoint Claude Master profile lists. A launcher holds an
exclusive lock on every profile in its list, so sharing a profile between lanes
would be both ambiguous and impossible. With one profile per lane, the lane-to-
subscription mapping is exact. With fallbacks, the gym records the eligible set
and Claude Master selects within that lane using its quota policy.

## One-time setup

Build/login commands create ordinary, separate Claude OAuth subscription
profiles. Every `login` opens the normal Anthropic authorization flow; do not
copy native tokens or credential files.

```bash
cd /home/ubuntu/CLIProxyAPI-claude-master
go run ./cmd/claude-master login claude-gym-a
go run ./cmd/claude-master login claude-gym-b
```

The gym will stop at preflight with a concrete second-login message when only
one profile exists. It reads profile directory names to perform that check, not
their credential contents.

Check whether the native `claude` on `PATH` is the current official release:

```bash
./scripts/claude-master-gym/claude-version.sh
```

This command never updates anything. Updating is a separate, explicit action:

```bash
./scripts/claude-master-gym/claude-version.sh --update
```

## Start and drive both lanes

The shortest invocation supplies the two primary profiles directly:

```bash
./scripts/claude-master-gym/start.sh claude-gym-a claude-gym-b
```

For per-lane fallback pools, copy `gym.env.example` outside version control,
edit only the profile names, and pass it explicitly:

```bash
cp scripts/claude-master-gym/gym.env.example /tmp/claude-master-gym.env
./scripts/claude-master-gym/start.sh --config /tmp/claude-master-gym.env
```

The same settings can be provided directly in the environment as comma-
separated lists:

```bash
CLAUDE_MASTER_GYM_LANE_A_PROFILES=claude-a,claude-a-fallback \
CLAUDE_MASTER_GYM_LANE_B_PROFILES=claude-b,claude-b-fallback \
  ./scripts/claude-master-gym/start.sh
```

`start.sh` builds `./cmd/claude-master` from this checkout into a private run
directory, verifies that the installed native client matches the official latest
version, runs the launcher preflight, starts windows `lane-a` and `lane-b`, waits
for both native clients, sends `/effort ultracode`, then submits the bounded
fanout prompt. It intentionally does not use `claude --effort ultracode`: the
current CLI flag accepts only its documented flag values, while `ultracode` is
an interactive session mode.

If Claude Code displays a workspace-trust prompt, the driver does not answer it
for you. Review it in tmux, accept it yourself, and rerun `drive.sh`:

```bash
tmux attach -t claude-master-gym
./scripts/claude-master-gym/drive.sh
```

## Observe and stop

```bash
./scripts/claude-master-gym/status.sh
./scripts/claude-master-gym/inspect.sh
tmux attach -t claude-master-gym
./scripts/claude-master-gym/stop.sh
```

`status.sh` shows the two lane processes and their disjoint routing boundaries.
`inspect.sh` prints sanitized terminal evidence and only aggregate counts from
native debug logs; it never dumps raw debug records. Look for `GYM_LANE_A_OK`
and `GYM_LANE_B_OK`, two subagent results per lane, streamed terminal progress,
and nonzero Claude Master inference dispatch counters. `stop.sh` ends both
sessions but retains the private run directory named in its output.

Do not put tokens in the config, prompts, or repository. The gym never reads,
copies, or prints profile credential files.
