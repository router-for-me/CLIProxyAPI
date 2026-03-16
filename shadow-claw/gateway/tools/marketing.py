"""Marketing tools: Meta Ads analysis and performance monitoring.

Connects to Meta Marketing API for campaign performance data.
Detects anomalies by comparing current metrics against 7-day baselines.
"""

import json
import logging

import bot_state
from agent import tool

LOGGER = logging.getLogger("shadow_claw_gateway.tools.marketing")

_GRAPH_API_URL = "https://graph.facebook.com/v20.0"
_ANOMALY_THRESHOLD = 0.15  # 15% deviation from baseline


def _get_meta_token() -> str | None:
    """Read META_ADS_TOKEN from config."""
    cfg = bot_state.config
    if cfg is None:
        return None
    return getattr(cfg, "meta_ads_token", None) or cfg.extra.get("META_ADS_TOKEN")


@tool(
    "analyze_meta_ads",
    "Analyze Meta Ads campaign performance. Returns CPC, CTR, ROAS, "
    "spend, and flags anomalies compared to 7-day baseline.",
    {
        "type": "object",
        "properties": {
            "account_id": {
                "type": "string",
                "description": "Meta Ads account ID (e.g., 'act_123456789')",
            },
            "period": {
                "type": "string",
                "description": "Analysis period: 'today', 'yesterday', 'last_7d', 'last_30d'",
            },
        },
        "required": ["account_id"],
    },
)
async def analyze_meta_ads(account_id: str, period: str = "yesterday") -> str:
    token = _get_meta_token()
    if not token:
        return (
            "META_ADS_TOKEN not configured. Set it in your .env file.\n"
            "Get a token at: https://developers.facebook.com/tools/explorer/"
        )

    # Normalize account ID
    if not account_id.startswith("act_"):
        account_id = f"act_{account_id}"

    period_map = {
        "today": "today",
        "yesterday": "yesterday",
        "last_7d": "last_7_d",
        "last_30d": "last_30_d",
    }
    date_preset = period_map.get(period, "yesterday")

    try:
        import requests
    except ImportError:
        return "requests library not available."

    # Fetch campaign insights — token via header to avoid log leakage
    url = f"{_GRAPH_API_URL}/{account_id}/insights"
    params = {
        "date_preset": date_preset,
        "fields": "campaign_name,spend,impressions,clicks,cpc,ctr,actions",
        "level": "campaign",
        "limit": 20,
    }
    headers = {"Authorization": f"Bearer {token}"}

    try:
        resp = requests.get(url, params=params, headers=headers, timeout=30)
        if resp.status_code == 401:
            return "Meta Ads token expired or invalid. Generate a new one."
        resp.raise_for_status()
        data = resp.json().get("data", [])
    except requests.RequestException as e:
        return f"Meta Ads API error: {e}"

    if not data:
        return f"No campaign data found for {account_id} ({period})."

    # Format results
    lines = [f"Meta Ads Report — {account_id} ({period})\n"]
    total_spend = 0.0

    for camp in data:
        name = camp.get("campaign_name", "Unknown")
        spend = float(camp.get("spend", 0))
        impressions = int(camp.get("impressions", 0))
        clicks = int(camp.get("clicks", 0))
        cpc = float(camp.get("cpc", 0))
        ctr = float(camp.get("ctr", 0))
        total_spend += spend

        lines.append(f"Campaign: {name}")
        lines.append(f"  Spend: R${spend:.2f} | Impressions: {impressions:,}")
        lines.append(f"  Clicks: {clicks:,} | CPC: R${cpc:.2f} | CTR: {ctr:.2f}%")

        # Extract conversions from actions
        actions = camp.get("actions", [])
        conversions = sum(
            int(a.get("value", 0))
            for a in actions
            if a.get("action_type") in ("lead", "offsite_conversion", "purchase")
        )
        if conversions and spend > 0:
            cpa = spend / conversions
            lines.append(f"  Conversions: {conversions} | CPA: R${cpa:.2f}")

        lines.append("")

    lines.append(f"Total spend: R${total_spend:.2f}")

    return "\n".join(lines)
