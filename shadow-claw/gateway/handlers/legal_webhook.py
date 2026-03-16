"""INTIMA.AI webhook handler for court movement notifications.

Receives POST requests from INTIMA.AI when new movimentações are detected,
classifies urgency, and sends Telegram alerts with inline action buttons.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import logging
import time
from dataclasses import dataclass
from enum import Enum

LOGGER = logging.getLogger("shadow_claw_gateway.handlers.legal_webhook")

# ---------------------------------------------------------------------------
# Urgency classification
# ---------------------------------------------------------------------------

class Urgency(str, Enum):
    CRITICAL = "critical"   # 🔴 prazo < 5 dias úteis
    IMPORTANT = "important"  # 🟡 intimação, citação
    INFO = "info"           # 🔵 juntada, certidão, despacho de mero expediente

_URGENCY_EMOJI = {
    Urgency.CRITICAL: "🔴",
    Urgency.IMPORTANT: "🟡",
    Urgency.INFO: "🔵",
}

# Keywords that indicate urgency (Portuguese legal terms)
_CRITICAL_KEYWORDS = frozenset({
    "prazo", "contestação", "recurso", "embargos", "impugnação",
    "manifestação", "réplica", "contrarrazões", "apelação",
})

_IMPORTANT_KEYWORDS = frozenset({
    "intimação", "citação", "notificação", "audiência",
    "despacho", "decisão", "sentença", "acórdão",
})


@dataclass
class CourtMovement:
    """Parsed court movement from INTIMA.AI webhook payload."""
    processo: str
    tribunal: str
    tipo: str
    descricao: str
    data: str
    prazo_dias: int | None = None
    urgency: Urgency = Urgency.INFO


def classify_urgency(tipo: str, descricao: str, prazo_dias: int | None = None) -> Urgency:
    """Classify movement urgency based on type, description, and deadline."""
    if prazo_dias is not None and prazo_dias <= 5:
        return Urgency.CRITICAL

    text = f"{tipo} {descricao}".lower()

    if any(kw in text for kw in _CRITICAL_KEYWORDS):
        return Urgency.CRITICAL if prazo_dias is not None else Urgency.IMPORTANT

    if any(kw in text for kw in _IMPORTANT_KEYWORDS):
        return Urgency.IMPORTANT

    return Urgency.INFO


def validate_webhook_signature(payload: bytes, signature: str, secret: str) -> bool:
    """Validate INTIMA.AI webhook HMAC-SHA256 signature."""
    expected = hmac.new(
        secret.encode("utf-8"),
        payload,
        hashlib.sha256,
    ).hexdigest()
    return hmac.compare_digest(expected, signature)


def parse_webhook_payload(data: dict) -> CourtMovement:
    """Parse INTIMA.AI webhook JSON into a CourtMovement."""
    processo = data.get("processo", data.get("numero_processo", ""))
    tribunal = data.get("tribunal", data.get("orgao", ""))
    tipo = data.get("tipo", data.get("tipo_movimentacao", ""))
    descricao = data.get("descricao", data.get("texto", ""))
    data_mov = data.get("data", data.get("data_movimentacao", ""))
    prazo_dias = data.get("prazo_dias", data.get("dias_prazo"))

    if prazo_dias is not None:
        try:
            prazo_dias = int(prazo_dias)
        except (ValueError, TypeError):
            prazo_dias = None

    urgency = classify_urgency(tipo, descricao, prazo_dias)

    return CourtMovement(
        processo=processo,
        tribunal=tribunal,
        tipo=tipo,
        descricao=descricao,
        data=data_mov,
        prazo_dias=prazo_dias,
        urgency=urgency,
    )


def format_telegram_alert(movement: CourtMovement) -> str:
    """Format a court movement as a Telegram message."""
    emoji = _URGENCY_EMOJI[movement.urgency]
    lines = [
        f"{emoji} **{movement.urgency.value.upper()}** — Movimentação Processual",
        "",
        f"📋 **Processo:** {movement.processo}",
        f"🏛 **Tribunal:** {movement.tribunal}",
        f"📝 **Tipo:** {movement.tipo}",
    ]

    if movement.prazo_dias is not None:
        lines.append(f"⏰ **Prazo:** {movement.prazo_dias} dias úteis")

    lines.extend([
        "",
        f"📄 {movement.descricao[:500]}",
        "",
        f"📅 {movement.data}",
    ])

    return "\n".join(lines)
