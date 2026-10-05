import asyncio
import importlib.metadata
import importlib.util
import json
import shutil
import subprocess
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock

import litellm
import pytest
import pytest_asyncio
from fastapi import HTTPException
from litellm.proxy import proxy_server
from litellm.proxy._types import LitellmUserRoles, UserAPIKeyAuth
from litellm.proxy.db.db_spend_update_writer import DBSpendUpdateWriter
from litellm.proxy.hooks.proxy_track_cost_callback import _ProxyDBLogger
from litellm.proxy.logging_endpoints import callback_logs_endpoints as upstream
from litellm.proxy.utils import PrismaClient, _create_spend_logs_with_poison_isolation
from litellm.types.proxy.callback_logs_endpoints import CallbackLogsRequest

ROOT = Path(__file__).parent


@pytest.fixture
def receiver(tmp_path):
    assert importlib.metadata.version("litellm") == "1.103.0"
    target = tmp_path / "litellm/proxy/logging_endpoints/callback_logs_endpoints.py"
    target.parent.mkdir(parents=True)
    shutil.copy(upstream.__file__, target)
    subprocess.run(["patch", "-p1", "-i", str(ROOT / "completed-failures.patch")], cwd=tmp_path, check=True, capture_output=True)
    spec = importlib.util.spec_from_file_location("contract_receiver", target)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture
def records():
    return CallbackLogsRequest.model_validate_json((ROOT / "records.json").read_text()).records


@pytest_asyncio.fixture
async def accounting(monkeypatch):
    writer = DBSpendUpdateWriter()
    prisma = SimpleNamespace(
        spend_log_transactions=[],
        _spend_log_transactions_lock=asyncio.Lock(),
        get_request_status=lambda payload: PrismaClient.get_request_status(None, payload),
    )
    monkeypatch.setattr(proxy_server, "prisma_client", prisma)
    monkeypatch.setattr(proxy_server, "disable_spend_logs", False)
    monkeypatch.setattr(proxy_server.proxy_logging_obj, "db_spend_update_writer", writer)
    monkeypatch.setattr(proxy_server, "increment_spend_counters", AsyncMock())
    monkeypatch.setattr(proxy_server, "update_cache", AsyncMock())
    monkeypatch.setattr(proxy_server.proxy_logging_obj.slack_alerting_instance, "customer_spend_alert", AsyncMock())
    logger = _ProxyDBLogger()
    monkeypatch.setattr(litellm, "_async_success_callback", [logger])
    monkeypatch.setattr(litellm, "_async_failure_callback", [logger])
    yield writer, prisma
    # Let LiteLLM's fire-and-forget database enqueue tasks finish without timer sleeps.
    await drain_tasks()


async def drain_tasks():
    current = asyncio.current_task()
    pending = [task for task in asyncio.all_tasks() if task is not current and "DBSpendUpdateWriter._batch_database_updates" in repr(task.get_coro())]
    if pending:
        await asyncio.gather(*pending)


@pytest.mark.asyncio
async def test_route_auth_and_batch(receiver, records):
    assert any(route.path == "/v1/rust_control_plane/logs" and "POST" in route.methods for route in receiver.rust_control_plane_router.routes)
    with pytest.raises(HTTPException) as error:
        await receiver.ingest_callback_logs(CallbackLogsRequest(records=records), UserAPIKeyAuth(user_role=LitellmUserRoles.INTERNAL_USER))
    assert error.value.status_code == 403
    capability = await receiver.callback_logs_capabilities(UserAPIKeyAuth(user_role=LitellmUserRoles.PROXY_ADMIN))
    assert capability == {"version": "1.103.0", "profile": "ai-proxy-completed-v1"}
    replay = receiver.CallbackLogsReplayer()
    replay.replay = AsyncMock(side_effect=[None, ValueError("contract failure"), None])
    result = await replay.replay_batch(records)
    assert (result.processed, result.failed) == (2, 1)
    assert result.failures[0].index == 1


@pytest.mark.asyncio
async def test_completed_accounting_and_duplicates(receiver, records, accounting):
    writer, prisma = accounting
    result = await receiver.CallbackLogsReplayer().replay_batch(records)
    assert result.failed == 0
    await drain_tasks()
    rows = prisma.spend_log_transactions
    assert len(rows) == 3, rows
    assert len({row["request_id"] for row in rows}) == 3
    assert [row["status"] for row in rows] == ["success", "failure", "failure"]
    for row in rows:
        assert (row["prompt_tokens"], row["completion_tokens"], row["total_tokens"]) == (10, 5, 15)
        assert row["spend"] == pytest.approx(0.000123456)
        assert row["api_key"] == "a" * 64
        assert row["user"] == "accounting-user"
        assert row["team_id"] == "accounting-team"
        assert row["model"] == "gpt-4o"
        assert row["custom_llm_provider"] == "openai"

    assert sum(row["total_tokens"] for row in rows) == 45
    assert sum(row["spend"] for row in rows) == pytest.approx(0.000370368)

    # The same completed ID is replayed on a fresh Logging object. Ordinary
    # completions have no aggregate charge claim in v1.103.0.
    await receiver.CallbackLogsReplayer().replay_batch(records[:1])
    await drain_tasks()
    assert len(prisma.spend_log_transactions) == 4
    assert len({row["request_id"] for row in prisma.spend_log_transactions}) == 3
    user = await writer.daily_spend_update_queue.flush_and_get_aggregated_daily_spend_update_transactions()
    team = await writer.daily_team_spend_update_queue.flush_and_get_aggregated_daily_spend_update_transactions()
    for aggregates in (user, team):
        assert len(aggregates) == 1, aggregates
        aggregate = next(iter(aggregates.values()))
        assert aggregate["api_requests"] == 4
        assert aggregate["successful_requests"] == 2
        assert aggregate["failed_requests"] == 2
        assert aggregate["prompt_tokens"] == 40
        assert aggregate["completion_tokens"] == 20
        assert aggregate["spend"] == pytest.approx(4 * 0.000123456)
    counters = await writer.spend_update_queue.flush_and_get_aggregated_db_spend_update_transactions()
    for entity, identity in (("key", "a" * 64), ("user", "accounting-user"), ("team", "accounting-team")):
        assert counters[entity + "_list_transactions"][identity] == pytest.approx(4 * 0.000123456), counters
    assert proxy_server.increment_spend_counters.await_count == 4

    # Exercise the pinned spend-log writer's duplicate insertion policy at its
    # repository boundary. This is not a live Postgres persistence assertion.
    table = SimpleNamespace(create_many=AsyncMock())
    await _create_spend_logs_with_poison_isolation(SimpleNamespace(table=table), rows, 256)
    assert table.create_many.call_args.kwargs["skip_duplicates"] is True


@pytest.mark.asyncio
async def test_stock_failure_does_not_persist(records, accounting):
    _, prisma = accounting
    result = await upstream.CallbackLogsReplayer().replay_batch(records[1:2])
    await drain_tasks()
    assert result.processed == 1
    assert prisma.spend_log_transactions == []


@pytest.mark.asyncio
async def test_callback_acceptance_is_not_persistence(receiver, records, accounting, monkeypatch):
    writer, prisma = accounting
    monkeypatch.setattr(writer, "update_database", AsyncMock(side_effect=RuntimeError("persistence unavailable")))
    result = await receiver.CallbackLogsReplayer().replay_batch(records[:1])
    assert (result.processed, result.failed) == (1, 0)
    assert prisma.spend_log_transactions == []
