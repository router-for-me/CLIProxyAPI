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
