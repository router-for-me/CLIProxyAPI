"""Finance tools: stock/crypto quotes and analysis.

Inspired by virattt/ai-hedge-fund. Uses yfinance for market data.
"""

import json
import logging

from agent import tool

LOGGER = logging.getLogger("shadow_claw_gateway.tools.finance")


def _get_yfinance():
    try:
        import yfinance
        return yfinance
    except ImportError:
        return None


@tool(
    "finance_quote",
    "Get current stock or cryptocurrency price, change, and volume. "
    "Use standard tickers like AAPL, GOOGL, BTC-USD, ETH-USD.",
    {
        "type": "object",
        "properties": {
            "ticker": {
                "type": "string",
                "description": "Stock or crypto ticker symbol (e.g., 'AAPL', 'BTC-USD')",
            },
        },
        "required": ["ticker"],
    },
)
async def finance_quote(ticker: str) -> str:
    yf = _get_yfinance()
    if yf is None:
        return "yfinance is required. Install with: pip install yfinance"

    try:
        stock = yf.Ticker(ticker)
        info = stock.fast_info
        price = getattr(info, "last_price", None)
        prev_close = getattr(info, "previous_close", None)
        market_cap = getattr(info, "market_cap", None)

        if price is None:
            return f"Could not find data for ticker '{ticker}'. Check the symbol."

        change = ((price - prev_close) / prev_close * 100) if prev_close else None
        change_str = f"{change:+.2f}%" if change is not None else "N/A"

        result = {
            "ticker": ticker.upper(),
            "price": round(price, 2),
            "change": change_str,
            "previous_close": round(prev_close, 2) if prev_close else None,
            "market_cap": market_cap,
        }
        return json.dumps(result, indent=2)
    except Exception as e:
        return f"Error fetching quote for {ticker}: {e}"


@tool(
    "finance_analyze",
    "Get technical analysis summary for a stock or crypto ticker. "
    "Includes recent price history and basic indicators.",
    {
        "type": "object",
        "properties": {
            "ticker": {
                "type": "string",
                "description": "Ticker symbol to analyze",
            },
        },
        "required": ["ticker"],
    },
)
async def finance_analyze(ticker: str) -> str:
    yf = _get_yfinance()
    if yf is None:
        return "yfinance is required. Install with: pip install yfinance"

    try:
        stock = yf.Ticker(ticker)
        hist = stock.history(period="1mo")
        if hist.empty:
            return f"No historical data for '{ticker}'."

        closes = hist["Close"].tolist()
        volumes = hist["Volume"].tolist()
        high = max(closes)
        low = min(closes)
        avg_volume = sum(volumes) / len(volumes) if volumes else 0
        current = closes[-1]

        # Simple moving averages
        sma_5 = sum(closes[-5:]) / min(5, len(closes))
        sma_20 = sum(closes[-20:]) / min(20, len(closes))

        trend = "bullish" if sma_5 > sma_20 else "bearish"

        result = {
            "ticker": ticker.upper(),
            "period": "1 month",
            "current": round(current, 2),
            "high": round(high, 2),
            "low": round(low, 2),
            "sma_5": round(sma_5, 2),
            "sma_20": round(sma_20, 2),
            "trend": trend,
            "avg_daily_volume": int(avg_volume),
        }
        return json.dumps(result, indent=2)
    except Exception as e:
        return f"Error analyzing {ticker}: {e}"


@tool(
    "finance_news",
    "Get latest financial news for a query or ticker.",
    {
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "Search query or ticker for financial news",
            },
        },
        "required": ["query"],
    },
)
async def finance_news(query: str) -> str:
    yf = _get_yfinance()
    if yf is None:
        return "yfinance is required. Install with: pip install yfinance"

    try:
        stock = yf.Ticker(query)
        news = stock.news or []
        if not news:
            return f"No recent news found for '{query}'."

        items = []
        for item in news[:5]:
            title = item.get("title", "No title")
            publisher = item.get("publisher", "")
            link = item.get("link", "")
            items.append(f"- {title} ({publisher})\n  {link}")

        return f"News for '{query}':\n\n" + "\n\n".join(items)
    except Exception as e:
        return f"Error fetching news for {query}: {e}"
