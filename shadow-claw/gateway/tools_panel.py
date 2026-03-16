"""Interactive tools panel: inline keyboard UI for Shadow-Claw agent tools.

Provides a /tools command that shows categorized tool buttons.
Users tap a category → see tools → tap a tool → either runs immediately
(no-input tools) or prompts for input.

Callback data convention: tools:<action>:<param>
  - tools:cat:<category>   → show category sub-panel
  - tools:run:<tool_name>  → execute a tool
  - tools:back:main        → return to main panel
"""

import logging
import re
import time
from contextlib import suppress

from telegram import InlineKeyboardButton, InlineKeyboardMarkup, Update
from telegram.ext import ContextTypes

import bot_state
from auth import check_auth
from chat_client import safe_edit_text
from config import MAX_TELEGRAM_MESSAGE_LENGTH, truncate_text

LOGGER = logging.getLogger("shadow_claw_gateway.tools_panel")

# ---------------------------------------------------------------------------
# Category → tool mapping + UI labels
# ---------------------------------------------------------------------------

TOOL_CATEGORIES: dict[str, list[dict]] = {
    "inbox": [
        {"name": "inbox_list_unread", "label": "📬 Não lidos", "prompt": None, "param": None},
        {"name": "inbox_search", "label": "🔎 Buscar", "prompt": "Digite o remetente, assunto ou palavra-chave:", "param": "query"},
        {"name": "inbox_reply", "label": "↩️ Responder", "prompt": "Digite: thread_id | mensagem de resposta:", "param": "thread_reply"},
    ],
    "calendar": [
        {"name": "calendar_today", "label": "📅 Hoje", "prompt": None, "param": None},
        {"name": "calendar_list_events", "label": "📆 Eventos", "prompt": "Digite a data (AAAA-MM-DD) ou intervalo (início | fim):", "param": "date_range"},
        {"name": "calendar_create_event", "label": "➕ Criar", "prompt": "Digite: título | início | fim (ex: Reunião | 2024-01-15 10:00 | 2024-01-15 11:00):", "param": "event_create"},
        {"name": "calendar_find_free_time", "label": "🕐 Horário Livre", "prompt": "Digite: data | duração em min (ex: 2024-01-15 | 30):", "param": "free_time"},
    ],
    "memory": [
        {"name": "memory_store", "label": "💾 Salvar", "prompt": "O que deseja memorizar? (formato: chave | conteúdo)", "param": "key_content"},
        {"name": "memory_recall", "label": "🔍 Buscar", "prompt": "Digite o que deseja lembrar:", "param": "query"},
        {"name": "memory_list", "label": "📋 Listar", "prompt": None, "param": None},
    ],
    "browser": [
        {"name": "browse_url", "label": "🌍 Abrir URL", "prompt": "Digite a URL:", "param": "url"},
        {"name": "browse_search", "label": "🔎 Pesquisar", "prompt": "Digite sua pesquisa:", "param": "query"},
    ],
    "scraper": [
        {"name": "scrape_data", "label": "📄 Extrair Dados", "prompt": "Digite a URL para extrair:", "param": "url"},
        {"name": "scrape_links", "label": "🔗 Extrair Links", "prompt": "Digite a URL para extrair links:", "param": "url"},
    ],
    "finance": [
        {"name": "finance_quote", "label": "📈 Cotação", "prompt": "Digite o ticker (ex: AAPL, BTC-USD):", "param": "ticker"},
        {"name": "finance_analyze", "label": "📊 Análise", "prompt": "Digite o ticker para analisar:", "param": "ticker"},
        {"name": "finance_news", "label": "📰 Notícias", "prompt": "Digite o ticker ou assunto:", "param": "query"},
    ],
    "research": [
        {"name": "research_topic", "label": "🔬 Pesquisar", "prompt": "Digite o tema de pesquisa:", "param": "query"},
    ],
    "planner": [
        {"name": "plan_task", "label": "📝 Criar Plano", "prompt": "Descreva o objetivo do plano:", "param": "objective"},
        {"name": "plan_execute", "label": "✅ Atualizar", "prompt": "Digite: plan_id | step | status (ex: plan_1 | 1 | completed):", "param": "plan_update"},
    ],
    "security": [
        {"name": "security_headers", "label": "🔒 Headers", "prompt": "Digite a URL para verificar headers:", "param": "url"},
        {"name": "security_scan", "label": "🛡️ Scan", "prompt": "Digite a URL para escanear:", "param": "target_url"},
    ],
    "desktop": [
        {"name": "desktop_screenshot", "label": "📸 Screenshot", "prompt": None, "param": None},
        {"name": "desktop_command", "label": "⌨️ Comando", "prompt": "Digite: ação | valor (ex: type | hello):", "param": "action_value"},
    ],
    "voice": [
        {"name": "voice_speak", "label": "🗣️ Falar", "prompt": "Digite o texto para converter em áudio:", "param": "text"},
    ],
    "payments": [
        {"name": "payment_check_balance", "label": "💰 Saldo", "prompt": None, "param": None},
        {"name": "payment_send", "label": "💸 Enviar", "prompt": "Digite: valor | destinatário (ex: 50 | email@test.com):", "param": "amount_recipient"},
    ],
}

CATEGORY_LABELS: dict[str, str] = {
    "inbox": "📬 Caixa de Entrada",
    "calendar": "📅 Calendário",
    "memory": "🧠 Memória",
    "browser": "🌐 Browser",
    "scraper": "🕷️ Scraper",
    "finance": "📊 Finanças",
    "research": "🔍 Pesquisa",
    "planner": "📋 Planner",
    "security": "🛡️ Segurança",
    "desktop": "🖥️ Desktop",
    "voice": "🔊 Voz",
    "payments": "💳 Pagamentos",
}

# Per-chat pending tool input state: chat_id → tool flow context.
# Timestamps are kept in a parallel dict (_pending_ts) so the state dicts
# remain clean. _PENDING_TTL_SECONDS is the eviction window;
# _PENDING_MAX_SIZE guards against unbounded growth.
_pending_tool_input: dict[int, dict] = {}
_pending_ts: dict[int, float] = {}
_PENDING_TTL_SECONDS: float = 1800.0  # 30 minutes
_PENDING_MAX_SIZE: int = 500


def _set_pending(chat_id: int, state: dict) -> None:
    """Store pending tool state with TTL and max-size eviction."""
    now = time.monotonic()
    # Evict expired entries
    expired = [k for k, ts in _pending_ts.items() if now - ts > _PENDING_TTL_SECONDS]
    for k in expired:
        _pending_tool_input.pop(k, None)
        _pending_ts.pop(k, None)
    # If still over the cap, evict oldest by insertion order (dicts are ordered in 3.7+)
    if len(_pending_tool_input) >= _PENDING_MAX_SIZE:
        oldest = next(iter(_pending_tool_input))
        del _pending_tool_input[oldest]
        _pending_ts.pop(oldest, None)
    _pending_tool_input[chat_id] = state
    _pending_ts[chat_id] = now


# Flat lookup: tool_name → tool definition dict (built once at module load).
_TOOL_LOOKUP: dict[str, dict] = {
    t["name"]: t
    for tools in TOOL_CATEGORIES.values()
    for t in tools
}


def _parse_plan_steps(text: str) -> list[str]:
    """Parse steps entered either one per line or pipe-separated."""
    raw_steps = text.split("|") if "|" in text else text.splitlines()
    return [step.strip() for step in raw_steps if step.strip()]


def _is_explicit_confirmation(text: str) -> bool:
    """Require an explicit confirmation token before sending payments."""
    return text.strip().lower() in {"sim", "yes", "confirmo", "confirmar", "confirm"}


def _extract_payment_id(text: str) -> str | None:
    """Extract a payment ID from the payment tool's confirmation response."""
    match = re.search(r"Payment ID:\s*(\S+)", text)
    return match.group(1) if match else None


# ---------------------------------------------------------------------------
# Panel builders
# ---------------------------------------------------------------------------


def _build_main_panel() -> InlineKeyboardMarkup:
    """Build the 10-category main panel (2 buttons per row)."""
    cats = list(CATEGORY_LABELS.items())
    rows = []
    for i in range(0, len(cats), 2):
        row = [
            InlineKeyboardButton(label, callback_data=f"tools:cat:{key}")
            for key, label in cats[i : i + 2]
        ]
        rows.append(row)
    return InlineKeyboardMarkup(rows)


def _build_category_panel(category: str) -> InlineKeyboardMarkup:
    """Build sub-panel for a category with tool buttons + back."""
    tools = TOOL_CATEGORIES.get(category, [])
    rows = []
    for i in range(0, len(tools), 2):
        row = [
            InlineKeyboardButton(t["label"], callback_data=f"tools:run:{t['name']}")
            for t in tools[i : i + 2]
        ]
        rows.append(row)
    rows.append([InlineKeyboardButton("⬅ Voltar", callback_data="tools:back:main")])
    return InlineKeyboardMarkup(rows)


# ---------------------------------------------------------------------------
# Handlers
# ---------------------------------------------------------------------------


async def tools_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    """Send the interactive tools panel."""
    from auth import authorize

    if not await authorize(update, bot_state.config):
        return
    await update.message.reply_text(
        "🛠️ Painel de Ferramentas\nEscolha uma categoria:",
        reply_markup=_build_main_panel(),
    )


async def _execute_tool_direct(tool_name: str, args: dict, query, chat_id: int) -> None:
    """Run a no-input tool via ToolRegistry and answer the callback."""
    from agent import ToolRegistry

    await query.answer()
    try:
        await query.edit_message_text(f"⏳ Executando {tool_name}...")
        result = await ToolRegistry.invoke(tool_name, args, log_event=bot_state.log_event)
        display = truncate_text(result, MAX_TELEGRAM_MESSAGE_LENGTH)
        await query.edit_message_text(display)
    except Exception as exc:
        LOGGER.warning("Tool panel exec failed for %s: %s", tool_name, exc)
        with suppress(Exception):
            await query.edit_message_text(f"Erro ao executar {tool_name}: {exc}")


def _parse_tool_input(tool_name: str, param_type: str, text: str) -> dict:
    """Parse pipe-separated user text into the tool's keyword arguments."""
    if param_type == "key_content":
        parts = text.split("|", 1)
        key = parts[0].strip()
        content = parts[1].strip() if len(parts) > 1 else key
        return {"key": key, "content": content}
    if param_type == "plan_update":
        parts = [p.strip() for p in text.split("|")]
        return {
            "plan_id": parts[0],
            "step_number": int(parts[1]) if len(parts) > 1 and parts[1].isdigit() else 1,
            "status": parts[2] if len(parts) > 2 else "completed",
        }
    if param_type == "action_value":
        parts = text.split("|", 1)
        return {
            "action": parts[0].strip(),
            "value": parts[1].strip() if len(parts) > 1 else "",
        }
    if param_type == "amount_recipient":
        parts = text.split("|", 1)
        try:
            amount = float(parts[0].strip())
        except ValueError:
            amount = 0
        return {
            "amount": amount,
            "recipient": parts[1].strip() if len(parts) > 1 else "",
        }
    if param_type == "thread_reply":
        parts = text.split("|", 1)
        return {
            "thread_id": parts[0].strip(),
            "body": parts[1].strip() if len(parts) > 1 else "",
        }
    if param_type == "date_range":
        parts = text.split("|", 1)
        start = parts[0].strip()
        return {
            "start_date": start,
            "end_date": parts[1].strip() if len(parts) > 1 else start,
        }
    if param_type == "event_create":
        parts = [p.strip() for p in text.split("|")]
        return {
            "title": parts[0],
            "start_time": parts[1] if len(parts) > 1 else "",
            "end_time": parts[2] if len(parts) > 2 else "",
            "description": parts[3] if len(parts) > 3 else "",
        }
    if param_type == "free_time":
        parts = text.split("|", 1)
        try:
            duration = int(parts[1].strip()) if len(parts) > 1 else 30
        except ValueError:
            duration = 30
        return {
            "date": parts[0].strip(),
            "duration_minutes": duration,
        }
    # Single-param tools (query, url, ticker, text, target_url, objective)
    return {param_type: text.strip()}


async def tools_callback(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    """Handle inline keyboard taps from the tools panel."""
    query = update.callback_query
    data = query.data or ""
    chat_id = query.message.chat_id

    # Auth check: verify the user tapping the button is allowed
    user = query.from_user
    if user and not check_auth(user.id, bot_state.config):
        await query.answer("⛔ Acesso negado.", show_alert=True)
        return

    if not data.startswith("tools:"):
        return

    parts = data.split(":", 2)
    if len(parts) < 3:
        await query.answer("Dados inválidos.")
        return

    action, param = parts[1], parts[2]

    # Navigate to category sub-panel
    if action == "cat":
        if param not in TOOL_CATEGORIES:
            await query.answer("Categoria não encontrada.")
            return
        await query.answer()
        label = CATEGORY_LABELS.get(param, param)
        await query.edit_message_text(
            f"🛠️ {label}\nEscolha uma ferramenta:",
            reply_markup=_build_category_panel(param),
        )
        return

    # Back to main panel
    if action == "back":
        await query.answer()
        await query.edit_message_text(
            "🛠️ Painel de Ferramentas\nEscolha uma categoria:",
            reply_markup=_build_main_panel(),
        )
        return

    # Run a tool
    if action == "run":
        tool_name = param
        tool_def = _TOOL_LOOKUP.get(tool_name)

        if tool_def is None:
            await query.answer("Ferramenta não encontrada.")
            return

        # No-input tool: execute immediately
        if tool_def["prompt"] is None:
            await _execute_tool_direct(tool_name, {}, query, chat_id)
            return

        # Input-required tool: ask for input
        pending = {
            "tool": tool_name,
            "prompt": tool_def["prompt"],
            "param": tool_def["param"],
        }
        if tool_name == "plan_task":
            pending["stage"] = "objective"
        elif tool_name == "payment_send":
            pending["stage"] = "details"
        _set_pending(chat_id, pending)
        await query.answer()
        await query.edit_message_text(f"🔧 {tool_def['label']}\n\n{tool_def['prompt']}")
        return


async def intercept_pending_input(update: Update, user_message: str) -> bool:
    """Check for and handle pending tool input from the /tools panel.

    Returns True if the message was consumed as tool input, False otherwise.
    Called from handle_message() before the normal chat routing.
    """
    from agent import ToolRegistry

    chat_id = update.message.chat_id
    pending = _pending_tool_input.pop(chat_id, None)
    _pending_ts.pop(chat_id, None)
    if pending is None:
        return False

    tool_name = pending["tool"]
    stage = pending.get("stage")

    if tool_name == "plan_task" and stage != "steps":
        _set_pending(chat_id, {
            "tool": tool_name,
            "stage": "steps",
            "objective": user_message.strip(),
        })
        await update.message.reply_text(
            "📝 Criar Plano\n\nDigite os passos, um por linha ou separados por |."
        )
        return True

    if tool_name == "plan_task":  # stage == "steps"
        steps = _parse_plan_steps(user_message)
        if not steps:
            await update.message.reply_text("Nenhum passo válido informado. Operação cancelada.")
            return True
        args = {"objective": pending["objective"], "steps": steps}
    elif tool_name == "payment_send" and stage != "confirm":
        args = _parse_tool_input(tool_name, pending["param"], user_message)
        bot_state.log_event("tools_panel.exec", tool=tool_name, chat_id=chat_id)
        status_msg = await update.message.reply_text(f"⏳ Executando {tool_name}...")
        try:
            result = await ToolRegistry.invoke(
                tool_name,
                {**args, "confirmed": False},
                log_event=bot_state.log_event,
            )
            if result.startswith("Payment pending confirmation:"):
                payment_id = _extract_payment_id(result)
                if payment_id:
                    _set_pending(chat_id, {
                        "tool": tool_name,
                        "stage": "confirm",
                        "payment_id": payment_id,
                    })
                    result = (
                        f"{result}\n\n"
                        "Responda 'sim' para confirmar este pagamento. "
                        "Qualquer outra resposta cancela."
                    )
                else:
                    LOGGER.warning("Payment confirmation response missing payment_id for chat %s", chat_id)
                    result = f"{result}\n\nErro interno: Payment ID ausente na resposta de confirmação."
            await safe_edit_text(status_msg, truncate_text(result, MAX_TELEGRAM_MESSAGE_LENGTH))
        except Exception as exc:
            LOGGER.warning("Tool panel input exec failed for %s: %s", tool_name, exc)
            with suppress(Exception):
                await safe_edit_text(status_msg, f"Erro ao executar {tool_name}: {exc}")
        return True
    elif tool_name == "payment_send" and stage == "confirm":
        if not _is_explicit_confirmation(user_message):
            await update.message.reply_text("Pagamento cancelado.")
            return True
        payment_id = pending.get("payment_id")
        if not payment_id:
            await update.message.reply_text("Pagamento pendente inválido. Operação cancelada.")
            return True
        args = {"payment_id": payment_id, "confirmed": True}
    else:
        args = _parse_tool_input(tool_name, pending["param"], user_message)

    bot_state.log_event("tools_panel.exec", tool=tool_name, chat_id=chat_id)
    status_msg = await update.message.reply_text(f"⏳ Executando {tool_name}...")
    try:
        result = await ToolRegistry.invoke(tool_name, args, log_event=bot_state.log_event)
        await safe_edit_text(status_msg, truncate_text(result, MAX_TELEGRAM_MESSAGE_LENGTH))
    except Exception as exc:
        LOGGER.warning("Tool panel input exec failed for %s: %s", tool_name, exc)
        with suppress(Exception):
            await safe_edit_text(status_msg, f"Erro ao executar {tool_name}: {exc}")
    return True
