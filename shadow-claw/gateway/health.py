import requests

from config import build_proxy_endpoint, proxy_headers
from chat_client import response_error_details, get_proxy_endpoint


async def probe_keep_alive(config: dict) -> dict:
    url = build_proxy_endpoint(config, "/keep-alive")
    try:
        response = await get_proxy_endpoint(url, config, config["health_timeout_seconds"])
    except requests.RequestException as error:
        return {"ok": False, "message": str(error)}

    if response.status_code == 200:
        return {"ok": True, "message": "ok"}

    return {"ok": False, "message": f"HTTP {response.status_code}: {response_error_details(response)}"}


async def fetch_models(config: dict) -> dict:
    url = build_proxy_endpoint(config, "/v1/models")
    try:
        response = await get_proxy_endpoint(url, config, config["health_timeout_seconds"])
    except requests.RequestException as error:
        return {"ok": False, "message": str(error), "model_ids": []}

    if response.status_code != 200:
        return {
            "ok": False,
            "message": f"HTTP {response.status_code}: {response_error_details(response)}",
            "model_ids": [],
        }

    try:
        body = response.json()
        model_ids = sorted(
            item.get("id", "")
            for item in body.get("data", [])
            if isinstance(item, dict) and item.get("id")
        )
    except Exception as error:
        return {"ok": False, "message": f"invalid model response: {error}", "model_ids": []}

    return {"ok": True, "message": f"{len(model_ids)} models visible", "model_ids": model_ids}
