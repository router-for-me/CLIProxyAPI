"""Research tools: deep research on any topic.

Inspired by karpathy/autoresearch. Wraps the existing autoresearch shell
command for deep research, or uses browser tools for quick research.
"""

from agent import tool


@tool(
    "research_topic",
    "Conduct research on any topic. For quick research, synthesizes "
    "information from web searches. For deep research, runs the "
    "autoresearch tool as a background job.",
    {
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "Research query or topic",
            },
            "depth": {
                "type": "string",
                "enum": ["quick", "deep"],
                "description": "Research depth: 'quick' for immediate synthesis, "
                "'deep' for thorough background research",
            },
        },
        "required": ["query"],
    },
)
async def research_topic(query: str, depth: str = "quick") -> str:
    if depth == "deep":
        # Use existing autoresearch shell command
        try:
            import bot
            config = bot._config
            if config is None:
                return "Configuration not available."
            result = await bot.run_autoresearch(query, config)
            if result["ok"]:
                return f"Research results:\n\n{result['output']}"
            return f"Research failed: {result['output']}"
        except Exception as e:
            return f"Deep research failed: {e}"

    # Quick research: use browse_search + browse_url
    from tools.browser import browse_search, browse_url
    search_results = await browse_search(query)
    return f"Research on '{query}':\n\n{search_results}"
