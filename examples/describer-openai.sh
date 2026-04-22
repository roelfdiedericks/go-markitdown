#!/usr/bin/env bash
# examples/describer-openai.sh
#
# Describer hook for `go-markitdown --describer ./examples/describer-openai.sh`.
# Reads an image from stdin, calls the OpenAI Chat Completions API with a
# vision-capable model, prints the description to stdout.
#
# Required env: OPENAI_API_KEY
# Optional env: OPENAI_MODEL (default: gpt-4o)
#               OPENAI_MAX_TOKENS (default: 2048)
#               OPENAI_TEMPERATURE (default: 0 — deterministic for parity
#                                   comparison; set to "" to omit the field)
#               OPENAI_BASE_URL (default: https://api.openai.com/v1 — override
#                                to point at an OpenAI-compatible endpoint
#                                such as Azure OpenAI or a local vLLM)
#               GO_MARKITDOWN_ENV (path to an env file to source; defaults
#                                  search: $PWD/.env then <script-dir>/../.env)
# Injected by go-markitdown: GO_MARKITDOWN_PROMPT, GO_MARKITDOWN_MIME
#
# Dependencies: bash curl jq base64
#
# Quick start:
#   echo "OPENAI_API_KEY=sk-..." > .env
#   go-markitdown convert --include-images \
#       --describer ./examples/describer-openai.sh document.docx

set -euo pipefail

# Source an .env file if one exists and OPENAI_API_KEY is not already set.
# Precedence: GO_MARKITDOWN_ENV override > $PWD/.env > <script-dir>/../.env.
if [ -z "${OPENAI_API_KEY:-}" ]; then
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

KEY="${OPENAI_API_KEY:-}"
if [ -z "$KEY" ]; then
    echo "describer-openai: OPENAI_API_KEY not set" >&2
    echo "describer-openai: set it in the environment, or put it in a .env file" >&2
    echo "describer-openai:   echo 'OPENAI_API_KEY=sk-...' > .env" >&2
    exit 2
fi

for tool in curl jq base64; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "describer-openai: missing dependency: $tool" >&2
        exit 2
    fi
done

MODEL="${OPENAI_MODEL:-gpt-4o}"
MAX_TOKENS="${OPENAI_MAX_TOKENS:-2048}"
TEMPERATURE="${OPENAI_TEMPERATURE-0}"
BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
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

# OpenAI's vision API consumes images as data URLs embedded in a
# multi-part message.content array. The `temperature` field is optional;
# we include it only when the caller left the default (0) or set an
# explicit value, so callers who want the model default can set
# OPENAI_TEMPERATURE="" to drop the field entirely.
BODY_FILE="${TMPDIR_RUN}/body.json"
if [ -n "${TEMPERATURE}" ]; then
    jq -n \
        --arg model "$MODEL" \
        --argjson max_tokens "$MAX_TOKENS" \
        --argjson temperature "$TEMPERATURE" \
        --arg prompt "$PROMPT" \
        --arg mime "$MIME" \
        --rawfile b64 "$B64_FILE" \
        '{
            model: $model,
            max_tokens: $max_tokens,
            temperature: $temperature,
            messages: [{
                role: "user",
                content: [
                    { type: "text",      text: $prompt },
                    { type: "image_url", image_url: { url: ("data:" + $mime + ";base64," + $b64) } }
                ]
            }]
        }' >"$BODY_FILE"
else
    jq -n \
        --arg model "$MODEL" \
        --argjson max_tokens "$MAX_TOKENS" \
        --arg prompt "$PROMPT" \
        --arg mime "$MIME" \
        --rawfile b64 "$B64_FILE" \
        '{
            model: $model,
            max_tokens: $max_tokens,
            messages: [{
                role: "user",
                content: [
                    { type: "text",      text: $prompt },
                    { type: "image_url", image_url: { url: ("data:" + $mime + ";base64," + $b64) } }
                ]
            }]
        }' >"$BODY_FILE"
fi

RESP="$(curl -sS "${BASE_URL%/}/chat/completions" \
    -H "Authorization: Bearer $KEY" \
    -H "content-type: application/json" \
    --data-binary "@${BODY_FILE}")"

# Surface structured API errors to stderr and exit non-zero; the go-markitdown
# CLI will note the failure and fall back to placeholder alt-text.
if ERR_MSG="$(jq -er '.error.message // empty' <<<"$RESP")"; then
    echo "describer-openai: API error: $ERR_MSG" >&2
    exit 1
fi

jq -r '.choices[0].message.content // ""' <<<"$RESP"
