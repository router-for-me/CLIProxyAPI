import asyncio
import json
import logging
import os
import shlex
import shutil
import time
from contextlib import suppress
from pathlib import Path

import requests
from dotenv import load_dotenv
from telegram import Update
from telegram.ext import Application, CommandHandler, ContextTypes, MessageHandler, filters

from config import (
    ENV_PATH,
    SYSTEM_PROMPT,
    CHAT_ROUTE_DEFAULT,
    CHAT_ROUTE_CODING,
    PROMPT_PLACEHOLDER,
    FALLBACK_STATUS_MESSAGE,
    MAX_TELEGRAM_MESSAGE_LENGTH,
    DEFAULT_CHAT_TIMEOUT_SECONDS,
    DEFAULT_HEALTH_TIMEOUT_SECONDS,
    DEFAULT_TOOL_TIMEOUT_SECONDS,
    CODING_KEYWORDS,
    RETRYABLE_STATUS_CODES,
    NON_RETRYABLE_STATUS_CODES,
    MAX_TOOL_CAPTURE_BYTES,
    TOOL_PROBE_CACHE_TTL_SECONDS,
    parse_int_env,
    parse_bool_env,
    load_config,
    proxy_headers,
    proxy_root_url,
    build_proxy_endpoint,
    extract_prompt_from_command,
    truncate_text,
)
from auth import check_auth, update_context
from chat_client import (
    classify_text_route,
    get_chat_profile,
    build_chat_payload,
    should_fallback,
    response_error_details,
    normalize_content,
    extract_chat_content,
    safe_edit_text,
    post_chat_completion,
    get_proxy_endpoint,
    attempt_chat_request,
    attempt_agent_request,
)
from tools_handler import (
    build_tool_probe,
    format_tool_probe_status,
    tool_routes_enabled,
    read_stream_limited,
    write_process_stdin,
    collect_process_output,
)
from health import probe_keep_alive, fetch_models

load_dotenv(ENV_PATH)

TOOL_PROBE_CACHE = {}

logging.basicConfig(level=logging.INFO, format="%(message)s")
LOGGER = logging.getLogger("shadow_claw_gateway")

# Late-initialized subsystems (set up in main())
_job_store = None
_audit_log = None
_config = None
_rate_limiter = None
_conversation_manager = None



def log_event(event: str, **fields) -> None:
    payload = {"event": event}
    for key, value in fields.items():
        if value is not None:
            payload[key] = value
    LOGGER.info(json.dumps(payload, ensure_ascii=False, sort_keys=True))



def get_tool_probe(tool_name: str, tool_config: dict, now: float | None = None) -> dict:
    checked_at = now if now is not None else time.monotonic()
    cached = TOOL_PROBE_CACHE.get(tool_name)
    if cached and checked_at - cached["checked_at"] < TOOL_PROBE_CACHE_TTL_SECONDS:
        return cached

    inspection = inspect_tool_command(tool_name, tool_config.get("command", ""))
    probe = build_tool_probe(
        tool_name,
        inspection,
        verified=inspection.get("available", False),
        contract_capable=False,
        checked_at=checked_at,
    )
    TOOL_PROBE_CACHE[tool_name] = probe
    return probe



async def send_with_fallback(prompt: str, route_name: str, status_message, config: dict | None = None) -> str:
    active_config = config or load_config()
    profile = get_chat_profile(active_config, route_name)
    primary = await attempt_chat_request(prompt, profile, active_config, "Primary route")
    if primary["ok"]:
        return primary["content"]

    if not primary["retryable"]:
        raise RuntimeError(primary["error"])

    if status_message is not None:
        await safe_edit_text(status_message, FALLBACK_STATUS_MESSAGE)

    fallback_profile = {
        "route": "fallback",
        "model": active_config["fallback_model"],
        "reasoning_effort": profile.get("reasoning_effort", ""),
    }
    fallback = await attempt_chat_request(prompt, fallback_profile, active_config, "Fallback route")
    if fallback["ok"]:
        return f"Fallback model {fallback_profile['model']} answered successfully.\n\n{fallback['content']}"

    raise RuntimeError(f"{primary['error']}\n{fallback['error']}")


def inspect_tool_command(tool_name: str, command_text: str) -> dict:
    configured_command = (command_text or "").strip()
    if not configured_command:
        return {
            "tool": tool_name,
            "configured": False,
            "available": False,
            "message": "not configured",
        }

    try:
        argv = shlex.split(configured_command)
    except ValueError as error:
        return {
            "tool": tool_name,
            "configured": True,
            "available": False,
            "message": f"invalid command: {error}",
        }

    if not argv:
        return {
            "tool": tool_name,
            "configured": True,
            "available": False,
            "message": "empty command",
        }

    executable = argv[0]
    resolved = None
    if os.path.sep in executable or executable.startswith("."):
        expanded = str(Path(executable).expanduser())
        if Path(expanded).exists() and os.access(expanded, os.X_OK):
            resolved = expanded
    else:
        resolved = shutil.which(executable)

    if not resolved:
        return {
            "tool": tool_name,
            "configured": True,
            "available": False,
            "message": f"executable not found: {executable}",
            "argv": argv,
        }

    return {
        "tool": tool_name,
        "configured": True,
        "available": True,
        "message": resolved,
        "argv": [resolved, *argv[1:]],
        "resolved": resolved,
        "uses_prompt_placeholder": PROMPT_PLACEHOLDER in argv,
    }


async def run_tool_command(tool_name: str, prompt: str, tool_config: dict, output_limit: int) -> dict:
    inspection = inspect_tool_command(tool_name, tool_config.get("command", ""))
    if not inspection["configured"]:
        env_name = f"{tool_name.upper().replace('-', '_')}_COMMAND"
        return {
            "ok": False,
            "output": f"{tool_name} is not configured. Set {env_name} in shadow-claw/gateway/.env.",
        }
    if not inspection["available"]:
        return {"ok": False, "output": f"{tool_name} is unavailable: {inspection['message']}"}

    log_event(
        "tool.exec.start",
        tool_name=tool_name,
        executable=inspection.get("resolved"),
        uses_prompt_placeholder=inspection.get("uses_prompt_placeholder"),
    )
    argv = [prompt if arg == PROMPT_PLACEHOLDER else arg for arg in inspection["argv"]]
    stdin_data = None
    stdin_pipe = None
    if not inspection["uses_prompt_placeholder"]:
        stdin_data = prompt.encode("utf-8")
        stdin_pipe = asyncio.subprocess.PIPE

    safe_env = {
        k: v for k, v in os.environ.items()
        if k in {"PATH", "HOME", "LANG", "LC_ALL", "TERM", "USER", "SHELL", "TMPDIR", "TMP", "TEMP"}
    }

    try:
        process = await asyncio.create_subprocess_exec(
            *argv,
            stdin=stdin_pipe,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env=safe_env,
        )
    except OSError as error:
        return {"ok": False, "output": f"{tool_name} failed to start: {error}"}

    timeout = tool_config.get("timeout") or DEFAULT_TOOL_TIMEOUT_SECONDS
    telegram_limit = max(output_limit, 1)
    capture_limit = min(MAX_TOOL_CAPTURE_BYTES, telegram_limit)
    collected = await collect_process_output(process, stdin_data, capture_limit, timeout)
    if collected["timed_out"]:
        log_event("tool.exec.timeout", tool_name=tool_name, timeout_seconds=timeout)
        return {"ok": False, "output": f"{tool_name} timed out after {timeout}s."}

    stdout_result = collected["stdout"]
    stderr_result = collected["stderr"]
    stdout_text = stdout_result["data"].decode("utf-8", errors="replace").strip()
    stderr_text = stderr_result["data"].decode("utf-8", errors="replace").strip()
    output_truncated = stdout_result["truncated"] or stderr_result["truncated"]

    if output_truncated:
        log_event(
            "tool.exec.truncated",
            tool_name=tool_name,
            capture_limit_bytes=capture_limit,
            stdout_bytes_seen=stdout_result["bytes_seen"],
            stderr_bytes_seen=stderr_result["bytes_seen"],
        )

    if collected["returncode"] == 0:
        output = stdout_text or stderr_text or f"{tool_name} completed with no output."
        if output_truncated:
            output = f"{output}\n\n[output truncated after {capture_limit} bytes]"
        log_event("tool.exec.completed", tool_name=tool_name, returncode=collected["returncode"], ok=True)
        return {"ok": True, "output": truncate_text(output, output_limit)}

    failure_output = stderr_text or stdout_text or f"{tool_name} exited with status {collected['returncode']}."
    if output_truncated:
        failure_output = f"{failure_output}\n\n[output truncated after {capture_limit} bytes]"
    log_event("tool.exec.completed", tool_name=tool_name, returncode=collected["returncode"], ok=False)
    return {
        "ok": False,
        "output": truncate_text(
            f"{tool_name} exited with status {collected['returncode']}.\n\n{failure_output}",
            output_limit,
        ),
    }


async def run_autoresearch(prompt: str, config: dict | None = None) -> dict:
    active_config = config or load_config()
    return await run_tool_command(
        "autoresearch",
        prompt,
        active_config["tools"]["autoresearch"],
        active_config["tool_output_limit"],
    )


async def run_ruflo(prompt: str, config: dict | None = None) -> dict:
    active_config = config or load_config()
    return await run_tool_command(
        "ruflo",
        prompt,
        active_config["tools"]["ruflo"],
        active_config["tool_output_limit"],
    )


async def run_browser_use(prompt: str, config: dict | None = None) -> dict:
    active_config = config or load_config()
    return await run_tool_command(
        "browser-use",
        prompt,
        active_config["tools"]["browser-use"],
        active_config["tool_output_limit"],
    )


async def build_health_report(config: dict | None = None) -> str:
    active_config = config or load_config()
    keep_alive, models = await asyncio.gather(
        probe_keep_alive(active_config),
        fetch_models(active_config),
    )
    tool_checks = {
        tool_name: get_tool_probe(tool_name, tool_config)
        for tool_name, tool_config in active_config["tools"].items()
    }

    required_models = {
        active_config["default_profile"]["model"],
        active_config["coding_profile"]["model"],
        active_config["fallback_model"],
    }
    available_models = set(models["model_ids"])
    missing_models = sorted(model for model in required_models if model and model not in available_models)

    lines = [
        "Gateway status: running",
        f"Auth config: {'loaded' if active_config['telegram_token'] and active_config['allowed_user_id'] else 'missing'}",
        f"Default chat: {active_config['default_profile']['model']} (reasoning={active_config['default_profile']['reasoning_effort']})",
        f"Coding chat: {active_config['coding_profile']['model']} (reasoning={active_config['coding_profile']['reasoning_effort']})",
        f"Fallback chat: {active_config['fallback_model']}",
        f"Tool routes: {'enabled' if tool_routes_enabled(active_config) else 'disabled by config'}",
        f"CLIProxy models (primary): {'ok' if models['ok'] else 'error'} ({models['message']})",
        f"CLIProxy keep-alive (optional): {'ok' if keep_alive['ok'] else 'unavailable'} ({keep_alive['message']})",
    ]

    if models["ok"]:
        lines.append(
            "Required models visible: "
            + (", ".join(sorted(required_models)) if not missing_models else f"missing {', '.join(missing_models)}")
        )

    lines.append("Tool commands:")
    for tool_name in ("autoresearch", "ruflo", "browser-use"):
        lines.append(
            f"- {tool_name}: {format_tool_probe_status(tool_checks[tool_name], tools_enabled=tool_routes_enabled(active_config))}"
        )

    return truncate_text("\n".join(lines), MAX_TELEGRAM_MESSAGE_LENGTH)


async def authorize(update: Update, config: dict, reply_on_denied: bool = False) -> bool:
    user = update.effective_user
    if not user or check_auth(user.id, config):
        return True

    log_event("gateway.auth.denied", **update_context(update))
    if reply_on_denied and update.message:
        await update.message.reply_text("⛔ Acesso negado. Você não tem permissão para falar com este agente.")
    return False


async def reload_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    global _config
    if not await authorize(update, _config, reply_on_denied=True):
        return
    load_dotenv(ENV_PATH, override=True)
    _config = load_config()
    log_event("gateway.config.reloaded", **update_context(update))
    await update.message.reply_text("Configuração recarregada com sucesso.")


async def help_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return
    await update.message.reply_text(
        "Comandos disponíveis:\n"
        "/start - Iniciar o bot\n"
        "/health - Status do gateway\n"
        "/code <prompt> - Modo código\n"
        "/autoresearch <prompt> - Pesquisa automática\n"
        "/ruflo <prompt> - Agente RuFlo\n"
        "/browseruse <prompt> - Navegação web\n"
        "/jobs [tool] [status] - Listar jobs recentes\n"
        "/audit [export] - Resumo de auditoria\n"
        "/metrics - Métricas do gateway\n"
        "/reload - Recarregar configuração\n"
        "/help - Esta mensagem"
    )


async def start(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config, reply_on_denied=True):
        return

    await update.message.reply_text(
        "🤖 Olá, Mestre. Shadow-Claw Gateway online.\n\n"
        "Sistema seguro. O que faremos hoje?"
    )


async def handle_chat_prompt(update: Update, prompt: str, route_name: str, config: dict) -> None:
    ctx = update_context(update)
    started_at = time.monotonic()

    if _rate_limiter is not None:
        user = update.effective_user
        if user and not _rate_limiter.check(user.id):
            await update.message.reply_text("Rate limit atingido. Aguarde alguns segundos.")
            return

    profile = get_chat_profile(config, route_name)
    log_event("chat.request.start", **ctx, route=route_name, model=profile.get("model"))
    status_msg = await update.message.reply_text("🧠 Pensando...")

    # Agent mode: use tool-calling agent loop
    if config.get("agent_mode_enabled") and _conversation_manager is not None:
        try:
            reply = await _run_agent_loop(prompt, profile, config, ctx, status_msg)
            log_event(
                "chat.request.success",
                **ctx,
                route=route_name,
                model=profile.get("model"),
                duration_ms=int((time.monotonic() - started_at) * 1000),
                agent_mode=True,
            )
            await safe_edit_text(status_msg, truncate_text(reply, MAX_TELEGRAM_MESSAGE_LENGTH))
        except Exception as error:
            log_event(
                "chat.request.error",
                **ctx,
                route=route_name,
                model=profile.get("model"),
                duration_ms=int((time.monotonic() - started_at) * 1000),
                error=str(error),
                error_type=type(error).__name__,
                agent_mode=True,
            )
            # Fallback to plain chat on agent failure
            LOGGER.warning("Agent loop failed, falling back to plain chat: %s", error)
            try:
                reply = await send_with_fallback(prompt, route_name, status_msg, config)
                await safe_edit_text(status_msg, truncate_text(reply, MAX_TELEGRAM_MESSAGE_LENGTH))
            except Exception as fallback_error:
                await safe_edit_text(
                    status_msg,
                    truncate_text(f"Falha ao contactar o cérebro local: {fallback_error}", MAX_TELEGRAM_MESSAGE_LENGTH),
                )
        return

    # Plain chat mode (agent disabled or no conversation manager)
    try:
        reply = await send_with_fallback(prompt, route_name, status_msg, config)
        log_event(
            "chat.request.success",
            **ctx,
            route=route_name,
            model=profile.get("model"),
            duration_ms=int((time.monotonic() - started_at) * 1000),
            fallback_used=reply.startswith("Fallback model "),
        )
        await safe_edit_text(status_msg, truncate_text(reply, MAX_TELEGRAM_MESSAGE_LENGTH))
    except Exception as error:
        log_event(
            "chat.request.error",
            **ctx,
            route=route_name,
            model=profile.get("model"),
            duration_ms=int((time.monotonic() - started_at) * 1000),
            error=str(error),
            error_type=type(error).__name__,
        )
        await safe_edit_text(status_msg, truncate_text(f"Falha ao contactar o cérebro local: {error}", MAX_TELEGRAM_MESSAGE_LENGTH))


async def _run_agent_loop(prompt: str, profile: dict, config: dict, ctx: dict, status_msg) -> str:
    """Execute the agent loop for a user message."""
    from agent import AgentLoop

    session_id = str(ctx.get("chat_id", "default"))

    # Store user message
    await _conversation_manager.store_message(session_id, "user", prompt)

    # Build messages with memory context
    memory_ctx = await _conversation_manager.build_memory_context(session_id, prompt)
    history = await _conversation_manager.get_history(session_id, limit=10)

    messages = [{"role": "system", "content": SYSTEM_PROMPT}]
    if memory_ctx:
        messages.append({"role": "system", "content": memory_ctx})
    # Add history (skip the last one which is the current user message)
    if len(history) > 1:
        messages.extend(history[:-1])
    messages.append({"role": "user", "content": prompt})

    # Create the LLM send function for the agent loop
    async def send_fn(msgs, tools):
        result = await attempt_agent_request(msgs, profile, config, tools=tools)
        if not result["ok"]:
            # Try fallback model
            fallback_profile = {
                "route": "fallback",
                "model": config["fallback_model"],
                "reasoning_effort": profile.get("reasoning_effort", ""),
            }
            await safe_edit_text(status_msg, FALLBACK_STATUS_MESSAGE)
            result = await attempt_agent_request(msgs, fallback_profile, config, tools=tools)
            if not result["ok"]:
                raise RuntimeError(result["error"])
        return result["message"]

    loop = AgentLoop(
        send_fn=send_fn,
        max_iterations=config.get("max_tool_iterations", 5),
        total_timeout=config.get("agent_loop_timeout_seconds", 120),
        log_event=log_event,
    )
    reply = await loop.run(messages)

    # Store assistant reply
    await _conversation_manager.store_message(session_id, "assistant", reply)

    return reply


async def handle_tool_prompt(update: Update, prompt: str, tool_name: str, runner, config: dict) -> None:
    if not prompt:
        await update.message.reply_text(f"Usage: /{tool_name.replace('-', '')} <prompt>")
        return

    if not tool_routes_enabled(config):
        log_event("tool.route.disabled", **update_context(update), tool_name=tool_name)
        await update.message.reply_text(
            "Tool routes are disabled by config. Chat and /health remain available."
        )
        return

    if _rate_limiter is not None:
        user = update.effective_user
        if user and not _rate_limiter.check(user.id):
            await update.message.reply_text("Rate limit atingido. Aguarde alguns segundos.")
            return

    context = update_context(update)
    started_at = time.monotonic()
    log_event("tool.job.started", **context, tool_name=tool_name)

    # Track job in persistent store
    job_id = None
    if _job_store is not None:
        try:
            job_id = await _job_store.create_job(
                tool_name,
                prompt,
                context.get("telegram_user_id"),
                context.get("chat_id"),
            )
            await _job_store.update_status(job_id, "running")
        except Exception:
            LOGGER.warning("jobstore create/update failed", exc_info=True)
            job_id = None

    status_msg = await update.message.reply_text(f"Executando {tool_name}...")
    result = await runner(prompt, config)
    duration_ms = int((time.monotonic() - started_at) * 1000)
    ok = result.get("ok", False)
    log_event(
        "tool.job.completed",
        **context,
        tool_name=tool_name,
        ok=ok,
        duration_ms=duration_ms,
    )

    # Update persistent job
    if _job_store is not None and job_id is not None:
        try:
            status = "completed" if ok else "failed"
            summary = truncate_text(result.get("output", ""), 200)
            await _job_store.update_status(job_id, status, result_summary=summary)
        except Exception:
            LOGGER.warning("jobstore status update failed for %s", job_id, exc_info=True)

    await safe_edit_text(status_msg, result["output"])


async def health(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return

    status_msg = await update.message.reply_text("Verificando saúde do sistema...")
    report = await build_health_report(config)
    await safe_edit_text(status_msg, report)


async def code_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return

    prompt = " ".join(context.args).strip() or extract_prompt_from_command(update.message.text, "code")
    if not prompt:
        await update.message.reply_text("Usage: /code <prompt>")
        return

    await handle_chat_prompt(update, prompt, CHAT_ROUTE_CODING, config)


async def autoresearch_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return

    prompt = " ".join(context.args).strip() or extract_prompt_from_command(update.message.text, "autoresearch")
    await handle_tool_prompt(update, prompt, "autoresearch", run_autoresearch, config)


async def ruflo_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return

    prompt = " ".join(context.args).strip() or extract_prompt_from_command(update.message.text, "ruflo")
    await handle_tool_prompt(update, prompt, "ruflo", run_ruflo, config)


async def browseruse_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return

    prompt = " ".join(context.args).strip() or extract_prompt_from_command(update.message.text, "browseruse")
    await handle_tool_prompt(update, prompt, "browser-use", run_browser_use, config)


async def browseruse_alias(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return

    prompt = extract_prompt_from_command(update.message.text, "browser-use")
    await handle_tool_prompt(update, prompt, "browser-use", run_browser_use, config)


async def metrics_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return

    from metrics import get_metrics_summary

    summary = get_metrics_summary()
    await update.message.reply_text(summary)


async def jobs_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return

    if _job_store is None:
        await update.message.reply_text("Armazenamento de jobs não disponível.")
        return

    from jobstore import format_jobs_list

    # Parse optional filters: /jobs [tool] [status]
    args = context.args or []
    tool_filter = args[0] if len(args) >= 1 else None
    status_filter = args[1] if len(args) >= 2 else None
    try:
        jobs = await _job_store.list_recent(limit=10, tool_name=tool_filter, status=status_filter)
    except ValueError as e:
        await update.message.reply_text(f"Filtro inválido: {e}")
        return
    await update.message.reply_text(format_jobs_list(jobs))


async def audit_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return

    if _audit_log is None:
        await update.message.reply_text("Log de auditoria não disponível.")
        return

    args_text = " ".join(context.args).strip() if context.args else ""

    if args_text.lower() == "export":
        export_data = await _audit_log.export_json(since_hours=24)
        await update.message.reply_document(
            document=export_data.encode("utf-8"),
            filename="audit_export.json",
            caption="Exportação de auditoria (últimas 24h)",
        )
        return

    # Parse optional hours filter: /audit [hours]
    hours = 1
    if args_text and args_text.isdigit():
        hours = max(1, min(int(args_text), 720))

    summary = await _audit_log.summary(hours=hours)
    await update.message.reply_text(summary)


async def handle_message(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    config = _config
    if not await authorize(update, config):
        return

    if _rate_limiter is not None:
        user = update.effective_user
        if user and not _rate_limiter.check(user.id):
            remaining = _rate_limiter.remaining(user.id)
            await update.message.reply_text(
                f"Rate limit atingido. Aguarde alguns segundos. ({remaining} requisições restantes)"
            )
            return

    user_message = (update.message.text or "").strip()
    if not user_message:
        return

    route_name = classify_text_route(user_message)
    await handle_chat_prompt(update, user_message, route_name, config)


def _init_subsystems() -> None:
    """Initialize metrics, job store, audit log, and agent subsystems."""
    global log_event, _job_store, _audit_log, _conversation_manager

    # Metrics: wrap log_event to feed the collector
    from metrics import install_metrics

    log_event = install_metrics(log_event)

    # Job store: persistent SQLite tracking
    from jobstore import JobStore

    _job_store = JobStore()
    lost_count = _job_store._mark_lost_on_restart_sync()
    if lost_count:
        log_event("jobstore.restart.lost", count=lost_count)

    # Audit log: persistent event trail
    from audit import AuditLog, install_audit

    _audit_log = AuditLog()
    log_event = install_audit(log_event, _audit_log)

    # Rate limiter
    global _rate_limiter
    from ratelimit import RateLimiter
    rate_limit = parse_int_env("RATE_LIMIT_PER_MINUTE", 30)
    _rate_limiter = RateLimiter(max_requests=rate_limit, window_seconds=60)

    # Conversation manager + agent tools
    config = _config or load_config()
    if config.get("agent_mode_enabled", True):
        from memory_store import ConversationManager
        _conversation_manager = ConversationManager(db_path=config.get("memory_db_path"))
        log_event("agent.init", memory_db=_conversation_manager._db_path)

        # Register all agent tools (imports trigger @tool decorator registration)
        try:
            import tools as _agent_tools  # noqa: F401
            from agent import ToolRegistry
            tool_names = ToolRegistry.list_tools()
            log_event("agent.tools.registered", count=len(tool_names), tools=tool_names)
        except Exception as exc:
            LOGGER.warning("Failed to register agent tools: %s", exc)


def main() -> None:
    global _config
    _config = load_config()
    config = _config
    if not config["telegram_token"] or config["allowed_user_id"] == 0:
        print("ERRO: Configure o TELEGRAM_TOKEN e o ALLOWED_USER_ID no arquivo .env")
        return

    _init_subsystems()

    log_event("gateway.start", allowed_user_id=config["allowed_user_id"], api_url=config["api_url"])
    print("Iniciando Shadow-Claw Gateway...")
    application = Application.builder().token(config["telegram_token"]).build()

    application.add_handler(CommandHandler("start", start))
    application.add_handler(CommandHandler("help", help_command))
    application.add_handler(CommandHandler("health", health))
    application.add_handler(CommandHandler("code", code_command))
    application.add_handler(CommandHandler("metrics", metrics_command))
    application.add_handler(CommandHandler("jobs", jobs_command))
    application.add_handler(CommandHandler("audit", audit_command))
    application.add_handler(CommandHandler("reload", reload_command))
    application.add_handler(CommandHandler("autoresearch", autoresearch_command))
    application.add_handler(CommandHandler("ruflo", ruflo_command))
    application.add_handler(CommandHandler("browseruse", browseruse_command))
    application.add_handler(MessageHandler(filters.Regex(r"^/browser-use(?:\s|$)") & filters.TEXT, browseruse_alias))
    application.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, handle_message))

    print("Gateway conectado ao Telegram com sucesso! Pressione Ctrl+C para parar.")
    application.run_polling(allowed_updates=Update.ALL_TYPES)


if __name__ == "__main__":
    main()
