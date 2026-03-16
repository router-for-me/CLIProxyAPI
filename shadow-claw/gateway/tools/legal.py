"""Legal tools: court case monitoring via INTIMA.AI and PJe.

Provides tools for checking case status, listing deadlines,
and monitoring court movements.
"""

import json
import logging

import bot_state
from agent import tool

LOGGER = logging.getLogger("shadow_claw_gateway.tools.legal")

_INTIMA_API_URL = "https://api.intima.ai/v2"


def _get_intima_token() -> str | None:
    cfg = bot_state.config
    if cfg is None:
        return None
    return getattr(cfg, "intima_ai_token", None) or cfg.extra.get("INTIMA_AI_TOKEN")


def _intima_request(endpoint: str, params: dict | None = None) -> dict:
    """Make an authenticated request to INTIMA.AI API."""
    import requests

    token = _get_intima_token()
    if not token:
        raise ValueError(
            "INTIMA_AI_TOKEN not configured. Set it in your .env file.\n"
            "Get a token at: https://app.intima.ai/"
        )

    headers = {"Authorization": f"Bearer {token}", "Accept": "application/json"}
    resp = requests.get(
        f"{_INTIMA_API_URL}/{endpoint}",
        params=params or {},
        headers=headers,
        timeout=30,
    )
    resp.raise_for_status()
    return resp.json()


@tool(
    "check_case_status",
    "Check the current status and recent movements of a court case. "
    "Uses INTIMA.AI to query across 90+ Brazilian tribunais.",
    {
        "type": "object",
        "properties": {
            "processo": {
                "type": "string",
                "description": "Case number (e.g., '0001234-56.2026.5.01.0001')",
            },
        },
        "required": ["processo"],
    },
)
async def check_case_status(processo: str) -> str:
    processo = processo.strip()
    if not processo:
        return "Please provide a case number."

    try:
        data = _intima_request("processos/consulta", {"numero": processo})
    except ValueError as e:
        return str(e)
    except Exception as e:
        LOGGER.error("INTIMA.AI request failed: %s", e)
        return f"Failed to query INTIMA.AI: {e}"

    if not data.get("data"):
        return f"No case found for: {processo}"

    case = data["data"] if isinstance(data["data"], dict) else data["data"][0]

    lines = [f"📋 Processo: {processo}"]

    tribunal = case.get("tribunal", case.get("orgao", ""))
    if tribunal:
        lines.append(f"🏛 Tribunal: {tribunal}")

    status = case.get("status", case.get("situacao", ""))
    if status:
        lines.append(f"📊 Status: {status}")

    partes = case.get("partes", [])
    if partes:
        lines.append("\n👥 Partes:")
        for parte in partes[:5]:
            nome = parte.get("nome", "")
            tipo = parte.get("tipo", "")
            lines.append(f"  - {tipo}: {nome}")

    movimentacoes = case.get("movimentacoes", case.get("andamentos", []))
    if movimentacoes:
        lines.append("\n📝 Últimas movimentações:")
        for mov in movimentacoes[:5]:
            data_mov = mov.get("data", "")
            desc = mov.get("descricao", mov.get("texto", ""))[:200]
            lines.append(f"  [{data_mov}] {desc}")

    return "\n".join(lines)


@tool(
    "list_deadlines",
    "List all active court deadlines (prazos) with days remaining. "
    "Critical for avoiding missed deadlines (OAB disciplinary risk).",
    {
        "type": "object",
        "properties": {
            "status": {
                "type": "string",
                "description": "Filter: 'active' (default), 'all', 'expired'",
            },
        },
    },
)
async def list_deadlines(status: str = "active") -> str:
    try:
        data = _intima_request("prazos", {"status": status})
    except ValueError as e:
        return str(e)
    except Exception as e:
        LOGGER.error("INTIMA.AI prazos request failed: %s", e)
        return f"Failed to list deadlines: {e}"

    prazos = data.get("data", [])
    if not prazos:
        return "No active deadlines found. ✅"

    lines = ["⏰ **Prazos Ativos:**\n"]
    for prazo in prazos:
        processo = prazo.get("processo", prazo.get("numero_processo", "?"))
        tipo = prazo.get("tipo", prazo.get("tipo_prazo", ""))
        dias = prazo.get("dias_restantes", prazo.get("prazo_dias", "?"))
        vencimento = prazo.get("vencimento", prazo.get("data_limite", ""))

        if isinstance(dias, int) and dias <= 3:
            emoji = "🔴"
        elif isinstance(dias, int) and dias <= 7:
            emoji = "🟡"
        else:
            emoji = "🔵"

        lines.append(f"{emoji} **{processo}**")
        lines.append(f"   {tipo} — {dias} dias restantes (vence {vencimento})")
        lines.append("")

    return "\n".join(lines)
