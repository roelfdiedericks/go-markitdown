#!/usr/bin/env python3
"""
msft-markitdown-llm.py — invoke Microsoft's markitdown with an OpenAI
LLM image describer, producing a fair side-by-side for our parity suite.

Microsoft's CLI does not expose LLM configuration directly; the feature
is only reachable via the Python API. This thin wrapper takes a file
path on argv and writes the converted markdown to stdout, configuring
the LLM exactly the way their documentation recommends:

    MarkItDown(
        llm_client=openai.OpenAI(),
        llm_model=<model>,
    ).convert(path)

Intended to be invoked by scripts/compare-msft.sh when running with
--llm. Expects to run under the pipx venv that `markitdown` is
installed into (the venv needs `openai` injected via
`pipx inject markitdown openai`); the Makefile target handles both.

Required env:
  OPENAI_API_KEY   — forwarded into the OpenAI client
Optional env:
  OPENAI_MODEL     — default gpt-4o
  OPENAI_BASE_URL  — override for Azure OpenAI / compatible endpoints
  GO_MARKITDOWN_ENV — path to an env file to source before running;
                     default search: $PWD/.env then <script-dir>/../.env
"""

from __future__ import annotations

import os
import sys
from pathlib import Path


def load_env_file() -> None:
    """Honour the same .env convention as examples/describer-openai.sh."""
    if os.environ.get("OPENAI_API_KEY"):
        return
    override = os.environ.get("GO_MARKITDOWN_ENV")
    candidates: list[Path] = []
    if override:
        candidates.append(Path(override))
    candidates.append(Path.cwd() / ".env")
    candidates.append(Path(__file__).resolve().parent.parent / ".env")
    for path in candidates:
        if not path.is_file():
            continue
        for raw in path.read_text().splitlines():
            line = raw.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            k, _, v = line.partition("=")
            k = k.strip()
            v = v.strip().strip('"').strip("'")
            os.environ.setdefault(k, v)
        break


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: msft-markitdown-llm.py <input-file>", file=sys.stderr)
        return 2

    load_env_file()
    if not os.environ.get("OPENAI_API_KEY"):
        print(
            "msft-markitdown-llm: OPENAI_API_KEY not set; put it in .env or the environment",
            file=sys.stderr,
        )
        return 2

    try:
        from markitdown import MarkItDown
        import openai
    except ImportError as exc:
        print(
            f"msft-markitdown-llm: missing dependency ({exc}). "
            "Run: make compare-msft-install && pipx inject markitdown openai",
            file=sys.stderr,
        )
        return 2

    client_kwargs: dict[str, str] = {}
    if base := os.environ.get("OPENAI_BASE_URL"):
        client_kwargs["base_url"] = base

    client = openai.OpenAI(**client_kwargs)
    model = os.environ.get("OPENAI_MODEL", "gpt-4o")

    md = MarkItDown(
        llm_client=client,
        llm_model=model,
    )

    path = sys.argv[1]
    result = md.convert(path)
    sys.stdout.write(result.text_content)
    if not result.text_content.endswith("\n"):
        sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
