"""Telegram command handlers for the Shadow-Claw gateway.

Each async function corresponds to a /command. Handlers are registered
in bot.main() to keep this module free of Application setup logic.
"""

from telegram import Update
from telegram.ext import ContextTypes

import bot_state
from auth import authorize, update_context
from config import (
    CHAT_ROUTE_CODING,
    MAX_TELEGRAM_MESSAGE_LENGTH,
    extract_prompt_from_command,
    load_config,
    truncate_text,
)
from chat_client import safe_edit_text
from tools_handler import tool_routes_enabled


# ---------------------------------------------------------------------------
# Utility commands
# ---------------------------------------------------------------------------


async def start(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not await authorize(update, bot_state.config, reply_on_denied=True):
        return
    await update.message.reply_text(
        "🤖 Olá, Mestre. Shadow-Claw Gateway online.\n\n"
        "Sistema seguro. O que faremos hoje?"
    )


async def help_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not await authorize(update, bot_state.config):
        return
    await update.message.reply_text(
        "Comandos disponíveis:\n"
        "/start - Iniciar o bot\n"
        "/health - Status do gateway\n"
        "/code <prompt> - Modo código\n"
        "/autoresearch <prompt> - Pesquisa automática\n"
        "/ruflo <prompt> - Agente RuFlo\n"
        "/browseruse <prompt> - Navegação web\n"
        "/tools - Painel interativo de ferramentas\n"
        "/jobs [tool] [status] - Listar jobs recentes\n"
        "/audit [export] - Resumo de auditoria\n"
        "/metrics - Métricas do gateway\n"
        "/reload - Recarregar configuração\n"
        "/help - Esta mensagem"
    )


async def reload_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    from dotenv import load_dotenv
    from config import ENV_PATH

    if not await authorize(update, bot_state.config, reply_on_denied=True):
        return
    load_dotenv(ENV_PATH, override=True)
    bot_state.config = load_config()
    bot_state.log_event("gateway.config.reloaded", **update_context(update))
    await update.message.reply_text("Configuração recarregada com sucesso.")


# ---------------------------------------------------------------------------
# Health & monitoring commands
# ---------------------------------------------------------------------------


async def health(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    from bot import build_health_report

    if not await authorize(update, bot_state.config):
        return
    status_msg = await update.message.reply_text("Verificando saúde do sistema...")
    report = await build_health_report(bot_state.config)
    await safe_edit_text(status_msg, report)


async def metrics_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not await authorize(update, bot_state.config):
        return
    from metrics import get_metrics_summary

    summary = get_metrics_summary()
    await update.message.reply_text(summary)


async def jobs_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not await authorize(update, bot_state.config):
        return
    if bot_state.job_store is None:
        await update.message.reply_text("Armazenamento de jobs não disponível.")
        return

    from jobstore import format_jobs_list

    args = context.args or []
    tool_filter = args[0] if len(args) >= 1 else None
    status_filter = args[1] if len(args) >= 2 else None
    try:
        jobs = await bot_state.job_store.list_recent(
            limit=10, tool_name=tool_filter, status=status_filter,
        )
    except ValueError as e:
        await update.message.reply_text(f"Filtro inválido: {e}")
        return
    await update.message.reply_text(format_jobs_list(jobs))


async def audit_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not await authorize(update, bot_state.config):
        return
    if bot_state.audit_log is None:
        await update.message.reply_text("Log de auditoria não disponível.")
        return

    args_text = " ".join(context.args).strip() if context.args else ""

    if args_text.lower() == "export":
        export_data = await bot_state.audit_log.export_json(since_hours=24)
        await update.message.reply_document(
            document=export_data.encode("utf-8"),
            filename="audit_export.json",
            caption="Exportação de auditoria (últimas 24h)",
        )
        return

    hours = 1
    if args_text and args_text.isdigit():
        hours = max(1, min(int(args_text), 720))

    summary = await bot_state.audit_log.summary(hours=hours)
    await update.message.reply_text(summary)


# ---------------------------------------------------------------------------
# Chat & tool route commands
# ---------------------------------------------------------------------------


async def code_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    from bot import handle_chat_prompt

    if not await authorize(update, bot_state.config):
        return
    prompt = " ".join(context.args).strip() or extract_prompt_from_command(
        update.message.text, "code",
    )
    if not prompt:
        await update.message.reply_text("Usage: /code <prompt>")
        return
    await handle_chat_prompt(update, prompt, CHAT_ROUTE_CODING, bot_state.config)


async def autoresearch_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    from bot import handle_tool_prompt, run_autoresearch

    if not await authorize(update, bot_state.config):
        return
    prompt = " ".join(context.args).strip() or extract_prompt_from_command(
        update.message.text, "autoresearch",
    )
    await handle_tool_prompt(update, prompt, "autoresearch", run_autoresearch, bot_state.config)


async def ruflo_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    from bot import handle_tool_prompt, run_ruflo

    if not await authorize(update, bot_state.config):
        return
    prompt = " ".join(context.args).strip() or extract_prompt_from_command(
        update.message.text, "ruflo",
    )
    await handle_tool_prompt(update, prompt, "ruflo", run_ruflo, bot_state.config)


async def browseruse_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    from bot import handle_tool_prompt, run_browser_use

    if not await authorize(update, bot_state.config):
        return
    prompt = " ".join(context.args).strip() or extract_prompt_from_command(
        update.message.text, "browseruse",
    )
    await handle_tool_prompt(update, prompt, "browser-use", run_browser_use, bot_state.config)


async def browseruse_alias(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    from bot import handle_tool_prompt, run_browser_use

    if not await authorize(update, bot_state.config):
        return
    prompt = extract_prompt_from_command(update.message.text, "browser-use")
    await handle_tool_prompt(update, prompt, "browser-use", run_browser_use, bot_state.config)
