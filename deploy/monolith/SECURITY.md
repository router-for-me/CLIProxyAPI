# Monolith deployment safety and validation scope

The eight files imported from the supplied patch retain their original content
and executable modes. They are manual deployment utilities, separate from the
Pinable Desktop six-platform runtime and release workflow. Merging them does not
run them against a server and does not publish an image or release.

## Before using a real host

The supplied defaults are intended for a trusted lab, **not a hardened production
service**. In particular:

- Replace both sample passwords. Password SSH and root SSH are enabled by the base
  image. Use a dedicated release key, verify and pin the host key, disable password
  and root SSH after provisioning, and never expose the sample credentials.
- The default published ports bind all host interfaces. Bind SSH and OAuth
  callbacks to loopback or restrict them at the host network boundary. The scripts
  accept host-qualified HTTP_PORT and SSH_PORT values, such as
  `SSH_PORT=127.0.0.1:58524`; review callback bindings separately.
- The supervisor socket is world-writable and controls a root-run service. Treat
  all container users, including deploy, as administrators. Restrict socket
  permissions and process privileges before permitting untrusted local users.
- The base image currently receives SSH host keys during package installation.
  Provision unique host keys per deployment rather than sharing image-baked keys.
- Paths, names, SSH_OPTS and release archives are trusted administrative inputs;
  they are not an untrusted-input API. Remote shell interpolation is not hardened
  for quotes or shell metacharacters. Check every path, use dedicated app/data/build
  directories, verify archive provenance and checksums, and keep backups before
  using `--rebuild`, `--recreate` or switching releases.
- Release switching uses removal plus symlink creation, not an atomic transaction
  or automatic rollback. The status helper is a process check, not an HTTP health
  check; the unsupervised fallback also does not report stopped state as failure.
  Independently verify `/healthz`, authenticated requests and the expected version.
- Build on a compatible Linux amd64 toolchain. The base image is Ubuntu 22.04
  amd64, while the package builder allows overrides. CGO builds made on newer
  distributions may not run on that base. Do not assume architecture overrides or
  arbitrary third-party plugins have been validated.

The README is the original deployment procedure. In v8, start from the current
configuration example and use its nested fields (for example `server.port`,
`credentials.auth-dir`, `access.api-keys`); legacy flat examples are compatibility
inputs. A successful v8 management write migrates the saved layout. The machine
executing the build script still needs Go and any required C toolchain; "no local
Go" in the procedure means the operator's workstation, not that build machine.

## Regression checks

`python3 scripts/pinable/test_monolith.py -v` uses temporary repositories and fake
Go/SSH/SCP commands. It checks shell syntax, executable modes, package contents,
flags, generated helpers, release ordering and failure propagation. It never
contacts a server and is not proof of real compilation or a real deployment.

The read-only `Pinable monolith check` CI separately builds the actual base image
and two real CGO-enabled Linux amd64 releases. Its Docker/SSH test binds only to
127.0.0.1, generates disposable passwords and a release key, obtains the host key
through Docker's local control channel, and requires strict SSH host-key checking.
It exercises the actual deploy script, supervisor restart, version/health/API
checks, a v8 configuration update, preservation of configuration and data across
a second release, and container restart. It removes only its randomly named test
container and image. It does not access the example LAN host or user credentials.

Run that integration test only on a disposable local Linux amd64 Docker host:

```bash
python3 scripts/pinable/smoke_monolith.py --run-disposable-docker \
  --report /tmp/monolith-smoke.json
```

Completed results are recorded in the PR and CI, not asserted by this document.
Passing these functional checks is not a security certification, a supplier OAuth
integration test, validation of arbitrary remote host setup, or a production rollout.
