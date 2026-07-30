"""Dump the FastAPI app's OpenAPI schema to JSON. Run via `make api-types`."""
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
from usage_dashboard.api import app

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "frontend", "openapi.json")


def main():
    schema = app.openapi()
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(schema, f, indent=2, ensure_ascii=False)
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
