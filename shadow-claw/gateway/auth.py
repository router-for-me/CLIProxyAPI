from telegram import Update

from config import load_config


def check_auth(user_id: int, config: dict | None = None) -> bool:
    active_config = config or load_config()
    return user_id == active_config["allowed_user_id"]


def update_context(update: Update | None) -> dict:
    if update is None:
        return {}
    user = getattr(update, "effective_user", None)
    chat = getattr(update, "effective_chat", None)
    return {
        "update_id": getattr(update, "update_id", None),
        "telegram_user_id": getattr(user, "id", None),
        "chat_id": getattr(chat, "id", None),
    }


async def authorize(update: Update, config: dict, reply_on_denied: bool = False) -> bool:
    """Check if the effective user is authorized. Logs denial events."""
    import bot_state

    user = update.effective_user
    if not user or check_auth(user.id, config):
        return True

    bot_state.log_event("gateway.auth.denied", **update_context(update))
    if reply_on_denied and update.message:
        await update.message.reply_text(
            "⛔ Acesso negado. Você não tem permissão para falar com este agente."
        )
    return False
