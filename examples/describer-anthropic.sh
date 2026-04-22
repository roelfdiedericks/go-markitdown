#!/usr/bin/env bash
# examples/describer-anthropic.sh
#
# Describer hook for `go-markitdown --describer ./examples/describer-anthropic.sh`.
# Reads an image from stdin, calls the Anthropic Messages API, prints the
# description to stdout.
#
# Required env: ANTHROPIC_KEY or ANTHROPIC_API_KEY
# Optional env: ANTHROPIC_MODEL (default: claude-opus-4-5)
#               ANTHROPIC_MAX_TOKENS (default: 2048)
#               ANTHROPIC_VERSION (default: 2023-06-01)
#               GO_MARKITDOWN_ENV (path to an env file to source; defaults
#                                  search: $PWD/.env then <script-dir>/../.env)
# Injected by go-markitdown: GO_MARKITDOWN_PROMPT, GO_MARKITDOWN_MIME
#
# Dependencies: bash curl jq base64
#
# Quick start:
#   echo "ANTHROPIC_KEY=sk-ant-..." > .env
#   go-markitdown convert --include-images \
#       --describer ./examples/describer-anthropic.sh document.docx

set -euo pipefail

# Source an .env file if one exists and ANTHROPIC_KEY is not already set.
# Precedence: GO_MARKITDOWN_ENV override > $PWD/.env > <script-dir>/../.env.
if [ -z "${ANTHROPIC_KEY:-}" ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    ENV_FILE=""
    if [ -n "${GO_MARKITDOWN_ENV:-}" ] && [ -f "${GO_MARKITDOWN_ENV}" ]; then
        ENV_FILE="${GO_MARKITDOWN_ENV}"
    elif [ -f "${PWD}/.env" ]; then
        ENV_FILE="${PWD}/.env"
    elif [ -f "${SCRIPT_DIR}/../.env" ]; then
        ENV_FILE="${SCRIPT_DIR}/../.env"
    fi
    if [ -n "${ENV_FILE}" ]; then
        set -a
        # shellcheck disable=SC1090
        . "${ENV_FILE}"
        set +a
    fi
fi

KEY="${ANTHROPIC_KEY:-${ANTHROPIC_API_KEY:-}}"
if [ -z "$KEY" ]; then
    echo "describer-anthropic: ANTHROPIC_KEY (or ANTHROPIC_API_KEY) not set" >&2
    echo "describer-anthropic: set it in the environment, or put it in a .env file" >&2
    echo "describer-anthropic:   echo 'ANTHROPIC_KEY=sk-ant-...' > .env" >&2
    exit 2
fi

for tool in curl jq base64; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "describer-anthropic: missing dependency: $tool" >&2
        exit 2
    fi
done

MODEL="${ANTHROPIC_MODEL:-claude-opus-4-5}"
MAX_TOKENS="${ANTHROPIC_MAX_TOKENS:-2048}"
VERSION_HDR="${ANTHROPIC_VERSION:-2023-06-01}"
PROMPT="${GO_MARKITDOWN_PROMPT:-Describe this image.}"
MIME="${GO_MARKITDOWN_MIME:-image/png}"

# Base64-encode the stdin image into a temp file. We route the base64
# payload through a file (and use jq --rawfile) rather than $(...) + jq
# --arg to avoid ARG_MAX limits on larger images — a ~500KB JPEG trips
# "Argument list too long" on Linux when passed as a shell argument.
# GNU coreutils base64 uses -w; macOS / BSD base64 takes no flag and
# already emits a single line. Try -w0 first, fall back otherwise.
TMPDIR_RUN="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_RUN"' EXIT
B64_FILE="${TMPDIR_RUN}/image.b64"
if ! base64 -w0 >"$B64_FILE" 2>/dev/null; then
    base64 | tr -d '\n' >"$B64_FILE"
fi

BODY_FILE="${TMPDIR_RUN}/body.json"
jq -n \
    --arg model "$MODEL" \
    --argjson max_tokens "$MAX_TOKENS" \
    --arg prompt "$PROMPT" \
    --arg mime "$MIME" \
    --rawfile data "$B64_FILE" \
    '{
        model: $model,
        max_tokens: $max_tokens,
        messages: [{
            role: "user",
            content: [
                { type: "image", source: { type: "base64", media_type: $mime, data: $data } },
                { type: "text",  text: $prompt }
            ]
        }]
    }' >"$BODY_FILE"

RESP="$(curl -sS https://api.anthropic.com/v1/messages \
    -H "x-api-key: $KEY" \
    -H "anthropic-version: $VERSION_HDR" \
    -H "content-type: application/json" \
    --data-binary "@${BODY_FILE}")"

# Surface structured API errors to stderr and exit non-zero; the go-markitdown
# CLI will note the failure and fall back to placeholder alt-text.
if ERR_MSG="$(jq -er 'select(.type == "error") | .error.message' <<<"$RESP" 2>/dev/null)"; then
    echo "describer-anthropic: API error: $ERR_MSG" >&2
    exit 1
fi

jq -r '.content[0].text // ""' <<<"$RESP"
