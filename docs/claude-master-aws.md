# Deploying claude-master on AWS

claude-master itself knows nothing about AWS: it is a binary driven by flags and files (see
[claude-master.md](claude-master.md)). This guide shows one way to run the **shared server** on AWS, with a
working Terraform example in [`examples/claude-master-aws`](../examples/claude-master-aws).

```
 client box ──TLS + client certificate──▶  claude-master server (one small EC2 box)  ──▶  api.anthropic.com
 (holds no login)                          holds the subscription logins + the CA key
                                                │ OTLP :4318 (loopback)          │ log file
                                                ▼                                ▼
                                         CloudWatch agent ──▶ CloudWatch metrics + Logs
```

**Why one box.** A subscription login rotates on every refresh and the previous access token dies at once, so a
login copied to several machines destroys itself within hours. One process owns each login; every other machine
talks to it. Clients hold no login and no password: they hold a short-lived certificate the server issued.

## What the example creates

| Resource | Purpose |
|---|---|
| EC2 instance, Graviton (`t4g.micro`, Ubuntu 24.04), fixed private IP | The server. 20 GB encrypted gp3 root that is **kept** when the instance is destroyed (it holds the logins and the CA key). IMDSv2 only. |
| Security group | The proxy port from your client CIDRs, **nothing else inbound** (no SSH rule: use Session Manager). |
| IAM role + instance profile | `AmazonSSMManagedInstanceCore`; optional CloudWatch publishing limited to one namespace and one log group; optional read of one secret (the paid API-key backup). |
| CloudWatch log group (optional) | The proxy's own log, redacted at the source. |
| Bootstrap (`user-data.sh.tftpl`) | Swap, a dedicated service account, the pinned binary (sha256-checked), Envoy (pinned, sha256-checked) in front of two server units (`claude-master-server@blue`, `@green`), the optional CloudWatch agent, and helpers: `claude-master-login`, `claude-master-sign`, `claude-master-status`, `claude-master-rollout`. |

**Restarts nobody notices.** Envoy listens on the port clients dial and passes TCP through, unchanged, to one of
two servers on the box, each on its own port of the same address (the server certificate names the address clients
dial; the two ports are not in the security group). `sudo claude-master-rollout` starts the idle server, points
Envoy's new connections at it, and stops the old one, which drains (`serve --balanced`): it keeps serving the
connections it has, each response closing its connection, so every client moves over on its next request without an
error, and running requests get up to `drain_seconds` (default 600) to finish. That is how a new binary goes live.

The service **stays idle until every profile has a login**, so the first apply creates a box that does nothing
until you finish step 3 below.

## Prerequisites

- A VPC and a subnet with outbound internet (a public subnet with `assign_public_ip = true`, or a private subnet behind
  a NAT gateway with `assign_public_ip = false`). The server reaches `api.anthropic.com` and GitHub releases; **nothing
  connects in from outside** either way.
- A free private IP in that subnet. Client certificates name this address, so it must not change.
- Terraform >= 1.5 and AWS credentials that can create the resources above.
- The AWS CLI plus the Session Manager plugin on the machine you administer from.
- One Claude subscription account per profile you want in the pool, and a browser you can sign in to each of them with
  (use a private window per account so you do not log the wrong one in).

## Deploy

### 1. Apply

```bash
cd examples/claude-master-aws
cp terraform.tfvars.example terraform.tfvars    # vpc_id, subnet_id, private_ip, client_cidrs
terraform init
terraform apply
```

`release_tag` and `binary_sha256` pin the binary: the bootstrap refuses a download that does not match. Defaults point
at a known release; to use another, take the sha256 of its `claude-master-linux-arm64` asset and set both.

### 2. Open a shell on the box

```bash
aws ssm start-session --target "$(terraform output -raw instance_id)"
sudo claude-master-status     # three lines "login <profile>: MISSING", server: inactive
```

### 3. Log each profile in (interactive, once)

```bash
sudo claude-master-login claude-1       # prints a claude.ai link
```

Open the link in a browser signed in to **that profile's** account, approve, and paste the `CODE#STATE` string back.
The login is its own OAuth login stored for the service account; nothing is copied from another machine. A profile
that already has a login is refused, so a failed attempt is safe to repeat. Repeat for each profile, then:

```bash
sudo claude-master-status        # every login: present
sudo claude-master-rollout       # starts the first server behind Envoy
sudo runuser -u claude-master -- env HOME=/var/lib/claude-master \
  claude-master probe claude-1 --model claude-haiku-4-5-20251001     # status=200 matched=true
```

The order of `profiles` is the fallback order: the first is preferred when quotas tie.

### 4. Enrol a client box

On the client, make a key (it never leaves that box) and a certificate request, and print the request as one line:

```bash
claude-master client-init --dir ~/.config/claude-master --name dev-box-1
base64 -w0 ~/.config/claude-master/client.csr; echo
```

On the server (a Session Manager shell), sign it. The request is public, so it is safe to paste:

```bash
echo '<the base64 line>' | sudo claude-master-sign 30 > /tmp/signed.pem     # 30 days; at most 90
cat /tmp/signed.pem        # two PEM blocks: the client certificate, then the CA certificate
```

Save the first block on the client as `~/.config/claude-master/client.pem` and the second as
`~/.config/claude-master/ca.pem` (the CA certificate is public, not a secret). Then:

```bash
claude-master connect --server <proxy_address> --dir ~/.config/claude-master -- --remote-control
```

Re-enrol before the certificate expires; a lost box expires on its own.

### 5. Check it

```bash
sudo tail -f /var/log/claude-master/server.log        # info lines: what CHANGES, plus a quota snapshot every 5 minutes
```

## Rolling the binary forward

Change `release_tag` and `binary_sha256`, then `terraform apply`. The instance ignores `user_data` changes on purpose, so a
rolled binary never replaces the machine that holds your logins. Run the new bootstrap on the box instead:

```bash
terraform output -raw bootstrap_script > bootstrap.sh
jq -Rn --rawfile s bootstrap.sh '{commands: [$s]}' > params.json
aws ssm send-command --document-name AWS-RunShellScript \
  --instance-ids "$(terraform output -raw instance_id)" --parameters file://params.json
```

The bootstrap is idempotent. It swaps the binary atomically and **never restarts a running server or Envoy**. Put the
new binary live with `sudo claude-master-rollout` (over Session Manager, or `aws ssm send-command` with
`commands=["claude-master-rollout"]`): the idle server starts on it, new connections move to it, and the old server
drains. No session notices. `claude-master-status` shows the active server and installed against pinned.

## Operating notes

- **Back up the root volume.** It holds the logins (three interactive logins to redo) and `state/ca.key` (losing it
  means re-enrolling every client). Add it to your backup plan.
- **The CA key never leaves** `/var/lib/claude-master/state`. Certificates are short-lived on purpose.
- **A revoked or expired login** shows as `profile credential rejected by Anthropic` in the log. The login command
  refuses an existing profile name; to replace one, log in under a new profile name and update `profiles`.
- **Never expose the proxy publicly.** `--listen` must be a loopback or private address (a public bind is refused), and
  the security group admits only your client CIDRs. For laptops outside your network use the loopback-only
  `--open-loopback` listener *behind something that authenticates* (a Cloudflare tunnel with an access policy, an SSM
  port forward); see [claude-master.md](claude-master.md#clients-that-arrive-through-a-tunnel-no-certificate).
- **Size.** A 0.5 GB instance was OOM-killed during its own first boot; keep `t4g.micro` or larger, and the swapfile.

## Metrics at a glance

The server exports OpenTelemetry metrics over OTLP/HTTP; the CloudWatch agent in the example receives them on
`127.0.0.1:4318` and publishes them in the `ClaudeMaster` namespace (change it with `metrics_namespace`). Anything that
speaks OTLP works instead: set `enable_cloudwatch = false` and point `--otlp-endpoint` at your collector. The full
catalogue, with dimensions, is in [claude-master.md](claude-master.md#metrics-opentelemetry); this is the map.

| Question | Metric (dimensions) | Look at |
|---|---|---|
| Is the pool healthy? | `claude_master.quota.used_fraction`, `.band`, `.resets_in_seconds` (`profile`) | each subscription's weekly allowance; band 0 ok, 1 reserve (90%), 2 exhausted |
| Who is using it? | `claude_master.inference.requests` (`profile`, `client_account`, `status_class`), `.by_client` (`client`, `client_account`), `.by_model` (`model`) | which subscription served which user, from which box, on which model |
| Is it fast? | `inference.ttfb`, `.upstream_ttfb`, `proxy.overhead` (histograms), `inference.duration_quantile` (`profile`, `quantile`) | Anthropic's own first-byte time against claude-master's added overhead; p50/p95/p99 gauges |
| Is it failing? | `inference.errors` (`profile`, `status`, `client_account`), `anthropic.ratelimit` (`profile`, `window`, `measure`), `quota.rate_limited` | Anthropic errors by status; every `Anthropic-Ratelimit-*` header (`5h`, `7d` windows) |
| Is routing working? | `routing.switches` (`from`, `to`, `reason`), `routing.picks`, `routing.backup_requests` | conversations moved between subscriptions and why; use of the paid API backup |
| Are the logins alive? | `auth.refresh` (`profile`, `result`), `auth.token_expires_in_seconds`, `usage.polls` | refresh failures are the early warning for a login that needs redoing |
| Who is connected? | `proxy.connections` (`listener`, `result`), `proxy.active_connections`, `proxy.tls_handshake_errors` | clients, and clients that were refused |
| Is the box OK? | `claude_master.process.*`, `mem_used_percent`, `swap_used_percent` | uptime, goroutines, heap, memory and swap |

Name your users instead of seeing `acct-xxxxxxxx`: `claude-master account-key <accountUuid>` prints a key, and
`/etc/claude-master/account-labels` takes `ACCOUNT_UUID=NAME` lines (the account id itself is never exported).

**Example CloudWatch Metrics Insights queries**

```sql
SELECT SUM("claude_master.inference.requests") FROM "ClaudeMaster" GROUP BY client_account   -- requests per user
SELECT SUM("claude_master.inference.requests") FROM "ClaudeMaster" GROUP BY profile          -- per subscription
SELECT MAX("claude_master.quota.used_fraction") FROM "ClaudeMaster" GROUP BY profile         -- allowance used
SELECT SUM("claude_master.inference.errors") FROM "ClaudeMaster" GROUP BY status             -- Anthropic errors
SELECT MAX("claude_master.inference.duration_quantile") FROM "ClaudeMaster" GROUP BY quantile
SELECT MIN("claude_master.auth.token_expires_in_seconds") FROM "ClaudeMaster" GROUP BY profile
```

Useful alarms: any subscription's `quota.used_fraction` above 0.9 for a sustained period, more than a handful of
`inference.errors` in ten minutes, `auth.refresh` with `result` other than ok, and `mem_used_percent` above 85.

**Cost shape.** CloudWatch bills every distinct combination of a metric's dimensions as its own custom metric (about
$0.30 a month each), and every OTLP attribute becomes a dimension. The proxy therefore never crosses its axes: each
metric carries at most three attributes and each axis (profile, user, box, model) has its own projection. Through the
CloudWatch agent, counters arrive as deltas (use `SUM`), histograms as approximate statistic sets with no percentiles
(use `duration_quantile`), and `service.name` is an extra dimension on every metric. Do not add an attribute to a request
metric without counting the series it multiplies. Model names and user accounts come from the client's request, so they
are bounded (64 models, 256 unlabelled accounts; the rest is `other`), and a client cannot create series by choosing a
name.

## Tear down

`terraform destroy` removes the instance, security group, role and log group but **keeps the root volume**
(`delete_on_termination = false`): it holds the logins and the CA key. Delete the volume yourself once you are sure you do
not need them.
