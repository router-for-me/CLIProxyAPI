"""Tests for the CI workflow YAML file."""

import yaml
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
WORKFLOW = ROOT / ".github/workflows/usage-dashboard-ci.yml"

# PyYAML interprets YAML's `on:` key as boolean True
def _trigger(doc):
    return doc.get("on") or doc.get(True)


def test_workflow_exists():
    assert WORKFLOW.is_file()


def test_workflow_is_valid_yaml():
    raw = WORKFLOW.read_text()
    doc = yaml.safe_load(raw)
    assert isinstance(doc, dict)


def test_workflow_triggers_on_pr():
    raw = WORKFLOW.read_text()
    doc = yaml.safe_load(raw)
    trig = _trigger(doc)
    assert "pull_request" in trig
    paths = trig["pull_request"]["paths"]
    assert "tools/usage-dashboard/**" in paths


def test_workflow_triggers_on_push_to_main():
    raw = WORKFLOW.read_text()
    doc = yaml.safe_load(raw)
    trig = _trigger(doc)
    assert "push" in trig
    assert trig["push"]["branches"] == ["main"]


def test_workflow_has_concurrency():
    raw = WORKFLOW.read_text()
    doc = yaml.safe_load(raw)
    assert "concurrency" in doc
    assert doc["concurrency"]["cancel-in-progress"] is True


def test_workflow_has_python_job():
    raw = WORKFLOW.read_text()
    doc = yaml.safe_load(raw)
    jobs = doc["jobs"]
    assert "python" in jobs
    steps = jobs["python"]["steps"]
    step_runs = [str(s) for s in steps]
    joined = " ".join(step_runs)
    assert "ruff" in joined
    assert "pytest" in joined
    assert "uv sync" in joined


def test_workflow_has_frontend_job():
    raw = WORKFLOW.read_text()
    doc = yaml.safe_load(raw)
    jobs = doc["jobs"]
    assert "frontend" in jobs
    steps = jobs["frontend"]["steps"]
    step_runs = [str(s) for s in steps]
    joined = " ".join(step_runs)
    assert "pnpm install" in joined
    assert "lint" in joined
    assert "typecheck" in joined
    assert "test" in joined
    assert "build" in joined