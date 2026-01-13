#!/usr/bin/env python3
"""
Gemini 2.5 Computer Use Agent
Uses CLIProxyAPI to control a browser via Gemini's computer use model.
"""

import sys
import json
import base64
import time
import re
import requests
from playwright.sync_api import sync_playwright

PROXY_URL = "http://127.0.0.1:8317"
API_KEY = "sk-proxy"
MODEL = "gemini-2.5-computer-use-preview-10-2025"
SCREEN_WIDTH = 1440
SCREEN_HEIGHT = 900
MAX_TURNS = 10
NORMALIZED_COORD_MAX = 1000


def send_to_model(screenshot_b64, prompt, history):
    """Send screenshot and prompt to Gemini computer use model."""
    parts = [
        {"text": prompt},
        {"inlineData": {"mimeType": "image/png", "data": screenshot_b64}}
    ]
    history.append({"role": "user", "parts": parts})

    response = requests.post(
        f"{PROXY_URL}/v1beta/models/{MODEL}:generateContent",
        headers={
            "Authorization": f"Bearer {API_KEY}",
            "Content-Type": "application/json"
        },
        json={
            "contents": history,
            "tools": [{"computerUse": {"environment": "ENVIRONMENT_BROWSER"}}]
        }
    )

    result = response.json()
    if "candidates" in result and result["candidates"]:
        model_response = result["candidates"][0]["content"]
        history.append(model_response)
        return model_response
    return {"parts": [{"text": "Error: No response"}]}


def extract_text(response):
    """Extract text content from a model response."""
    if isinstance(response, str):
        return response
    if not isinstance(response, dict):
        return ""
    parts = response.get("parts", [])
    texts = []
    for part in parts:
        if isinstance(part, dict) and part.get("text"):
            texts.append(part["text"])
    return "\n".join(texts).strip()


def coerce_actions(obj):
    """Coerce a parsed JSON object into a list of actions."""
    if isinstance(obj, dict):
        return [obj]
    if isinstance(obj, list):
        return obj
    return None


def parse_actions(response):
    """Parse model response to extract a list of actions."""
    if isinstance(response, list):
        return response

    text = extract_text(response)
    actions = None

    try:
        if text:
            try:
                actions = coerce_actions(json.loads(text))
            except json.JSONDecodeError:
                actions = None

        if actions is None and text:
            for match in re.finditer(r"```(?:json)?\s*(.*?)\s*```", text, re.DOTALL):
                try:
                    actions = coerce_actions(json.loads(match.group(1)))
                    if actions is not None:
                        break
                except json.JSONDecodeError:
                    continue

        if actions is None and text:
            decoder = json.JSONDecoder()
            for index, char in enumerate(text):
                if char in "[{":
                    try:
                        obj, _ = decoder.raw_decode(text[index:])
                    except json.JSONDecodeError:
                        continue
                    actions = coerce_actions(obj)
                    if actions is not None:
                        break

        if actions is None and text:
            point_match = re.search(
                r'"point"\s*:\s*\[\s*([\d.]+)\s*,\s*([\d.]+)\s*\]',
                text,
            )
            if point_match:
                return [{
                    "action": "click",
                    "point": [float(point_match.group(1)), float(point_match.group(2))],
                }]

            coord_match = re.search(
                r'\(x\s*=\s*([\d.]+)\s*,\s*y\s*=\s*([\d.]+)\)',
                text,
            )
            if coord_match:
                return [{
                    "action": "click",
                    "point": [float(coord_match.group(1)), float(coord_match.group(2))],
                }]

    except Exception as e:
        print(f"  Parse error: {e}")

    if actions:
        return actions

    if any(word in text.lower() for word in ["complete", "done", "finished", "found", "here are"]):
        return [{"type": "done", "text": text}]

    return [{"type": "unknown", "text": text[:200]}]


def parse_action(response):
    """Parse model response to extract the first action."""
    actions = parse_actions(response)
    return actions[0] if actions else {"type": "unknown", "text": ""}


def extract_point(action):
    """Extract a point tuple from an action if present."""
    if not isinstance(action, dict):
        return None

    point = action.get("point")
    if isinstance(point, (list, tuple)) and len(point) >= 2:
        return float(point[0]), float(point[1])

    if "x" in action and "y" in action:
        return float(action["x"]), float(action["y"])

    coordinates = action.get("coordinates")
    if isinstance(coordinates, (list, tuple)) and len(coordinates) >= 2:
        return float(coordinates[0]), float(coordinates[1])
    if isinstance(coordinates, dict) and "x" in coordinates and "y" in coordinates:
        return float(coordinates["x"]), float(coordinates["y"])

    return None


def clamp_point(x, y, width, height):
    """Clamp a point into the viewport bounds."""
    clamped_x = max(0, min(width - 1, int(round(x))))
    clamped_y = max(0, min(height - 1, int(round(y))))
    return clamped_x, clamped_y


def normalize_point(point, width, height):
    """Convert normalized (0-1000) coordinates into viewport pixels."""
    raw_x, raw_y = float(point[0]), float(point[1])
    x = (raw_x / NORMALIZED_COORD_MAX) * (width - 1)
    y = (raw_y / NORMALIZED_COORD_MAX) * (height - 1)
    return clamp_point(x, y, width, height)


def coerce_point_to_viewport(point, width, height):
    """Convert a point to viewport coordinates, handling normalized values."""
    raw_x, raw_y = float(point[0]), float(point[1])
    if 0 <= raw_x <= NORMALIZED_COORD_MAX and 0 <= raw_y <= NORMALIZED_COORD_MAX:
        return normalize_point((raw_x, raw_y), width, height)
    return clamp_point(raw_x, raw_y, width, height)


def normalize_key(key):
    """Normalize key names for Playwright."""
    key_str = str(key or "Enter")
    upper = key_str.upper()
    aliases = {
        "ENTER": "Enter",
        "RETURN": "Enter",
        "ESC": "Escape",
        "ESCAPE": "Escape",
        "TAB": "Tab",
        "BACKSPACE": "Backspace",
        "DELETE": "Delete",
        "SPACE": "Space",
        "SPACEBAR": "Space",
    }
    return aliases.get(upper, key_str)


def get_viewport_size(page):
    """Return the current viewport size for coordinate conversion."""
    viewport = page.viewport_size or {}
    width = viewport.get("width", SCREEN_WIDTH)
    height = viewport.get("height", SCREEN_HEIGHT)
    return width, height


def execute_action(page, action):
    """Execute an action on the page."""
    if not isinstance(action, dict):
        print(f"  -> Invalid action type: {type(action)}")
        return True

    action_type = (action.get("action") or action.get("type") or "").lower()

    if action.get("type") == "done" or action.get("done") is True:
        result = action.get("text", "") or action.get("result", "")
        print(f"  -> Done: {result[:200]}")
        return False

    if action_type == "unknown":
        print(f"  -> Unknown action, continuing...")
        return True

    if action_type in ("click", "tap") or (not action_type and extract_point(action)):
        point = extract_point(action)
        if point is None:
            print("  -> Click action missing coordinates.")
            return True
        width, height = get_viewport_size(page)
        x, y = coerce_point_to_viewport(point, width, height)
        print(f"  -> Clicking at ({x}, {y}) [raw: {point[0]}, {point[1]}]")
        page.mouse.click(x, y)
        time.sleep(0.5)
        return True

    if action_type in ("press", "key", "keypress"):
        key = normalize_key(action.get("key") or action.get("text") or "Enter")
        print(f"  -> Pressing: {key}")
        page.keyboard.press(key)
        time.sleep(0.3)
        return True

    if action_type in ("type", "input") or ("text" in action and action_type != "done"):
        text = action.get("text", "")
        if text:
            print(f"  -> Typing: {text}")
            page.keyboard.type(text)
            time.sleep(0.3)
        return True

    return True


def run_agent(task, start_url="https://www.google.com"):
    """Run the computer use agent."""
    print(f"Task: {task}")
    print("-" * 50)

    # More explicit prompt for the model
    system_prompt = f"""You are controlling a web browser. Your task: {task}

IMPORTANT: Respond ONLY with a JSON array of actions. Do NOT explain what you're doing.
Coordinates are normalized from 0-1000 relative to the screenshot.

Example responses:
- To click: [{{"action": "click", "point": [500, 300], "label": "click search box"}}]
- To type: [{{"action": "type", "text": "search query"}}]
- To press a key: [{{"action": "press", "key": "Enter"}}]
- When done: [{{"done": true, "result": "your answer here"}}]

Look at the screenshot and respond with the next action as JSON only."""

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=False)
        page = browser.new_page(viewport={"width": SCREEN_WIDTH, "height": SCREEN_HEIGHT})
        page.goto(start_url)
        time.sleep(1)

        history = []
        current_prompt = system_prompt

        for turn in range(MAX_TURNS):
            print(f"\n--- Turn {turn + 1} ---")

            screenshot = page.screenshot(type="png")
            screenshot_b64 = base64.b64encode(screenshot).decode()

            print("Asking Gemini 2.5 Computer Use...")
            response = send_to_model(screenshot_b64, current_prompt, history)

            actions = parse_actions(response)
            print(f"Actions: {actions}")

            stop = False
            for action in actions:
                if not execute_action(page, action):
                    print("\nTask complete!")
                    stop = True
                    break
            if stop:
                break

            current_prompt = "Continue. Respond with JSON action only."
            time.sleep(1)

        print("\nDone. Browser stays open for 10 seconds...")
        time.sleep(10)
        browser.close()


if __name__ == "__main__":
    if len(sys.argv) < 2:
        task = "Search for 'weather in Seattle' on Google"
    else:
        task = " ".join(sys.argv[1:])

    run_agent(task)
