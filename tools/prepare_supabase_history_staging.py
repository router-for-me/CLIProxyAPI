#!/usr/bin/env python3
"""Prepare isolated schema-v3 work directories for Supabase history sync.

Usage: pass the active uploader work directory, its YAML config, one or more
schema-v2 state snapshots, a new output directory below this repository's
``logs`` directory, and the three expected record counts. The tool reads only
local state and audit files; it never accesses object storage, archives, raw
logs, or the network, and it never modifies its inputs.
"""

from __future__ import annotations

import argparse
import copy
from dataclasses import dataclass
from datetime import datetime
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import sys
import tempfile
from typing import Any, Iterable
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError


SUCCESS_STATUSES = {
    "uploaded",
    "uploaded_cleanup_pending",
    "uploaded_delete_pending",
    "uploaded_archive_delete_pending",
}
PROVIDERS = {"", "codex", "fable5", "grok45"}
LEGACY_NAMING_POLICIES = {
    "codex56sol-jsonl-size-v1",
    "all-models-jsonl-size-v1",
}
MAX_SAFE_JSON_INTEGER = (1 << 53) - 1
SHA256_RE = re.compile(r"^[0-9a-fA-F]{64}$")
HOUR_KEY_RE = re.compile(
    r"^(?P<base>\d{4}-\d{2}-\d{2}-\d{2})(?:-p(?P<part>\d+))?"
    r"(?::(?P<provider>codex|fable5|grok45))?$"
)
TOP_LEVEL_WORK_DIR_RE = re.compile(r"^work-dir[ \t]*:")


class StagingError(Exception):
    """A sanitized, user-facing staging validation error."""


@dataclass(frozen=True)
class AuditEntry:
    data: dict[str, Any]
    object_key: str
    hour: datetime
    provider: str
    compressed_bytes: int

    @property
    def canonical_hour_key(self) -> str:
        provider = self.provider or "codex"
        return f"{self.hour:%Y-%m-%d-%H}:{provider}"


@dataclass(frozen=True)
class Evidence:
    object_state: dict[str, Any]
    hour_state: dict[str, Any]
    signature: tuple[int, str, str]


def nonnegative_integer(value: str) -> int:
    try:
        parsed = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("must be a non-negative integer") from exc
    if parsed < 0:
        raise argparse.ArgumentTypeError("must be a non-negative integer")
    return parsed


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work-dir", required=True, help="Active uploader work directory")
    parser.add_argument("--config", required=True, help="Uploader YAML config to copy")
    parser.add_argument(
        "--snapshot",
        action="append",
        default=[],
        help="Schema-v2 state snapshot; may be repeated",
    )
    parser.add_argument("--output", required=True, help="New staging directory below repo logs")
    parser.add_argument("--expected-current", required=True, type=nonnegative_integer)
    parser.add_argument("--expected-recovered", required=True, type=nonnegative_integer)
    parser.add_argument("--expected-skipped", required=True, type=nonnegative_integer)
    return parser


def is_relative_to(path: Path, parent: Path) -> bool:
    try:
        path.relative_to(parent)
    except ValueError:
        return False
    return True


def require_safe_directory(path: Path, label: str) -> None:
    try:
        info = path.lstat()
    except OSError as exc:
        raise StagingError(f"{label} is unavailable") from exc
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
        raise StagingError(f"{label} is not a safe directory")


def require_safe_regular_file(path: Path, label: str) -> None:
    try:
        info = path.lstat()
    except OSError as exc:
        raise StagingError(f"{label} is unavailable") from exc
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise StagingError(f"{label} is not a safe regular file")


def load_json_object(path: Path, label: str) -> dict[str, Any]:
    require_safe_regular_file(path, label)
    try:
        raw = path.read_bytes()
        value = json.loads(raw)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise StagingError(f"{label} is not valid JSON") from exc
    if not isinstance(value, dict):
        raise StagingError(f"{label} must contain a JSON object")
    return value


def load_config_template(path: Path) -> list[str]:
    require_safe_regular_file(path, "config")
    try:
        lines = path.read_text(encoding="utf-8").splitlines(keepends=True)
    except (OSError, UnicodeDecodeError) as exc:
        raise StagingError("config is not valid UTF-8") from exc
    matches = [index for index, line in enumerate(lines) if TOP_LEVEL_WORK_DIR_RE.match(line)]
    if len(matches) != 1:
        raise StagingError("config must contain exactly one top-level work-dir")
    return lines


def render_config(template: list[str], container_work_dir: str) -> str:
    rendered = list(template)
    index = next(
        index for index, line in enumerate(rendered) if TOP_LEVEL_WORK_DIR_RE.match(line)
    )
    newline = "\r\n" if rendered[index].endswith("\r\n") else "\n"
    if not rendered[index].endswith(("\n", "\r")):
        newline = ""
    rendered[index] = f"work-dir: {container_work_dir}{newline}"
    return "".join(rendered)


def valid_sha256(value: Any) -> bool:
    return isinstance(value, str) and SHA256_RE.fullmatch(value) is not None


def require_safe_integer(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise StagingError("successful audit ledger contains invalid totals")
    if value < 0 or value > MAX_SAFE_JSON_INTEGER:
        raise StagingError("successful audit ledger contains invalid totals")
    return value


def parse_hour(value: Any, location: ZoneInfo) -> datetime:
    if not isinstance(value, str):
        raise StagingError("successful audit ledger contains an invalid hour")
    normalized = value[:-1] + "+00:00" if value.endswith("Z") else value
    try:
        hour = datetime.fromisoformat(normalized)
    except ValueError as exc:
        raise StagingError("successful audit ledger contains an invalid hour") from exc
    if hour.tzinfo is None or hour.utcoffset() is None:
        raise StagingError("successful audit ledger contains an invalid hour")
    if any((hour.minute, hour.second, hour.microsecond)):
        raise StagingError("successful audit ledger contains an invalid hour")
    return hour.astimezone(location)


def validate_object_key(value: Any) -> str:
    if not isinstance(value, str) or not value.strip() or len(value) > 2048:
        raise StagingError("successful audit ledger contains an invalid object identity")
    if value.startswith("/") or any(character in value for character in "\\?#"):
        raise StagingError("successful audit ledger contains an invalid object identity")
    if re.match(r"^[A-Za-z][A-Za-z0-9+.-]*:", value):
        raise StagingError("successful audit ledger contains an invalid object identity")
    if any(segment in {".", ".."} for segment in value.split("/")):
        raise StagingError("successful audit ledger contains an invalid object identity")
    return value


def normalize_audit_record(raw: Any, location: ZoneInfo) -> AuditEntry | None:
    if not isinstance(raw, dict):
        raise StagingError("successful audit ledger contains an invalid record")
    status = raw.get("status", "")
    if not isinstance(status, str) or status.strip() not in SUCCESS_STATUSES:
        return None
    status = status.strip()
    provider = raw.get("provider", "")
    if not isinstance(provider, str) or provider.strip() not in PROVIDERS:
        raise StagingError("successful audit ledger contains an invalid provider")
    provider = provider.strip()
    hour = parse_hour(raw.get("hour"), location)
    object_key = validate_object_key(raw.get("object_key", ""))

    source_count = require_safe_integer(raw.get("source_count"))
    source_bytes = require_safe_integer(raw.get("source_bytes"))
    require_safe_integer(raw.get("jsonl_bytes"))
    compressed_bytes = require_safe_integer(raw.get("compressed_bytes"))
    key_names = raw.get("key_names")
    if not isinstance(key_names, dict) or not key_names:
        raise StagingError("successful audit ledger contains invalid usage rows")
    total_count = 0
    total_bytes = 0
    for key_name, summary in key_names.items():
        if not isinstance(key_name, str) or not key_name.strip() or len(key_name) > 48:
            raise StagingError("successful audit ledger contains invalid usage rows")
        if not isinstance(summary, dict):
            raise StagingError("successful audit ledger contains invalid usage rows")
        key_count = require_safe_integer(summary.get("source_count"))
        key_bytes = require_safe_integer(summary.get("source_bytes"))
        total_count += key_count
        total_bytes += key_bytes
        if total_count > MAX_SAFE_JSON_INTEGER or total_bytes > MAX_SAFE_JSON_INTEGER:
            raise StagingError("successful audit ledger contains invalid usage rows")
        models = summary.get("models", {})
        if models is None:
            models = {}
        if not isinstance(models, dict):
            raise StagingError("successful audit ledger contains invalid usage rows")
        model_count = 0
        model_bytes = 0
        for model_name, model in models.items():
            if not isinstance(model_name, str) or not model_name.strip() or not isinstance(model, dict):
                raise StagingError("successful audit ledger contains invalid usage rows")
            model_count += require_safe_integer(model.get("source_count"))
            model_bytes += require_safe_integer(model.get("source_bytes"))
        if provider == "":
            if model_count != key_count or model_bytes != key_bytes:
                raise StagingError("successful audit ledger contains invalid usage rows")
    if total_count != source_count or total_bytes != source_bytes:
        raise StagingError("successful audit ledger contains invalid usage totals")

    normalized = copy.deepcopy(raw)
    normalized["status"] = status
    normalized["provider"] = provider
    normalized["hour"] = hour.isoformat(timespec="seconds")
    normalized["object_key"] = object_key
    return AuditEntry(
        data=normalized,
        object_key=object_key,
        hour=hour,
        provider=provider,
        compressed_bytes=compressed_bytes,
    )


def audit_comparable(entry: AuditEntry) -> dict[str, Any]:
    data = entry.data
    return {
        "provider": entry.provider,
        "hour": data.get("hour"),
        "source_count": data.get("source_count"),
        "source_bytes": data.get("source_bytes"),
        "key_names": data.get("key_names"),
        "jsonl_bytes": data.get("jsonl_bytes"),
        "compressed_bytes": data.get("compressed_bytes"),
        "object_key": entry.object_key,
    }


def reconcile_audit_records(previous: AuditEntry, current: AuditEntry) -> AuditEntry:
    if audit_comparable(previous) != audit_comparable(current):
        raise StagingError("successful audit ledger contains conflicting duplicates")
    previous_event = previous.data.get("supabase_event_id", "")
    current_event = current.data.get("supabase_event_id", "")
    if not isinstance(previous_event, str) or not isinstance(current_event, str):
        raise StagingError("successful audit ledger contains an invalid event identity")
    if previous_event and current_event and previous_event != current_event:
        raise StagingError("successful audit ledger contains conflicting duplicates")
    if current_event and not previous_event:
        merged = copy.deepcopy(previous.data)
        merged["supabase_event_id"] = current_event
        return AuditEntry(
            data=merged,
            object_key=previous.object_key,
            hour=previous.hour,
            provider=previous.provider,
            compressed_bytes=previous.compressed_bytes,
        )
    return previous


def read_jsonl_records(path: Path, active: bool) -> Iterable[Any]:
    require_safe_regular_file(path, "audit ledger file")
    try:
        raw = path.read_bytes()
    except OSError as exc:
        raise StagingError("audit ledger file cannot be read") from exc
    if raw and not raw.endswith(b"\n"):
        if not active:
            raise StagingError("archived audit ledger has an incomplete final line")
        raw = raw.rsplit(b"\n", 1)[0] if b"\n" in raw else b""
    for line in raw.splitlines():
        if not line.strip():
            continue
        try:
            yield json.loads(line)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise StagingError("audit ledger contains invalid JSON") from exc


def read_ledger(work_dir: Path, location: ZoneInfo) -> list[AuditEntry]:
    files: list[tuple[Path, bool]] = []
    history_dir = work_dir / "history"
    if history_dir.exists() or history_dir.is_symlink():
        require_safe_directory(history_dir, "history ledger directory")
        try:
            entries = sorted(history_dir.iterdir(), key=lambda path: path.name)
        except OSError as exc:
            raise StagingError("history ledger directory cannot be read") from exc
        for path in entries:
            if path.suffix == ".jsonl":
                files.append((path, False))
    active = work_dir / "audit.jsonl"
    if active.exists() or active.is_symlink():
        files.append((active, True))

    by_object: dict[str, AuditEntry] = {}
    for path, is_active in files:
        for raw in read_jsonl_records(path, is_active):
            entry = normalize_audit_record(raw, location)
            if entry is None:
                continue
            previous = by_object.get(entry.object_key)
            by_object[entry.object_key] = (
                reconcile_audit_records(previous, entry) if previous else entry
            )
    return sorted(
        by_object.values(),
        key=lambda entry: (entry.hour, entry.provider, entry.object_key),
    )


def require_mapping(value: Any, label: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise StagingError(f"{label} is invalid")
    return value


def validate_current_state(
    state: dict[str, Any],
) -> tuple[dict[str, Any], dict[str, Any], dict[str, Any], ZoneInfo]:
    if state.get("schema_version") != 3:
        raise StagingError("current state is not schema-v3")
    target = require_mapping(state.get("target"), "current state target")
    policy = require_mapping(state.get("policy"), "current state policy")
    required_target_fields = ("provider", "endpoint", "region", "bucket", "object_prefix", "id")
    required_policy_fields = ("timezone", "grouping", "naming")
    if (
        not valid_sha256(target.get("id"))
        or any(not isinstance(target.get(field), str) for field in required_target_fields)
        or any(
            not isinstance(policy.get(field), str) or not policy[field].strip()
            for field in required_policy_fields
        )
        or any(not target[field].strip() for field in ("provider", "endpoint", "region", "bucket"))
    ):
        raise StagingError("current state target or policy is invalid")
    try:
        location = ZoneInfo(policy["timezone"])
    except (ZoneInfoNotFoundError, ValueError) as exc:
        raise StagingError("current state policy timezone is invalid") from exc
    require_mapping(state.get("objects"), "current state objects")
    require_mapping(state.get("hours"), "current state hours")
    outbox = require_mapping(state.get("supabase_outbox"), "current state Supabase outbox")
    schema_version = outbox.get("schema_version")
    destination_id = outbox.get("destination_id")
    if schema_version != 1:
        raise StagingError("current state Supabase outbox is invalid")
    if (
        not isinstance(destination_id, str)
        or (destination_id != "" and not re.fullmatch(r"[0-9a-f]{64}", destination_id))
        or not isinstance(outbox.get("entries"), dict)
    ):
        raise StagingError("current state Supabase outbox is invalid")
    return copy.deepcopy(target), copy.deepcopy(policy), {
        "schema_version": schema_version,
        "destination_id": destination_id,
        "entries": {},
    }, location


def normalized_object_state(obj: dict[str, Any], object_key: str) -> dict[str, Any]:
    result: dict[str, Any] = {
        "object_key": object_key,
        "compressed_size": obj["compressed_size"],
        "archive_sha256": obj["archive_sha256"].lower(),
        "archive_path": "",
    }
    for field in ("verification", "uploaded_at", "verified_at"):
        if field in obj:
            result[field] = copy.deepcopy(obj[field])
    return result


def normalized_hour_state(hour: dict[str, Any], entry: AuditEntry) -> dict[str, Any]:
    result: dict[str, Any] = {
        "status": "sealed",
        "object_key": entry.object_key,
        "archive_sha256": hour["archive_sha256"].lower(),
        "manifest_sha256": hour["manifest_sha256"].lower(),
    }
    if "uploaded_at" in hour:
        result["uploaded_at"] = copy.deepcopy(hour["uploaded_at"])
    event_id = hour.get("supabase_event_id", "")
    if event_id:
        result["supabase_event_id"] = event_id
    return result


def extract_evidence(
    state: dict[str, Any], entry: AuditEntry, *, current: bool
) -> Evidence | None:
    def mismatch() -> None:
        if current:
            raise StagingError("current state conflicts with successful audit")

    objects = state.get("objects")
    hours = state.get("hours")
    if not isinstance(objects, dict) or not isinstance(hours, dict):
        mismatch()
        return None
    obj = objects.get(entry.object_key)
    hour_matches = [
        (hour_key, hour)
        for hour_key, hour in hours.items()
        if isinstance(hour, dict) and hour.get("object_key") == entry.object_key
    ]
    if obj is None:
        if current and hour_matches:
            raise StagingError("current state conflicts with successful audit")
        return None
    if not isinstance(obj, dict) or len(hour_matches) != 1:
        mismatch()
        return None
    hour_key, hour = hour_matches[0]
    if not isinstance(hour_key, str):
        mismatch()
        return None
    if obj.get("object_key") != entry.object_key or hour.get("object_key") != entry.object_key:
        mismatch()
        return None
    compressed_size = obj.get("compressed_size")
    if (
        isinstance(compressed_size, bool)
        or not isinstance(compressed_size, int)
        or compressed_size != entry.compressed_bytes
    ):
        mismatch()
        return None
    object_sha = obj.get("archive_sha256")
    hour_sha = hour.get("archive_sha256")
    manifest_sha = hour.get("manifest_sha256")
    if (
        not valid_sha256(object_sha)
        or not valid_sha256(hour_sha)
        or object_sha.lower() != hour_sha.lower()
        or not valid_sha256(manifest_sha)
        or hour.get("status") != "sealed"
    ):
        mismatch()
        return None
    if current:
        if hour_key != entry.canonical_hour_key:
            raise StagingError("current state conflicts with successful audit")
        record_event = entry.data.get("supabase_event_id", "")
        hour_event = hour.get("supabase_event_id", "")
        if record_event and hour_event and record_event != hour_event:
            raise StagingError("current state conflicts with successful audit")
    else:
        match = HOUR_KEY_RE.fullmatch(hour_key)
        if match is None or match.group("base") != f"{entry.hour:%Y-%m-%d-%H}":
            return None
        if match.group("part") is not None and int(match.group("part")) < 2:
            return None
        state_provider = match.group("provider")
        if state_provider is None and entry.provider not in ("", "codex"):
            return None
        if state_provider is not None and state_provider != (entry.provider or "codex"):
            return None
    return Evidence(
        object_state=normalized_object_state(obj, entry.object_key),
        hour_state=normalized_hour_state(hour, entry),
        signature=(compressed_size, object_sha.lower(), manifest_sha.lower()),
    )


def validate_snapshot(state: dict[str, Any]) -> None:
    if state.get("schema_version") != 2:
        raise StagingError("snapshot is not schema-v2")
    require_mapping(state.get("target"), "snapshot target")
    require_mapping(state.get("policy"), "snapshot policy")
    require_mapping(state.get("objects"), "snapshot objects")
    require_mapping(state.get("hours"), "snapshot hours")


def snapshot_is_compatible(
    snapshot: dict[str, Any], target: dict[str, Any], policy: dict[str, Any]
) -> bool:
    snapshot_target = snapshot.get("target")
    snapshot_policy = snapshot.get("policy")
    if snapshot_target != target or not isinstance(snapshot_policy, dict):
        return False
    allowed_naming = {policy["naming"], *LEGACY_NAMING_POLICIES}
    return (
        snapshot_policy.get("timezone") == policy["timezone"]
        and snapshot_policy.get("grouping") == policy["grouping"]
        and snapshot_policy.get("naming") in allowed_naming
    )


def minimal_state(
    target: dict[str, Any],
    policy: dict[str, Any],
    outbox: dict[str, Any],
    objects: dict[str, Any],
    hours: dict[str, Any],
) -> dict[str, Any]:
    return {
        "schema_version": 3,
        "target": copy.deepcopy(target),
        "policy": copy.deepcopy(policy),
        "uploaded": {},
        "objects": objects,
        "hours": hours,
        "prepared_hours": {},
        "supabase_outbox": copy.deepcopy(outbox),
        "supabase_history": {},
    }


def chmod_supported(path: Path, mode: int) -> None:
    try:
        path.chmod(mode)
    except OSError:
        if os.name != "nt":
            raise


def write_sensitive_text(path: Path, content: str) -> None:
    path.write_text(content, encoding="utf-8", newline="")
    chmod_supported(path, 0o600)


def write_sensitive_json(path: Path, value: Any) -> None:
    write_sensitive_text(path, json.dumps(value, indent=2, sort_keys=True) + "\n")


def write_audit(path: Path, records: Iterable[AuditEntry]) -> None:
    content = "".join(
        json.dumps(entry.data, sort_keys=True, separators=(",", ":")) + "\n"
        for entry in records
    )
    write_sensitive_text(path, content)


def create_group(
    group_dir: Path,
    state: dict[str, Any],
    records: list[AuditEntry],
    config_template: list[str],
    container_work_dir: str,
) -> None:
    group_dir.mkdir(mode=0o700)
    chmod_supported(group_dir, 0o700)
    write_sensitive_json(group_dir / "state.json", state)
    write_audit(group_dir / "audit.jsonl", records)
    write_sensitive_text(
        group_dir / "config.yaml", render_config(config_template, container_work_dir)
    )


def prepare(args: argparse.Namespace, repo_root: Path) -> dict[str, Any]:
    work_dir_input = Path(args.work_dir)
    require_safe_directory(work_dir_input, "work directory")
    work_dir = work_dir_input.resolve()
    config_path = Path(args.config)
    config_template = load_config_template(config_path)

    output = Path(args.output).resolve(strict=False)
    if output.exists() or output.is_symlink():
        raise StagingError("output already exists")
    logs_root = (repo_root / "logs").resolve()
    require_safe_directory(logs_root, "repository logs directory")
    if output == logs_root or not is_relative_to(output, logs_root):
        raise StagingError("output must be below the repository logs directory")
    if output == work_dir or is_relative_to(output, work_dir):
        raise StagingError("output must not be the work directory or one of its children")
    require_safe_directory(output.parent, "output parent directory")

    current_state = load_json_object(work_dir / "state.json", "current state")
    target, policy, outbox, location = validate_current_state(current_state)
    ledger = read_ledger(work_dir, location)

    snapshots: list[dict[str, Any]] = []
    for raw_path in args.snapshot:
        snapshot = load_json_object(Path(raw_path), "snapshot")
        validate_snapshot(snapshot)
        snapshots.append(snapshot)

    current_records: list[AuditEntry] = []
    current_objects: dict[str, Any] = {}
    current_hours: dict[str, Any] = {}
    missing_records: list[AuditEntry] = []
    for entry in ledger:
        evidence = extract_evidence(current_state, entry, current=True)
        if evidence is None:
            missing_records.append(entry)
            continue
        current_records.append(entry)
        current_objects[entry.object_key] = evidence.object_state
        if entry.canonical_hour_key in current_hours:
            raise StagingError("current state contains an unsafe hour collision")
        current_hours[entry.canonical_hour_key] = evidence.hour_state

    recovered: list[tuple[AuditEntry, Evidence]] = []
    skipped: list[AuditEntry] = []
    for entry in missing_records:
        candidates: list[Evidence] = []
        for snapshot in snapshots:
            if not snapshot_is_compatible(snapshot, target, policy):
                continue
            evidence = extract_evidence(snapshot, entry, current=False)
            if evidence is not None:
                candidates.append(evidence)
        if not candidates:
            skipped.append(entry)
            continue
        signatures = {candidate.signature for candidate in candidates}
        if len(signatures) != 1:
            raise StagingError("snapshots contain conflicting trusted evidence")
        recovered.append((entry, candidates[0]))

    actual = (len(current_records), len(recovered), len(skipped))
    expected = (
        args.expected_current,
        args.expected_recovered,
        args.expected_skipped,
    )
    if actual != expected or len(ledger) != sum(expected):
        raise StagingError("record counts do not match the required expectations")

    relative_output = output.relative_to(logs_root)
    container_output = PurePosixPath("/CLIProxyAPI/logs") / PurePosixPath(
        relative_output.as_posix()
    )
    group_specs: list[tuple[str, dict[str, Any], list[AuditEntry]]] = [
        (
            "current",
            minimal_state(target, policy, outbox, current_objects, current_hours),
            current_records,
        )
    ]
    for index, (entry, evidence) in enumerate(recovered, 1):
        group_specs.append(
            (
                f"recovered-{index:04d}",
                minimal_state(
                    target,
                    policy,
                    outbox,
                    {entry.object_key: evidence.object_state},
                    {entry.canonical_hour_key: evidence.hour_state},
                ),
                [entry],
            )
        )

    temporary_path = Path(tempfile.mkdtemp(prefix=".history-staging-", dir=output.parent))
    chmod_supported(temporary_path, 0o700)
    published = False
    try:
        config_paths: list[str] = []
        for name, state, records in group_specs:
            container_group = container_output / name
            create_group(
                temporary_path / name,
                state,
                records,
                config_template,
                str(container_group),
            )
            config_paths.append(str(container_group / "config.yaml"))
        manifest = {
            "schema_version": 1,
            "current_records": len(current_records),
            "recovered_records": len(recovered),
            "skipped_records": len(skipped),
            "config_paths": config_paths,
        }
        write_sensitive_json(temporary_path / "manifest.json", manifest)
        if output.exists() or output.is_symlink():
            raise StagingError("output already exists")
        temporary_path.rename(output)
        published = True
    finally:
        if not published and temporary_path.exists():
            shutil.rmtree(temporary_path)

    return {
        "current_records": len(current_records),
        "recovered_records": len(recovered),
        "skipped_records": len(skipped),
        "groups": len(group_specs),
        "output": str(output),
    }


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    repo_root = Path(__file__).resolve().parents[1]
    try:
        summary = prepare(args, repo_root)
    except StagingError as exc:
        print(json.dumps({"error": str(exc)}, sort_keys=True), file=sys.stderr)
        return 1
    except OSError:
        print(json.dumps({"error": "staging filesystem operation failed"}), file=sys.stderr)
        return 1
    print(json.dumps(summary, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
