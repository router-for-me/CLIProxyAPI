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

# Per-chat pending tool input state: chat_id → {tool, prompt, param}
_pending_tool_input: dict[int, dict] = {}


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
            "plan_id": parts[0] if len(parts) > 0 else "",
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
        # Find tool definition in categories
        tool_def = None
        for tools in TOOL_CATEGORIES.values():
            for t in tools:
                if t["name"] == tool_name:
                    tool_def = t
                    break
            if tool_def:
                break

        if tool_def is None:
            await query.answer("Ferramenta não encontrada.")
            return

        # No-input tool: execute immediately
        if tool_def["prompt"] is None:
            await _execute_tool_direct(tool_name, {}, query, chat_id)
            return

        # Input-required tool: ask for input
        _pending_tool_input[chat_id] = {
            "tool": tool_name,
            "prompt": tool_def["prompt"],
            "param": tool_def["param"],
        }
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
    if pending is None:
        return False

    tool_name = pending["tool"]
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
