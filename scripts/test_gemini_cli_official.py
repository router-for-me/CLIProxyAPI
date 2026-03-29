#!/usr/bin/env python3
import argparse
import json
import platform
import sys
import time
import uuid
from datetime import datetime, timedelta, timezone
from email.utils import parsedate_to_datetime
from pathlib import Path
from urllib import error, parse, request


CODE_ASSIST_ENDPOINT = "https://cloudcode-pa.googleapis.com"
CODE_ASSIST_VERSION = "v1internal"
DEFAULT_MODEL = "gemini-3-flash-preview"
DEFAULT_PROMPT = "Reply with exactly: ok"
DEFAULT_TIMEOUT = 60
DEFAULT_RETRY_DELAY_SECONDS = 1.0
DEFAULT_RETRY_COUNT = 3
DEFAULT_SURFACE = "terminal"
OFFICIAL_GEMINI_CLI_VERSION = "0.36.0-nightly.20260317.2f90b4653"
REFRESH_SKEW_SECONDS = 300
RETRY_STATUS_CODES = {429, 499}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Call the official Gemini CLI Code Assist upstream with one Gemini auth file."
    )
    parser.add_argument("--auth-file", help="Path to Gemini auth JSON file.")
    parser.add_argument(
        "--auth-json",
        help="Raw Gemini auth JSON string. If omitted, --auth-file is required.",
    )
    parser.add_argument("--model", default=DEFAULT_MODEL)
    parser.add_argument("--prompt", default=DEFAULT_PROMPT)
    parser.add_argument(
        "--timeout",
        type=int,
        default=DEFAULT_TIMEOUT,
        help=f"HTTP timeout in seconds. Default: {DEFAULT_TIMEOUT}.",
    )
    parser.add_argument(
        "--retry-count",
        type=int,
        default=DEFAULT_RETRY_COUNT,
        help=f"Retry count for non-stream generateContent. Default: {DEFAULT_RETRY_COUNT}.",
    )
    parser.add_argument(
        "--retry-delay",
        type=float,
        default=DEFAULT_RETRY_DELAY_SECONDS,
        help=f"Retry delay in seconds. Default: {DEFAULT_RETRY_DELAY_SECONDS}.",
    )
    parser.add_argument(
        "--proxy-url",
        help="Optional HTTP proxy URL, e.g. http://127.0.0.1:7897",
    )
    parser.add_argument(
        "--save-refreshed-auth",
        action="store_true",
        help="Write refreshed token fields back to --auth-file when refresh succeeds.",
    )
    parser.add_argument(
        "--stream",
        action="store_true",
        help="Use streamGenerateContent?alt=sse instead of generateContent.",
    )
    parser.add_argument(
        "--surface",
        default=DEFAULT_SURFACE,
        help=f"Gemini CLI surface for User-Agent. Default: {DEFAULT_SURFACE}.",
    )
    parser.add_argument(
        "--client-name",
        default="",
        help="Optional Gemini CLI client name prefix, e.g. acp-vscode.",
    )
    parser.add_argument(
        "--show-request",
        action="store_true",
        help="Print the final upstream request headers and JSON body.",
    )
    return parser.parse_args()


def load_auth(args: argparse.Namespace) -> tuple[dict, Path | None]:
    auth_path = Path(args.auth_file).expanduser() if args.auth_file else None
    if args.auth_json:
        return json.loads(args.auth_json), auth_path
    if auth_path:
        return json.loads(auth_path.read_text(encoding="utf-8-sig")), auth_path
    raise SystemExit("Provide --auth-file or --auth-json.")


def parse_expiry(value: str) -> datetime | None:
    raw = (value or "").strip()
    if not raw:
        return None
    try:
        return datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        pass
    try:
        return parsedate_to_datetime(raw)
    except (TypeError, ValueError):
        return None


def extract_token_container(auth: dict) -> dict:
    token = auth.get("token")
    if isinstance(token, dict):
        return token
    return auth


def token_needs_refresh(auth: dict) -> bool:
    token = extract_token_container(auth)
    expiry = parse_expiry(str(token.get("expiry", "") or auth.get("expiry", "")))
    access_token = str(token.get("access_token", "") or auth.get("access_token", "")).strip()
    if expiry is None:
        return not bool(access_token)
    now = datetime.now(timezone.utc)
    return expiry.astimezone(timezone.utc) <= now + timedelta(seconds=REFRESH_SKEW_SECONDS)


def build_opener(proxy_url: str | None):
    handlers = []
    if proxy_url:
        handlers.append(request.ProxyHandler({"http": proxy_url, "https": proxy_url}))
    else:
        handlers.append(request.ProxyHandler({}))
    return request.build_opener(*handlers)


def refresh_access_token(auth: dict, timeout: int, opener) -> dict:
    token = extract_token_container(auth)
    refresh_token = str(token.get("refresh_token", "") or auth.get("refresh_token", "")).strip()
    token_uri = str(token.get("token_uri", "")).strip() or "https://oauth2.googleapis.com/token"
    client_id = str(token.get("client_id", "")).strip()
    client_secret = str(token.get("client_secret", "")).strip()

    if not refresh_token:
        raise RuntimeError("Auth file does not contain refresh_token.")
    if not client_id or not client_secret:
        raise RuntimeError("Auth file does not contain client_id/client_secret.")

    form = parse.urlencode(
        {
            "client_id": client_id,
            "client_secret": client_secret,
            "grant_type": "refresh_token",
            "refresh_token": refresh_token,
        }
    ).encode("utf-8")
    req = request.Request(
        token_uri,
        data=form,
        method="POST",
        headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "User-Agent": "google-auth-library-python/refresh",
        },
    )
    with opener.open(req, timeout=timeout) as resp:
        body = resp.read()
    token_resp = json.loads(body)

    now = datetime.now(timezone.utc)
    expires_in = int(token_resp.get("expires_in", token.get("expires_in", 0)) or 0)
    access_token = token_resp.get("access_token", token.get("access_token", ""))
    refresh_token_new = token_resp.get("refresh_token", refresh_token)
    expiry_dt = now + timedelta(seconds=expires_in)
    expiry_str = expiry_dt.isoformat().replace("+00:00", "Z")
    expiry_ms = int(expiry_dt.timestamp() * 1000)

    token["access_token"] = access_token
    token["refresh_token"] = refresh_token_new
    token["expires_in"] = expires_in
    token["expiry"] = expiry_str
    token["expiry_date"] = expiry_ms

    auth["access_token"] = access_token
    auth["refresh_token"] = refresh_token_new
    auth["token_type"] = token.get("token_type", auth.get("token_type", "Bearer"))
    auth["expiry"] = expiry_str
    auth["token"] = token
    return auth


def maybe_write_auth(auth: dict, auth_path: Path | None) -> None:
    if auth_path is None:
        return
    auth_path.write_text(
        json.dumps(auth, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )


def node_platform() -> str:
    if sys.platform.startswith("win"):
        return "win32"
    if sys.platform == "darwin":
        return "darwin"
    return "linux"


def node_arch() -> str:
    machine = platform.machine().lower()
    if machine in {"amd64", "x86_64"}:
        return "x64"
    if machine in {"aarch64", "arm64"}:
        return "arm64"
    if machine in {"x86", "i386", "i686"}:
        return "ia32"
    return machine or "unknown"


def build_user_agent(model: str, surface: str, client_name: str) -> str:
    prefix = f"GeminiCLI-{client_name}" if client_name else "GeminiCLI"
    return (
        f"{prefix}/{OFFICIAL_GEMINI_CLI_VERSION}/{model} "
        f"({node_platform()}; {node_arch()}; {surface or DEFAULT_SURFACE})"
    )


def build_request_payload(auth: dict, model: str, prompt: str) -> dict:
    project_id = str(auth.get("project_id", "")).strip()
    if not project_id:
        raise RuntimeError("Auth file does not contain project_id.")

    user_prompt_id = str(uuid.uuid4())
    session_id = str(uuid.uuid4())

    payload = {
        "model": model,
        "project": project_id,
        "user_prompt_id": user_prompt_id,
        "request": {
            "contents": [
                {
                    "role": "user",
                    "parts": [{"text": prompt}],
                }
            ],
            "generationConfig": {},
            "session_id": session_id,
        },
    }
    return payload


def build_request_url(stream: bool) -> str:
    action = "streamGenerateContent" if stream else "generateContent"
    url = f"{CODE_ASSIST_ENDPOINT}/{CODE_ASSIST_VERSION}:{action}"
    if stream:
        url += "?alt=sse"
    return url


def redact_headers(headers: dict[str, str]) -> dict[str, str]:
    out = dict(headers)
    authz = out.get("Authorization")
    if authz:
        out["Authorization"] = "Bearer <redacted>"
    return out


def should_retry(status: int) -> bool:
    return status in RETRY_STATUS_CODES or 500 <= status <= 599


def do_request(
    opener,
    *,
    url: str,
    payload: dict,
    access_token: str,
    timeout: int,
    user_agent: str,
    stream: bool,
    retry_count: int,
    retry_delay: float,
) -> tuple[int, dict, bytes]:
    data = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {access_token}",
        "User-Agent": user_agent,
    }

    attempts = 1 if stream else max(1, retry_count + 1)
    last_error = None
    for attempt in range(1, attempts + 1):
        req = request.Request(
            url,
            data=data,
            method="POST",
            headers=headers,
        )
        try:
            with opener.open(req, timeout=timeout) as resp:
                return resp.getcode(), dict(resp.headers.items()), resp.read()
        except error.HTTPError as exc:
            body = exc.read()
            status = exc.code
            if attempt < attempts and should_retry(status):
                time.sleep(retry_delay)
                continue
            return status, dict(exc.headers.items()), body
        except Exception as exc:  # noqa: BLE001
            last_error = exc
            if attempt < attempts:
                time.sleep(retry_delay)
                continue
            raise RuntimeError(f"request failed after {attempts} attempt(s): {exc}") from exc

    raise RuntimeError(f"request failed: {last_error}")


def print_response(status: int, headers: dict, body: bytes, stream: bool) -> None:
    print(f"status: {status}")
    print("headers:")
    print(json.dumps(headers, ensure_ascii=False, indent=2))
    print("body:")
    if stream:
        print(body.decode("utf-8", errors="replace"))
        return
    try:
        decoded = json.loads(body)
        print(json.dumps(decoded, ensure_ascii=False, indent=2))
    except json.JSONDecodeError:
        print(body.decode("utf-8", errors="replace"))


def main() -> int:
    args = parse_args()
    auth, auth_path = load_auth(args)

    auth_type = str(auth.get("type", "")).strip()
    if auth_type and auth_type != "gemini":
        raise SystemExit(f"Auth type is {auth_type!r}, expected 'gemini'.")

    opener = build_opener(args.proxy_url)

    refreshed = False
    if token_needs_refresh(auth):
        auth = refresh_access_token(auth, args.timeout, opener)
        refreshed = True
        if args.save_refreshed_auth:
            maybe_write_auth(auth, auth_path)

    token = extract_token_container(auth)
    access_token = str(token.get("access_token", "") or auth.get("access_token", "")).strip()
    if not access_token:
        raise SystemExit("Auth file does not contain access_token after refresh.")

    payload = build_request_payload(auth, args.model, args.prompt)
    url = build_request_url(args.stream)
    user_agent = build_user_agent(args.model, args.surface, args.client_name)
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {access_token}",
        "User-Agent": user_agent,
    }

    print(
        json.dumps(
            {
                "refreshed": refreshed,
                "email": auth.get("email"),
                "project_id": auth.get("project_id"),
                "model": args.model,
                "stream": args.stream,
                "url": url,
                "proxy_url": args.proxy_url or "",
                "user_agent": user_agent,
            },
            ensure_ascii=False,
        )
    )

    if args.show_request:
        print("\nrequest_headers:")
        print(json.dumps(redact_headers(headers), ensure_ascii=False, indent=2))
        print("request_body:")
        print(json.dumps(payload, ensure_ascii=False, indent=2))

    status, resp_headers, body = do_request(
        opener,
        url=url,
        payload=payload,
        access_token=access_token,
        timeout=args.timeout,
        user_agent=user_agent,
        stream=args.stream,
        retry_count=args.retry_count,
        retry_delay=args.retry_delay,
    )
    print()
    print_response(status, resp_headers, body, args.stream)
    return 0


if __name__ == "__main__":
    sys.exit(main())
