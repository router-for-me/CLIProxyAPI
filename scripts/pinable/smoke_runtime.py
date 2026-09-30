#!/usr/bin/env python3
"""Run the packaged executable on its real target OS, without provider credentials."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
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


def request(port, path, key="", method="GET"):
    headers = {"Authorization": "Bearer " + key} if key else {}
    data = b"" if method == "POST" else None
    req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", headers=headers, data=data, method=method)
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with client.open(req, timeout=2) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


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


def smoke(artifact_dir: Path):
    manifests = list(artifact_dir.glob("*.manifest.json"))
    runtime.require(len(manifests) == 1, "expected one native platform artifact")
    manifest, _ = runtime.verify(manifests[0])
    target = {"Windows": "windows", "Darwin": "darwin", "Linux": "linux"}[platform.system()]
    arch = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64", "ARM64": "arm64"}[platform.machine()]
    runtime.require(manifest["target"] == target + "/" + arch, "smoke must run on the native target architecture")
    checks = []
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
            output = subprocess.check_output([str(binary), *args], cwd=isolated, env=env, timeout=15)
            json.loads(output)
        checks.append("new and legacy discovery JSON")
        if target == "darwin":
            subprocess.run(["codesign", "--verify", "--strict", str(binary)], check=True)
            checks.append("ad-hoc signature verified after extraction")

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
              "live_provider_api_tested": False}
    (artifact_dir / "smoke-report.json").write_bytes(runtime.encoded(report))
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifact-dir", type=Path, required=True)
    smoke(parser.parse_args().artifact_dir.resolve())
