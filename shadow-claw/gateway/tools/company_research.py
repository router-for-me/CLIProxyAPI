"""Company research tools: due diligence and corporate intelligence.

Wraps gpt-researcher for company research and provides structured
due diligence reports via Telegram.
"""

import asyncio
import json
import logging
import re

from agent import tool
from tools.osint import _run_cmd

LOGGER = logging.getLogger("shadow_claw_gateway.tools.company_research")


def _sanitize_query(query: str) -> str:
    """Remove potentially dangerous characters from research queries."""
    return re.sub(r"[;&|`$(){}]", "", query).strip()[:500]


@tool(
    "research_company",
    "Conduct comprehensive due diligence research on a company. "
    "Returns financials, news, structure, and risk assessment.",
    {
        "type": "object",
        "properties": {
            "company": {
                "type": "string",
                "description": "Company name, CNPJ, or domain",
            },
            "focus": {
                "type": "string",
                "description": "Research focus: 'general', 'financial', 'legal', 'reputation'",
            },
        },
        "required": ["company"],
    },
)
async def research_company(company: str, focus: str = "general") -> str:
    company = _sanitize_query(company)
    if not company:
        return "Please provide a company name, CNPJ, or domain."

    focus_prompts = {
        "general": f"Comprehensive company research on {company}: overview, financials, key people, news, risks",
        "financial": f"Financial analysis of {company}: revenue, debt, profitability, recent financial news",
        "legal": f"Legal analysis of {company}: lawsuits, regulatory issues, compliance, court cases in Brazil",
        "reputation": f"Reputation analysis of {company}: reviews, complaints, social media sentiment, news",
    }

    query = focus_prompts.get(focus, focus_prompts["general"])

    # Try gpt-researcher CLI first
    import shutil
    if shutil.which("gpt-researcher"):
        cmd = [
            "gpt-researcher",
            query,
            "--report_type", "research_report",
        ]
        LOGGER.info("Running gpt-researcher for company: %s", company)
        rc, stdout, stderr = await _run_cmd(cmd, timeout=180)

        if rc == 0 and stdout.strip():
            output = stdout.strip()
            if len(output) > 4000:
                output = output[:4000] + "\n\n... (truncated — full report available)"
            return f"Company Research: {company}\n\n{output}"

        if rc == -1:
            LOGGER.warning("gpt-researcher timed out, falling back to browse")

    # Fallback: use browser search
    from tools.browser import browse_search
    results = await browse_search(query)
    return f"Company Research: {company}\n\n{results}"
