"""Point Grok CLI and Kimi Code CLI at the estate hub (m6). Idempotent.

Key source: ~/.claude/settings.json env.ANTHROPIC_AUTH_TOKEN (the hub access key, already on every
device). Never prints the key.
"""
import json, os, sys

HOME = os.path.expanduser("~")
URL = "https://waseems-mac-mini-m6.taild6c90e.ts.net:8317/v1"
key = json.load(open(os.path.join(HOME, ".claude/settings.json")))["env"]["ANTHROPIC_AUTH_TOKEN"]
assert key, "no hub key in Claude settings"

GROK = [("hub-grok-build", "grok-build-0.1", "Grok Build 0.1 (hub)"),
        ("hub-grok-4-7", "grok-4.7", "Grok 4.7 (hub)")]
KIMI = [("hub/kimi-k3", "kimi-k3", "Kimi K3 (hub)"),
        ("hub/kimi-k3.1-pro", "kimi-k3.1-pro", "Kimi K3.1 Pro (hub)")]


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(text)
    os.chmod(path, 0o600)


def grok():
    path = os.path.join(HOME, ".grok/config.toml")
    text = open(path).read() if os.path.exists(path) else ""
    if "[model.hub-grok-build]" in text:
        return "grok: already configured"
    if "\n[models]" in "\n" + text:
        sys.exit("grok: [models] table exists; merge by hand")
    # A signed-in Grok keeps its own default; the hub models are added beside it.
    signed_in = os.path.exists(os.path.join(HOME, ".grok/auth.json"))
    block = "" if signed_in else '\n[models]\ndefault = "hub-grok-build"\n'
    for alias, model, name in GROK:
        block += (f'\n[model.{alias}]\nmodel = "{model}"\nbase_url = "{URL}"\nname = "{name}"\n'
                  f'api_key = "{key}"\ncontext_window = 256000\n')
    write(path, text.rstrip("\n") + "\n" + block)
    return "grok: configured"


def kimi():
    path = os.path.join(HOME, ".kimi-code/config.toml")
    text = open(path).read() if os.path.exists(path) else ""
    if "[providers.hub]" in text:
        return "kimi: already configured"
    lines = text.splitlines()
    # An existing default_model (a signed-in Kimi) is kept; it is a top-level key, so it goes
    # before the first table.
    has_default = any(l.startswith("default_model") for l in lines)
    head = "" if has_default else 'default_model = "hub/kimi-k3"\n'
    tables = f'\n[providers.hub]\ntype = "openai"\nbase_url = "{URL}"\napi_key = "{key}"\n'
    for alias, model, name in KIMI:
        tables += (f'\n[models."{alias}"]\nprovider = "hub"\nmodel = "{model}"\n'
                   f'display_name = "{name}"\nmax_context_size = 256000\n'
                   'capabilities = ["thinking", "tool_use"]\n')
    first_table = next((i for i, l in enumerate(lines) if l.startswith("[")), len(lines))
    out = "\n".join(lines[:first_table]).rstrip("\n")
    out = (out + "\n" if out else "") + head + "\n".join(lines[first_table:]) + tables
    write(path, out)
    return "kimi: configured"


print(grok())
print(kimi())
