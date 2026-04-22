#!/usr/bin/env bash
#
# compare-msft.sh - parity check against Microsoft's Python markitdown.
#
# Runs our CLI and the Microsoft markitdown CLI over the same testdata
# fixtures and writes side-by-side outputs plus unified diffs under
# samples/. Intended as a dev tool: not wired into `go test`.
#
# Modes:
#   (default)   structural-only: neither tool is asked to describe images.
#               Fast, deterministic, no network or API keys needed.
#
#   --llm       describer parity: both tools describe images via the same
#               OpenAI model. Ours runs with --describer
#               examples/describer-openai.sh; theirs runs via the Python
#               wrapper scripts/msft-markitdown-llm.py executed under the
#               pipx venv that has `openai` injected. Requires
#               OPENAI_API_KEY (picked up via .env too).
#
# Prerequisites:
#   - `markitdown` on PATH. Install with `pipx install 'markitdown[all]'`
#     or run `make compare-msft-install`.
#   - Our CLI built at ./go-markitdown (run `make build` first).
#   - For --llm mode: `pipx inject markitdown openai` (the Makefile
#     target compare-msft-llm handles this for you).

set -euo pipefail

cd "$(dirname "$0")/.."

BINARY="./go-markitdown"
SAMPLES_DIR="samples"
OURS_DIR="$SAMPLES_DIR/ours"
MSFT_DIR="$SAMPLES_DIR/msft"
COMPARE_DIR="$SAMPLES_DIR/compare"
SUMMARY="$COMPARE_DIR/summary.md"

LLM_MODE=0
for arg in "$@"; do
    case "$arg" in
        --llm) LLM_MODE=1 ;;
        *)
            echo "unknown arg: $arg" >&2
            exit 2
            ;;
    esac
done

if ! command -v markitdown >/dev/null 2>&1; then
    cat >&2 <<EOF
ERROR: 'markitdown' not found on PATH.

Install it with:
    pipx install 'markitdown[all]'

or run:
    make compare-msft-install
EOF
    exit 1
fi

if [[ ! -x "$BINARY" ]]; then
    echo "ERROR: $BINARY not built. Run 'make build' first." >&2
    exit 1
fi

# In --llm mode we need the pipx venv's python (with openai injected).
MSFT_VENV_PY="${HOME}/.local/share/pipx/venvs/markitdown/bin/python"
if [[ "$LLM_MODE" -eq 1 ]]; then
    if [[ ! -x "$MSFT_VENV_PY" ]]; then
        echo "ERROR: markitdown pipx venv python not found at $MSFT_VENV_PY" >&2
        echo "Did you run 'make compare-msft-install'?" >&2
        exit 1
    fi
    if ! "$MSFT_VENV_PY" -c "import openai" 2>/dev/null; then
        echo "ERROR: 'openai' not injected into markitdown venv." >&2
        echo "Run: pipx inject markitdown openai" >&2
        exit 1
    fi
fi

mkdir -p "$OURS_DIR" "$MSFT_DIR" "$COMPARE_DIR"

# Parity fixtures: formats where both tools do real structural work.
# Entries are "format:fixture-path" — format is the stem used in the
# summary and informs file extension detection for xlsx/docx/etc. Each
# PPTX fixture is named separately so that test.pptx and complex.pptx
# both surface rather than one overwriting the other.
FIXTURES_PRIMARY=(
    "docx:docconv/testdata/test.docx"
    "pptx:docconv/testdata/test.pptx"
    "pptx-complex:docconv/testdata/complex.pptx"
    "xlsx:docconv/testdata/test.xlsx"
    "html:docconv/testdata/test.html"
)
FIXTURES_INFO=(
    "pdf:docconv/testdata/test.pdf"
    "pdf-cpq:docconv/testdata/CPQ.pdf"
)

run_pair() {
    local fixture="$1"
    local label="$2"

    local ours="$OURS_DIR/$label.md"
    local msft="$MSFT_DIR/$label.md"
    local diff="$COMPARE_DIR/$label.diff"

    local -a our_args=(convert "$fixture")
    if [[ "$LLM_MODE" -eq 1 ]]; then
        our_args=(convert --include-images --describer ./examples/describer-openai.sh "$fixture")
    fi
    "$BINARY" "${our_args[@]}" > "$ours" 2>/dev/null || {
        echo "  (our CLI failed on $label)"
        return 0
    }

    if [[ "$LLM_MODE" -eq 1 ]]; then
        "$MSFT_VENV_PY" scripts/msft-markitdown-llm.py "$fixture" > "$msft" 2>/dev/null || {
            echo "  (Microsoft markitdown-llm failed on $label)"
            return 0
        }
    else
        markitdown "$fixture" > "$msft" 2>/dev/null || {
            echo "  (Microsoft markitdown failed on $label)"
            return 0
        }
    fi

    diff -u "$msft" "$ours" > "$diff" 2>/dev/null || true
}

count_tables() {
    # Pipe-table rows as a rough proxy. Both tools emit GFM-ish tables so
    # the count is consistent when both produce structured output.
    local file="$1"
    if [[ ! -f "$file" ]]; then
        echo 0
        return
    fi
    local n
    n=$(grep -c '^[[:space:]]*|.*|.*|' "$file" 2>/dev/null || true)
    echo "${n:-0}"
}

count_images() {
    # Count markdown image tokens — ![alt](src) — as a robust proxy for
    # "images that survived extraction and description". Matches both
    # the plain and described forms.
    local file="$1"
    if [[ ! -f "$file" ]]; then
        echo 0
        return
    fi
    local n
    n=$(grep -oE '!\[[^]]*\]\([^)]+\)' "$file" 2>/dev/null | wc -l || true)
    echo "${n:-0}"
}

lines_of() {
    local file="$1"
    if [[ ! -f "$file" ]]; then
        echo 0
        return
    fi
    local n
    n=$(wc -l < "$file" 2>/dev/null || true)
    echo "${n:-0}"
}

echo "# Parity summary: go-markitdown vs Microsoft markitdown" > "$SUMMARY"
echo "" >> "$SUMMARY"
echo "Run at: $(date -u '+%Y-%m-%d %H:%M:%S UTC')" >> "$SUMMARY"
if [[ "$LLM_MODE" -eq 1 ]]; then
    echo "" >> "$SUMMARY"
    echo "Mode: **--llm** (both sides describe images via OpenAI \`${OPENAI_MODEL:-gpt-4o}\`)" >> "$SUMMARY"
fi
echo "" >> "$SUMMARY"

process() {
    local entry="$1"
    local label="${entry%%:*}"
    local fixture="${entry#*:}"

    if [[ ! -f "$fixture" ]]; then
        echo "skipping $label (no fixture at $fixture)"
        return
    fi

    echo "==> $label"
    run_pair "$fixture" "$label"

    local ours_lines msft_lines diff_lines ours_tables msft_tables ours_images msft_images
    ours_lines=$(lines_of "$OURS_DIR/$label.md")
    msft_lines=$(lines_of "$MSFT_DIR/$label.md")
    diff_lines=$(lines_of "$COMPARE_DIR/$label.diff")
    ours_tables=$(count_tables "$OURS_DIR/$label.md")
    msft_tables=$(count_tables "$MSFT_DIR/$label.md")
    ours_images=$(count_images "$OURS_DIR/$label.md")
    msft_images=$(count_images "$MSFT_DIR/$label.md")

    printf "| %s | %d | %d | %d | %d | %d | %d | %d |\n" \
        "$label" "$ours_lines" "$msft_lines" "$diff_lines" \
        "$ours_tables" "$msft_tables" \
        "$ours_images" "$msft_images" \
        >> "$SUMMARY"
}

emit_table_header() {
    echo "| fixture | ours lines | msft lines | diff lines | ours tables | msft tables | ours images | msft images |" >> "$SUMMARY"
    echo "|---------|-----------:|-----------:|-----------:|------------:|------------:|------------:|------------:|" >> "$SUMMARY"
}

echo "## Primary parity targets" >> "$SUMMARY"
echo "" >> "$SUMMARY"
emit_table_header
for f in "${FIXTURES_PRIMARY[@]}"; do
    process "$f"
done
echo "" >> "$SUMMARY"

echo "## Informational (backends differ fundamentally)" >> "$SUMMARY"
echo "" >> "$SUMMARY"
emit_table_header
for f in "${FIXTURES_INFO[@]}"; do
    process "$f"
done

echo "" >> "$SUMMARY"
echo "Outputs in \`$OURS_DIR/\` and \`$MSFT_DIR/\`; unified diffs in \`$COMPARE_DIR/\`." >> "$SUMMARY"

echo ""
echo "Wrote parity summary to $SUMMARY"
