#!/usr/bin/env python3
"""Run the packaged executable on its real target OS, without provider credentials."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import re
import secrets
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

sys.path.insert(0, str(Path(__file__).parent))
import runtime


def request(port, path, key="", method="GET", data: bytes | None = None, include_headers=False):
    headers = {"Authorization": "Bearer " + key} if key else {}
    if data is not None:
        headers["Content-Type"] = "application/json"
    elif method == "POST":
        data = b""
    req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", headers=headers, data=data, method=method)
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with client.open(req, timeout=2) as response:
            result = (response.status, response.read())
            return (*result, response.headers) if include_headers else result
    except urllib.error.HTTPError as error:
        result = (error.code, error.read())
        return (*result, error.headers) if include_headers else result


def discovery_probe(command, cwd, env, allow_unavailable_multicast=False):
    result = subprocess.run(command, cwd=cwd, env=env, timeout=15, capture_output=True)
    if result.returncode != 0:
        # Hosted macOS runners can have no multicast route. Only this exact
        # transport limitation is eligible; malformed JSON and all other
        # discovery failures must remain fatal. Production behavior is unchanged.
        if allow_unavailable_multicast and result.returncode == 1:
            payload = json.loads(result.stdout)
            error = payload.get("error") if isinstance(payload, dict) else None
            prefix = "discovery: send browse queries: "
            if (isinstance(error, str) and error.startswith(prefix)
                    and error[len(prefix):]
                    and set(payload) == {"error", "gateways"} and payload["gateways"] == []
                    and all(re.fullmatch(r"interface [^:]+: write udp .+: sendmsg: no route to host", line)
                            for line in error[len(prefix):].splitlines())):
                return payload
        raise ValueError(
            f"discovery probe failed ({result.returncode}): "
            f"stdout={result.stdout.decode('utf-8', errors='replace')!r}; "
            f"stderr={result.stderr.decode('utf-8', errors='replace')!r}"
        )
    return json.loads(result.stdout)


def wait_until(predicate, process, description, seconds=25):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        runtime.require(process.poll() is None, "runtime exited before " + description)
        try:
            if predicate():
                return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.1)  # Bounded integration readiness polling, not a unit-test clock.
    raise ValueError("timed out waiting for " + description)


def stop(process):
    if process is not None and process.poll() is None:
        process.kill()
        process.wait(timeout=10)


def build_darwin_plugin(root: Path, arch: str, env):
    """Compile only the checked-in, dependency-free C ABI demo; never fetch plugins."""
    runtime.require(arch in ("amd64", "arm64"), "unsupported Darwin plugin architecture")
    source = Path(__file__).resolve().parents[2] / "examples/plugin/management-api/c/src/plugin.c"
    runtime.require(source.is_file(), "trusted repository management-api C demo is missing")
    plugin_dir = root / "plugins" / "darwin" / arch
    plugin_dir.mkdir(parents=True)
    library = plugin_dir / "management-api-c.dylib"
    native_arch = {"amd64": "x86_64", "arm64": "arm64"}[arch]
    subprocess.run(["/usr/bin/xcrun", "clang", "-dynamiclib", "-arch", native_arch,
                    "-o", str(library), str(source)], check=True, env=env, timeout=60)
    result = subprocess.check_output(["/usr/bin/lipo", "-archs", str(library)],
                                     env=env, text=True, timeout=10)
    runtime.require(result.strip().split() == [native_arch], "demo plugin architecture mismatch")
    subprocess.run(["/usr/bin/codesign", "--force", "--sign", "-", str(library)],
                   check=True, env=env, timeout=15)
    subprocess.run(["/usr/bin/codesign", "--verify", "--strict", str(library)],
                   check=True, env=env, timeout=15)
    return library


def verify_plugin_response(code, body, headers):
    runtime.require(code == 200 and headers.get("X-CPA-SUPPORT-PLUGIN") == "1",
                    "Darwin release must advertise native plugin support")
    listing = json.loads(body)
    entries = listing.get("plugins", [])
    matches = [entry for entry in entries if entry.get("id") == "management-api-c"]
    runtime.require(listing.get("plugins_enabled") is True and len(matches) == 1,
                    "isolated demo plugin not discovered")
    plugin = matches[0]
    runtime.require(plugin.get("registered") is True and plugin.get("effective_enabled") is True
                    and (plugin.get("metadata") or {}).get("name") == "example-management-api-c",
                    "demo plugin was not actually registered and enabled")


def smoke_darwin_plugin(binary: Path, root: Path, arch: str, env):
    """Exercise the staged release binary, not a separately rebuilt Go host."""
    case = root / "native-plugin"
    case.mkdir()
    build_darwin_plugin(case, arch, env)
    key = secrets.token_hex(24)
    api_key = secrets.token_hex(24)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    cfg = case / "config.yaml"
    cfg.write_text(
        f'host: "127.0.0.1"\nport: {port}\nauth-dir: "auths"\n'
        f'api-keys: ["{api_key}"]\nremote-management:\n  allow-remote: false\n'
        f'  secret-key: "{key}"\n  disable-control-panel: true\n  disable-auto-update-panel: true\n'
        'discovery:\n  enabled: false\nplugins:\n  enabled: true\n  dir: "plugins"\n'
        '  configs:\n    management-api-c:\n      enabled: true\n'
        'logging-to-file: false\nusage-statistics-enabled: false\n', encoding="utf-8")
    process = None
    try:
        with (case / "process.log").open("wb") as log:
            process = subprocess.Popen([str(binary), "--config", str(cfg), "--local-model",
                                        "--parent-pid", str(os.getpid())], cwd=case, env=env,
                                       stdout=log, stderr=subprocess.STDOUT)
            wait_until(lambda: request(port, "/v8/management/plugins", key)[0] == 200,
                       process, "native plugin management endpoint")
            verify_plugin_response(*request(port, "/v8/management/plugins", key, include_headers=True))
            runtime.require(request(port, "/v8/management/plugins")[0] in (401, 403),
                            "plugin management must remain authenticated")
            code, body = request(port, "/v0/resource/plugins/management-api-c/status", key)
            runtime.require(code == 200 and b"<main>Management API resource</main>" in body,
                            "loaded demo plugin resource callback did not execute")
            runtime.require(request(port, "/v0/management/runtime-shutdown", key, "POST")[0] == 202,
                            "plugin fixture shutdown rejected")
            process.wait(timeout=15)
            runtime.require(process.returncode == 0, "plugin fixture did not exit cleanly")
    finally:
        stop(process)


def smoke(artifact_dir: Path, allow_unavailable_multicast=False):
    manifests = list(artifact_dir.glob("*.manifest.json"))
    runtime.require(len(manifests) == 1, "expected one native platform artifact")
    manifest, _ = runtime.verify(manifests[0])
    target = {"Windows": "windows", "Darwin": "darwin", "Linux": "linux"}[platform.system()]
    arch = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64", "ARM64": "arm64"}[platform.machine()]
    runtime.require(manifest["target"] == target + "/" + arch, "smoke must run on the native target architecture")
    checks = []
    dynamic_library_plugins_tested = False
    with tempfile.TemporaryDirectory(prefix="pinable-runtime-smoke-") as tmp:
        root = Path(tmp)
        binary = root / runtime.binary_name(manifest["target"])
        runtime.stage(manifests[0], binary, manifest["source"]["commit"], manifest["binary"]["sha256"])
        isolated = root / "empty"
        isolated.mkdir()
        env = {key: value for key, value in os.environ.items()
               if not key.upper().startswith(("CLIPROXY_", "PGSTORE_", "GITSTORE_", "OBJECTSTORE_"))
               and key.upper() not in ("MANAGEMENT_PASSWORD", "DEPLOY")}
        for flag in ("--version", "-version"):
            output = subprocess.check_output([str(binary), flag], cwd=isolated, env=env, timeout=10, text=True)
            runtime.require(manifest["version"] in output and manifest["source"]["commit"] in output,
                            "version probe metadata mismatch")
            runtime.require(len(output.splitlines()) == 1 and not list(isolated.iterdir()),
                            "version probe must not start services or create files")
        checks.append("both version flags without configuration")
        for args in (("discover", "--json", "--timeout", "1"),
                     ("--discover-json", "--discover-timeout", "1")):
            result = discovery_probe([str(binary), *args], cwd=isolated, env=env,
                                     allow_unavailable_multicast=allow_unavailable_multicast)
            if isinstance(result, dict) and "error" in result:
                checks.append("discovery error JSON verified; real LAN scan not verified: " + result["error"])
        checks.append("new and legacy discovery JSON")
        if target == "darwin":
            subprocess.run(["codesign", "--verify", "--strict", str(binary)], check=True)
            checks.append("ad-hoc signature verified after extraction")
            smoke_darwin_plugin(binary, root, arch, env)
            dynamic_library_plugins_tested = True
            checks.append("native same-architecture demo dylib registered and resource callback executed; support header 1")

        for test_parent_exit in (False, True):
            case = root / ("parent-exit" if test_parent_exit else "management")
            case.mkdir()
            management_key = secrets.token_hex(24)
            ephemeral_key = secrets.token_hex(24)
            configured_key = secrets.token_hex(24)
            reloaded_key = secrets.token_hex(24)
            v8_key = secrets.token_hex(24)
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                port = sock.getsockname()[1]
            cfg = case / "config.yaml"
            def configuration(key):
                return (f'host: "127.0.0.1"\nport: {port}\nauth-dir: "auths"\n'
                        f'api-keys: ["{key}"]\nremote-management:\n'
                        f'  allow-remote: false\n  secret-key: "{management_key}"\n'
                        '  disable-control-panel: true\n  disable-auto-update-panel: true\n'
                        'discovery:\n  enabled: false\nplugins:\n  enabled: false\n'
                        'logging-to-file: false\nusage-statistics-enabled: false\n')
            cfg.write_text(configuration(configured_key), encoding="utf-8")
            child_env = dict(env, CLIPROXY_EPHEMERAL_API_KEY=ephemeral_key)
            parent = process = None
            try:
                if test_parent_exit:
                    parent = subprocess.Popen([sys.executable, "-c", "import threading; threading.Event().wait()"],
                                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                parent_pid = parent.pid if parent else os.getpid()
                with (case / "process.log").open("wb") as log:
                    process = subprocess.Popen([str(binary), "--config", str(cfg), "--local-model",
                                                "--parent-pid", str(parent_pid)],
                                               cwd=case, env=child_env, stdout=log, stderr=subprocess.STDOUT)
                    wait_until(lambda: request(port, "/v0/management/runtime-info", management_key)[0] == 200,
                               process, "authenticated runtime-info")
                    code, body = request(port, "/v0/management/runtime-info", management_key)
                    info = json.loads(body)
                    runtime.require(info.get("contract_version") == "1" and info.get("pid") == process.pid
                                    and info.get("commit") == manifest["source"]["commit"]
                                    and info.get("component_version") == manifest["version"]
                                    and info.get("build_time") == manifest["source"]["commit_time"],
                                    "host contract metadata mismatch")
                    code, config_body = request(port, "/v8/management/config", management_key)
                    runtime.require(code == 200 and isinstance(json.loads(config_body), dict),
                                    "v8 management center config endpoint unavailable")
                    for unauthorized_key in ("", ephemeral_key):
                        runtime.require(request(port, "/v8/management/config", unauthorized_key)[0] in (401, 403),
                                        "v8 config management authentication was bypassed")
                    # A successful management request resets the failed-attempt
                    # counter between independent negative-authentication groups.
                    # Otherwise this group plus the existing host-control checks
                    # legitimately reaches the five-failure IP ban threshold.
                    runtime.require(request(port, "/v8/management/config", management_key)[0] == 200,
                                    "valid v8 management key rejected after negative auth checks")
                    checks.append("authenticated v8 management center config endpoint returns JSON")
                    for key in ("", ephemeral_key):
                        runtime.require(request(port, "/v0/management/runtime-info", key)[0] in (401, 403),
                                        "management authentication was bypassed")
                    runtime.require(ephemeral_key.encode() not in body and management_key.encode() not in body,
                                    "runtime metadata leaked credentials")
                    runtime.require(request(port, "/v1/models")[0] in (401, 403), "public API must require a key")
                    runtime.require(request(port, "/v1/models", ephemeral_key)[0] == 200, "runtime key rejected")
                    runtime.require(request(port, "/v1/models", configured_key)[0] == 200, "config key rejected")
                    runtime.require(request(port, "/v0/management/runtime-shutdown", "wrong", "POST")[0] in (401, 403),
                                    "unauthenticated shutdown accepted")
                    if test_parent_exit:
                        stop(parent)
                        process.wait(timeout=15)
                        runtime.require(process.returncode == 0, "parent-exit shutdown failed")
                        checks.append("parent death triggers graceful runtime exit")
                    else:
                        update = case / "config.pending"
                        update.write_text(configuration(reloaded_key), encoding="utf-8")
                        os.replace(update, cfg)
                        wait_until(lambda: request(port, "/v1/models", reloaded_key)[0] == 200,
                                   process, "config reload")
                        runtime.require(request(port, "/v1/models", configured_key)[0] in (401, 403),
                                        "old configuration key still accepted")
                        runtime.require(request(port, "/v1/models", ephemeral_key)[0] == 200,
                                        "ephemeral key lost during reload")
                        # v8 configuration writes migrate the layout. The supervising
                        # Desktop must keep its v0 control contract and runtime-only key.
                        before_v8_read = cfg.read_bytes()
                        v8_path = "/v8/management/config/access/api-keys"
                        code, body = request(port, v8_path, management_key)
                        runtime.require(code == 200 and json.loads(body) == [reloaded_key],
                                        "v8 view did not expose the legacy configuration key")
                        runtime.require(cfg.read_bytes() == before_v8_read,
                                        "v8 read unexpectedly migrated persisted configuration")
                        for key in ("", ephemeral_key):
                            runtime.require(request(port, v8_path, key)[0] in (401, 403),
                                            "v8 management authentication was bypassed")
                        code, body = request(port, v8_path, management_key, "PUT",
                                             json.dumps([v8_key]).encode())
                        runtime.require(code == 200 and json.loads(body).get("config-version") == 8,
                                        "v8 configuration mutation failed")
                        wait_until(lambda: request(port, "/v1/models", v8_key)[0] == 200,
                                   process, "v8 configuration reload")
                        runtime.require(request(port, "/v1/models", reloaded_key)[0] in (401, 403),
                                        "pre-v8 configuration key still accepted")
                        runtime.require(request(port, "/v1/models", ephemeral_key)[0] == 200,
                                        "ephemeral key lost during v8 layout migration")
                        code, body = request(port, "/v0/management/runtime-info", management_key)
                        runtime.require(code == 200 and json.loads(body).get("contract_version") == "1",
                                        "v0 host control lost after v8 configuration migration")
                        checks.append("v8 config migration preserves v0 host control and ephemeral authentication")
                        for path in case.rglob("*"):
                            if path.is_file() and path.name != "process.log":
                                runtime.require(ephemeral_key.encode() not in path.read_bytes(),
                                                "ephemeral key persisted to disk")
                        checks.extend(["authenticated host contract version 1 with pinned metadata",
                                       "public and management authentication separated",
                                       "ephemeral key survives proven config reload without persistence"])
                        runtime.require(request(port, "/v0/management/runtime-shutdown", management_key, "POST")[0] == 202,
                                        "authenticated loopback shutdown rejected")
                        process.wait(timeout=15)
                        runtime.require(process.returncode == 0, "management shutdown failed")
                        checks.append("authenticated loopback graceful shutdown")
            except Exception:
                log = case / "process.log"
                if log.exists():
                    text = log.read_text(errors="replace")[-16000:]
                    for secret in (management_key, ephemeral_key, configured_key, reloaded_key, v8_key):
                        text = text.replace(secret, "[REDACTED]")
                    print(text, file=sys.stderr)
                raise
            finally:
                stop(process)
                stop(parent)
    report = {"schema_version": 1, "target": manifest["target"], "source_commit": manifest["source"]["commit"],
              "binary_sha256": manifest["binary"]["sha256"], "status": "passed", "checks": checks,
              "live_provider_api_tested": False,
              "dynamic_library_plugins_tested": dynamic_library_plugins_tested}
    (artifact_dir / "smoke-report.json").write_bytes(runtime.encoded(report))
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifact-dir", type=Path, required=True)
    parser.add_argument("--allow-unavailable-multicast", action="store_true",
                        help="Allow only a structured multicast no-route error; report LAN scanning as unverified")
    args = parser.parse_args()
    smoke(args.artifact_dir.resolve(), args.allow_unavailable_multicast)
