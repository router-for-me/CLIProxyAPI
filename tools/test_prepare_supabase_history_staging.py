import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("prepare_supabase_history_staging.py")
SHA_A = "a" * 64
SHA_B = "b" * 64
SHA_C = "c" * 64
TARGET_ID = "f" * 64
DESTINATION_ID = "d" * 64


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, sort_keys=True) + "\n", encoding="utf-8")


def write_jsonl(path, records):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        "".join(json.dumps(record, sort_keys=True) + "\n" for record in records),
        encoding="utf-8",
    )


def audit_record(object_key, hour, compressed_bytes, *, provider="", status="uploaded"):
    key_name = "secret-key-" + object_key.rsplit("/", 1)[-1]
    return {
        "timestamp": "2026-08-01T00:00:00+08:00",
        "status": status,
        "provider": provider,
        "hour": hour,
        "source_count": 1,
        "source_bytes": 100,
        "key_names": {
            key_name: {
                "source_count": 1,
                "source_bytes": 100,
                "models": {
                    "gpt-5.6-sol": {"source_count": 1, "source_bytes": 100}
                },
            }
        },
        "jsonl_bytes": 150,
        "compressed_bytes": compressed_bytes,
        "object_key": object_key,
        "archive_path": "private/archive/path",
        "deleted_sources": 0,
    }


def object_state(object_key, compressed_bytes, archive_sha=SHA_A):
    return {
        "object_key": object_key,
        "compressed_size": compressed_bytes,
        "archive_sha256": archive_sha,
        "verification": "put-success-or-remote-head-match",
        "uploaded_at": "2026-08-01T00:00:00+08:00",
        "verified_at": "2026-08-01T00:00:00+08:00",
        "archive_path": "private/archive/path",
    }


def hour_state(object_key, archive_sha=SHA_A, manifest_sha=SHA_B):
    return {
        "status": "sealed",
        "object_key": object_key,
        "archive_sha256": archive_sha,
        "manifest_sha256": manifest_sha,
        "uploaded_at": "2026-08-01T00:00:00+08:00",
    }


def base_state(schema_version=3):
    state = {
        "schema_version": schema_version,
        "target": {
            "provider": "volcengine-tos",
            "endpoint": "https://tos.example.invalid",
            "region": "test-region",
            "bucket": "test-bucket",
            "object_prefix": "cliproxy-logs",
            "id": TARGET_ID,
        },
        "policy": {
            "timezone": "Asia/Shanghai",
            "grouping": "completion-modtime-hour-v1",
            "naming": "provider-jsonl-size-v2",
        },
        "uploaded": {},
        "objects": {},
        "hours": {},
        "prepared_hours": {},
    }
    if schema_version == 3:
        state.update(
            {
                "supabase_outbox": {
                    "schema_version": 1,
                    "destination_id": DESTINATION_ID,
                    "entries": {"must-be-cleared": {"secret": "value"}},
                },
                "supabase_history": {"must-be-cleared": {"secret": "value"}},
                "session_gate": {"sessions": {"must-be-omitted": {}}},
            }
        )
    return state


class PrepareSupabaseHistoryStagingTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.logs_root = REPO_ROOT / "logs"
        cls.created_logs_root = not cls.logs_root.exists()
        cls.logs_root.mkdir(exist_ok=True)

    @classmethod
    def tearDownClass(cls):
        if cls.created_logs_root:
            try:
                cls.logs_root.rmdir()
            except OSError:
                pass

    def setUp(self):
        self.input_temp = tempfile.TemporaryDirectory()
        self.output_temp = tempfile.TemporaryDirectory(dir=self.logs_root)
        self.input_root = Path(self.input_temp.name)
        self.output_parent = Path(self.output_temp.name)

    def tearDown(self):
        self.output_temp.cleanup()
        self.input_temp.cleanup()

    def make_config(self, text=None):
        config = self.input_root / "log-uploader.yaml"
        config.write_text(
            text
            or "# fixture\nwork-dir: old/work\ntimezone: Asia/Shanghai\nnested:\n  work-dir: untouched\n",
            encoding="utf-8",
        )
        return config

    def invoke(
        self,
        work_dir,
        config,
        snapshots,
        output,
        *,
        current,
        recovered,
        skipped,
    ):
        command = [
            sys.executable,
            str(SCRIPT),
            "--work-dir",
            str(work_dir),
            "--config",
            str(config),
        ]
        for snapshot in snapshots:
            command.extend(["--snapshot", str(snapshot)])
        command.extend(
            [
                "--output",
                str(output),
                "--expected-current",
                str(current),
                "--expected-recovered",
                str(recovered),
                "--expected-skipped",
                str(skipped),
            ]
        )
        return subprocess.run(
            command,
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            check=False,
        )

    def make_split_fixture(self):
        work_dir = self.input_root / "active-work"
        current = base_state()
        current_records = [
            audit_record("private/current-one", "2026-07-18T01:00:00+08:00", 10),
            audit_record("private/current-two", "2026-07-18T02:00:00+08:00", 20),
        ]
        for index, record in enumerate(current_records, 1):
            object_key = record["object_key"]
            current["objects"][object_key] = object_state(
                object_key, record["compressed_bytes"], SHA_A
            )
            current["hours"][f"2026-07-18-0{index}:codex"] = hour_state(
                object_key, SHA_A, SHA_B
            )
        write_json(work_dir / "state.json", current)

        recovered_records = [
            audit_record("private/recovered-one", "2026-07-19T03:00:00+08:00", 30),
            audit_record("private/recovered-two", "2026-07-19T03:00:00+08:00", 40),
        ]
        skipped_record = audit_record(
            "private/final-unresolved", "2026-07-20T04:00:00+08:00", 50
        )
        duplicate = copy.deepcopy(current_records[0])
        duplicate["status"] = "uploaded_cleanup_pending"
        duplicate["timestamp"] = "2026-08-02T00:00:00+08:00"
        duplicate["archive_path"] = "different/private/path"
        write_jsonl(
            work_dir / "history" / "2026-07.jsonl",
            [current_records[0], recovered_records[0], skipped_record, duplicate],
        )
        write_jsonl(work_dir / "audit.jsonl", [current_records[1], recovered_records[1]])

        snapshot = base_state(schema_version=2)
        snapshot["policy"]["naming"] = "codex56sol-jsonl-size-v1"
        for index, record in enumerate(recovered_records, 1):
            object_key = record["object_key"]
            snapshot["objects"][object_key] = object_state(
                object_key, record["compressed_bytes"], SHA_C
            )
            hour_key = "2026-07-19-03" if index == 1 else "2026-07-19-03-p2"
            snapshot["hours"][hour_key] = hour_state(
                object_key, SHA_C, SHA_B
            )
        snapshot_path = self.input_root / "old-state.json"
        write_json(snapshot_path, snapshot)
        return work_dir, self.make_config(), snapshot_path

    def input_bytes(self, *roots):
        result = {}
        for root in roots:
            if root.is_file():
                result[str(root)] = root.read_bytes()
                continue
            for path in sorted(item for item in root.rglob("*") if item.is_file()):
                result[str(path)] = path.read_bytes()
        return result

    def test_builds_current_and_isolated_same_hour_recovered_groups(self):
        work_dir, config, snapshot = self.make_split_fixture()
        before = self.input_bytes(work_dir, config, snapshot)
        output = self.output_parent / "staging"

        result = self.invoke(
            work_dir,
            config,
            [snapshot],
            output,
            current=2,
            recovered=2,
            skipped=1,
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        summary = json.loads(result.stdout)
        self.assertEqual(
            summary,
            {
                "current_records": 2,
                "groups": 3,
                "output": str(output.resolve()),
                "recovered_records": 2,
                "skipped_records": 1,
            },
        )
        for secret in ("secret-key", "private/current", "private/recovered", "final-unresolved"):
            self.assertNotIn(secret, result.stdout + result.stderr)

        groups = [output / "current", output / "recovered-0001", output / "recovered-0002"]
        self.assertEqual([path.name for path in output.iterdir() if path.is_dir()], [
            "current",
            "recovered-0001",
            "recovered-0002",
        ])
        current_state = json.loads((groups[0] / "state.json").read_text(encoding="utf-8"))
        self.assertEqual(len(current_state["objects"]), 2)
        self.assertEqual(len(current_state["hours"]), 2)
        current_audit = (groups[0] / "audit.jsonl").read_text(encoding="utf-8").splitlines()
        self.assertEqual(len(current_audit), 2)

        for group in groups:
            state = json.loads((group / "state.json").read_text(encoding="utf-8"))
            self.assertEqual(state["schema_version"], 3)
            self.assertEqual(state["target"], current_state["target"])
            self.assertEqual(state["policy"], current_state["policy"])
            self.assertEqual(state["uploaded"], {})
            self.assertEqual(state["prepared_hours"], {})
            self.assertEqual(
                state["supabase_outbox"],
                {
                    "schema_version": 1,
                    "destination_id": DESTINATION_ID,
                    "entries": {},
                },
            )
            self.assertEqual(state["supabase_history"], {})
            self.assertNotIn("session_gate", state)
            self.assertTrue(
                all(item.get("archive_path") == "" for item in state["objects"].values())
            )

        recovered_hour_keys = []
        for group in groups[1:]:
            state = json.loads((group / "state.json").read_text(encoding="utf-8"))
            self.assertEqual(len(state["objects"]), 1)
            self.assertEqual(len(state["hours"]), 1)
            self.assertEqual(
                len((group / "audit.jsonl").read_text(encoding="utf-8").splitlines()),
                1,
            )
            recovered_hour_keys.extend(state["hours"].keys())
        self.assertEqual(recovered_hour_keys, ["2026-07-19-03:codex"] * 2)

        relative_output = output.resolve().relative_to((REPO_ROOT / "logs").resolve())
        container_root = "/CLIProxyAPI/logs/" + relative_output.as_posix()
        expected_configs = [f"{container_root}/{group.name}/config.yaml" for group in groups]
        manifest = json.loads((output / "manifest.json").read_text(encoding="utf-8"))
        self.assertEqual(
            manifest,
            {
                "config_paths": expected_configs,
                "current_records": 2,
                "recovered_records": 2,
                "schema_version": 1,
                "skipped_records": 1,
            },
        )
        for group, container_config in zip(groups, expected_configs):
            config_text = (group / "config.yaml").read_text(encoding="utf-8")
            self.assertIn(f"work-dir: {container_config.rsplit('/', 1)[0]}\n", config_text)
            self.assertIn("  work-dir: untouched\n", config_text)

        if os.name != "nt":
            for directory in [output, *groups]:
                self.assertEqual(stat.S_IMODE(directory.stat().st_mode), 0o700)
            for path in output.rglob("*"):
                if path.is_file():
                    self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(self.input_bytes(work_dir, config, snapshot), before)

    def test_count_mismatch_rejects_before_output_exists(self):
        work_dir, config, snapshot = self.make_split_fixture()
        output = self.output_parent / "count-mismatch"

        result = self.invoke(
            work_dir,
            config,
            [snapshot],
            output,
            current=1,
            recovered=2,
            skipped=1,
        )

        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(output.exists())

    def test_mismatched_snapshot_evidence_is_skipped(self):
        mutations = {
            "target": lambda state, object_key: state["target"].update(id=SHA_A),
            "target_full": lambda state, object_key: state["target"].update(
                endpoint="https://different.example.invalid"
            ),
            "policy_timezone": lambda state, object_key: state["policy"].update(
                timezone="UTC"
            ),
            "policy_grouping": lambda state, object_key: state["policy"].update(
                grouping="different-grouping"
            ),
            "policy_naming": lambda state, object_key: state["policy"].update(
                naming="unsupported-naming"
            ),
            "size": lambda state, object_key: state["objects"][object_key].update(
                compressed_size=999
            ),
            "archive_sha": lambda state, object_key: state["hours"][
                "2026-07-21-05"
            ].update(archive_sha256=SHA_B),
            "manifest": lambda state, object_key: state["hours"][
                "2026-07-21-05"
            ].update(manifest_sha256="invalid"),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                fixture = self.input_root / name
                work_dir = fixture / "work"
                current = base_state()
                write_json(work_dir / "state.json", current)
                record = audit_record(
                    f"private/mismatch-{name}", "2026-07-21T05:00:00+08:00", 60
                )
                write_jsonl(work_dir / "audit.jsonl", [record])
                snapshot = base_state(schema_version=2)
                snapshot["objects"][record["object_key"]] = object_state(
                    record["object_key"], 60, SHA_A
                )
                snapshot["hours"]["2026-07-21-05"] = hour_state(
                    record["object_key"], SHA_A, SHA_B
                )
                mutate(snapshot, record["object_key"])
                snapshot_path = fixture / "snapshot.json"
                write_json(snapshot_path, snapshot)
                config = fixture / "config.yaml"
                config.write_text("work-dir: old\n", encoding="utf-8")
                output = self.output_parent / f"mismatch-{name}"

                result = self.invoke(
                    work_dir,
                    config,
                    [snapshot_path],
                    output,
                    current=0,
                    recovered=0,
                    skipped=1,
                )

                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertFalse((output / "recovered-0001").exists())
                self.assertEqual(
                    (output / "current" / "audit.jsonl").read_text(encoding="utf-8"),
                    "",
                )

    def test_conflicting_duplicate_audit_is_rejected_without_leaking_identity(self):
        work_dir = self.input_root / "conflict-work"
        write_json(work_dir / "state.json", base_state())
        first = audit_record("private/conflicting-object", "2026-07-22T06:00:00+08:00", 70)
        second = copy.deepcopy(first)
        second["compressed_bytes"] = 71
        write_jsonl(work_dir / "history" / "old.jsonl", [first])
        write_jsonl(work_dir / "audit.jsonl", [second])
        output = self.output_parent / "audit-conflict"

        result = self.invoke(
            work_dir,
            self.make_config(),
            [],
            output,
            current=0,
            recovered=0,
            skipped=1,
        )

        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(output.exists())
        self.assertNotIn("conflicting-object", result.stdout + result.stderr)
        self.assertNotIn("secret-key", result.stdout + result.stderr)

    def test_conflicting_valid_snapshot_evidence_is_rejected(self):
        work_dir = self.input_root / "snapshot-conflict-work"
        write_json(work_dir / "state.json", base_state())
        record = audit_record("private/snapshot-conflict", "2026-07-23T07:00:00+08:00", 80)
        write_jsonl(work_dir / "audit.jsonl", [record])
        snapshots = []
        for index, archive_sha in enumerate((SHA_A, SHA_B), 1):
            snapshot = base_state(schema_version=2)
            snapshot["objects"][record["object_key"]] = object_state(
                record["object_key"], 80, archive_sha
            )
            snapshot["hours"]["2026-07-23-07"] = hour_state(
                record["object_key"], archive_sha, SHA_C
            )
            path = self.input_root / f"conflicting-snapshot-{index}.json"
            write_json(path, snapshot)
            snapshots.append(path)
        output = self.output_parent / "snapshot-conflict"

        result = self.invoke(
            work_dir,
            self.make_config(),
            snapshots,
            output,
            current=0,
            recovered=1,
            skipped=0,
        )

        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(output.exists())
        self.assertNotIn("snapshot-conflict", result.stdout + result.stderr)

    def test_current_state_metadata_conflict_cannot_fall_back_to_snapshot(self):
        work_dir = self.input_root / "current-conflict-work"
        record = audit_record("private/current-conflict", "2026-07-24T08:00:00+08:00", 90)
        current = base_state()
        current["objects"][record["object_key"]] = object_state(
            record["object_key"], 91, SHA_A
        )
        current["hours"]["2026-07-24-08:codex"] = hour_state(
            record["object_key"], SHA_A, SHA_B
        )
        write_json(work_dir / "state.json", current)
        write_jsonl(work_dir / "audit.jsonl", [record])

        snapshot = base_state(schema_version=2)
        snapshot["objects"][record["object_key"]] = object_state(
            record["object_key"], 90, SHA_C
        )
        snapshot["hours"]["2026-07-24-08-p2"] = hour_state(
            record["object_key"], SHA_C, SHA_B
        )
        snapshot_path = self.input_root / "current-conflict-snapshot.json"
        write_json(snapshot_path, snapshot)
        output = self.output_parent / "current-conflict"

        result = self.invoke(
            work_dir,
            self.make_config(),
            [snapshot_path],
            output,
            current=0,
            recovered=1,
            skipped=0,
        )

        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(output.exists())
        self.assertNotIn("current-conflict", result.stdout + result.stderr)

    def test_current_target_and_policy_must_be_complete(self):
        mutations = {
            "target": lambda state: state["target"].pop("region"),
            "policy": lambda state: state["policy"].pop("naming"),
            "outbox": lambda state: state["supabase_outbox"].update(schema_version=2),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                fixture = self.input_root / f"invalid-{name}"
                work_dir = fixture / "work"
                state = base_state()
                mutate(state)
                write_json(work_dir / "state.json", state)
                write_jsonl(work_dir / "audit.jsonl", [])
                config = fixture / "config.yaml"
                config.write_text("work-dir: old\n", encoding="utf-8")
                output = self.output_parent / f"invalid-{name}"

                result = self.invoke(
                    work_dir,
                    config,
                    [],
                    output,
                    current=0,
                    recovered=0,
                    skipped=0,
                )

                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(output.exists())

    def test_runtime_provider_names_and_policy_timezone_drive_hour_keys(self):
        cases = [
            ("fable5", "2026-07-18T17:00:00+00:00", "2026-07-19-01:fable5"),
            ("grok45", "2026-07-19T02:00:00+08:00", "2026-07-19-02:grok45"),
        ]
        for index, (provider, audit_hour, hour_key) in enumerate(cases, 1):
            with self.subTest(provider=provider):
                fixture = self.input_root / provider
                work_dir = fixture / "work"
                record = audit_record(
                    f"private/provider-{index}", audit_hour, 100 + index, provider=provider
                )
                state = base_state()
                state["objects"][record["object_key"]] = object_state(
                    record["object_key"], record["compressed_bytes"], SHA_A
                )
                state["hours"][hour_key] = hour_state(record["object_key"], SHA_A, SHA_B)
                write_json(work_dir / "state.json", state)
                write_jsonl(work_dir / "audit.jsonl", [record])
                config = fixture / "config.yaml"
                config.write_text("work-dir: old\n", encoding="utf-8")
                output = self.output_parent / provider

                result = self.invoke(
                    work_dir,
                    config,
                    [],
                    output,
                    current=1,
                    recovered=0,
                    skipped=0,
                )

                self.assertEqual(result.returncode, 0, result.stderr)
                staged = json.loads(
                    (output / "current" / "state.json").read_text(encoding="utf-8")
                )
                self.assertEqual(list(staged["hours"]), [hour_key])
                staged_audit = json.loads(
                    (output / "current" / "audit.jsonl").read_text(encoding="utf-8")
                )
                if provider == "fable5":
                    self.assertEqual(staged_audit["hour"], "2026-07-19T01:00:00+08:00")

    def test_legacy_p1_snapshot_key_is_not_recovered(self):
        work_dir = self.input_root / "p1-work"
        write_json(work_dir / "state.json", base_state())
        record = audit_record("private/p1-object", "2026-07-25T09:00:00+08:00", 101)
        write_jsonl(work_dir / "audit.jsonl", [record])
        snapshot = base_state(schema_version=2)
        snapshot["objects"][record["object_key"]] = object_state(
            record["object_key"], 101, SHA_A
        )
        snapshot["hours"]["2026-07-25-09-p1"] = hour_state(
            record["object_key"], SHA_A, SHA_B
        )
        snapshot_path = self.input_root / "p1-snapshot.json"
        write_json(snapshot_path, snapshot)
        output = self.output_parent / "p1-output"

        result = self.invoke(
            work_dir,
            self.make_config(),
            [snapshot_path],
            output,
            current=0,
            recovered=0,
            skipped=1,
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((output / "recovered-0001").exists())

    def test_legacy_snapshot_key_without_provider_is_codex_only(self):
        cases = [
            ("fable5", "2026-07-27-11"),
            ("fable5", "2026-07-27-11-p2"),
            ("grok45", "2026-07-27-11"),
            ("grok45", "2026-07-27-11-p2"),
        ]
        for index, (provider, snapshot_hour_key) in enumerate(cases, 1):
            with self.subTest(provider=provider, hour_key=snapshot_hour_key):
                fixture = self.input_root / f"legacy-provider-{index}"
                work_dir = fixture / "work"
                write_json(work_dir / "state.json", base_state())
                record = audit_record(
                    f"private/legacy-provider-{index}",
                    "2026-07-27T11:00:00+08:00",
                    110 + index,
                    provider=provider,
                )
                write_jsonl(work_dir / "audit.jsonl", [record])
                snapshot = base_state(schema_version=2)
                snapshot["objects"][record["object_key"]] = object_state(
                    record["object_key"], record["compressed_bytes"], SHA_A
                )
                snapshot["hours"][snapshot_hour_key] = hour_state(
                    record["object_key"], SHA_A, SHA_B
                )
                snapshot_path = fixture / "snapshot.json"
                write_json(snapshot_path, snapshot)
                config = fixture / "config.yaml"
                config.write_text("work-dir: old\n", encoding="utf-8")
                output = self.output_parent / f"legacy-provider-{index}"

                result = self.invoke(
                    work_dir,
                    config,
                    [snapshot_path],
                    output,
                    current=0,
                    recovered=0,
                    skipped=1,
                )

                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertFalse((output / "recovered-0001").exists())

    def test_nonempty_provider_still_validates_model_rows(self):
        mutations = {
            "empty_name": lambda models: models.update(
                {"": {"source_count": 1, "source_bytes": 100}}
            ),
            "negative_total": lambda models: models.update(
                {"gpt-5.6-sol": {"source_count": -1, "source_bytes": 100}}
            ),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                fixture = self.input_root / f"model-{name}"
                work_dir = fixture / "work"
                record = audit_record(
                    f"private/model-{name}",
                    "2026-07-26T10:00:00+08:00",
                    102,
                    provider="codex",
                )
                models = record["key_names"][next(iter(record["key_names"]))]["models"]
                models.clear()
                mutate(models)
                write_json(work_dir / "state.json", base_state())
                write_jsonl(work_dir / "audit.jsonl", [record])
                config = fixture / "config.yaml"
                config.write_text("work-dir: old\n", encoding="utf-8")
                output = self.output_parent / f"model-{name}"

                result = self.invoke(
                    work_dir,
                    config,
                    [],
                    output,
                    current=0,
                    recovered=0,
                    skipped=1,
                )

                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(output.exists())

    def test_rejects_malformed_config_existing_output_and_output_inside_work_dir(self):
        work_dir = self.input_root / "safe-work"
        write_json(work_dir / "state.json", base_state())
        write_jsonl(work_dir / "audit.jsonl", [])

        malformed = self.make_config("work-dir: first\nwork-dir: second\n")
        malformed_output = self.output_parent / "malformed"
        malformed_result = self.invoke(
            work_dir,
            malformed,
            [],
            malformed_output,
            current=0,
            recovered=0,
            skipped=0,
        )
        self.assertNotEqual(malformed_result.returncode, 0)
        self.assertFalse(malformed_output.exists())

        existing_output = self.output_parent / "existing"
        existing_output.mkdir()
        existing_result = self.invoke(
            work_dir,
            self.make_config(),
            [],
            existing_output,
            current=0,
            recovered=0,
            skipped=0,
        )
        self.assertNotEqual(existing_result.returncode, 0)

        nested_work = self.output_parent / "nested-work"
        write_json(nested_work / "state.json", base_state())
        write_jsonl(nested_work / "audit.jsonl", [])
        inside_output = nested_work / "staging"
        inside_result = self.invoke(
            nested_work,
            self.make_config(),
            [],
            inside_output,
            current=0,
            recovered=0,
            skipped=0,
        )
        self.assertNotEqual(inside_result.returncode, 0)
        self.assertFalse(inside_output.exists())


if __name__ == "__main__":
    unittest.main()
