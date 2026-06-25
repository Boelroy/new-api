#!/usr/bin/env bash
# Probe: does $MODEL support structured output on this endpoint?
# Reads URL / KEY / MODEL from the .env next to this script.
#
# Usage:
#   tools/provider-eval/structured-output.sh
#   MODEL_OVERRIDE=anthropic-z/claude-opus-4-8 tools/provider-eval/structured-output.sh
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
set -a; source "$DIR/.env"; set +a
MODEL="${MODEL_OVERRIDE:-$MODEL}"
BASE="${URL%/}"                 # strip trailing slash; .env URL already ends in /v1

echo "BASE=$BASE  MODEL=$MODEL"
echo

# JSON schema the model must conform to.
SCHEMA='{"type":"object","properties":{"city":{"type":"string"},"population":{"type":"integer"}},"required":["city","population"],"additionalProperties":false}'

# --- 1) Native structured output via output_config.format ----------------------
echo "== output_config.format (json_schema) =="
curl -sS "$BASE/messages" \
  -H "content-type: application/json" \
  -H "x-api-key: $KEY" \
  -H "authorization: Bearer $KEY" \
  -H "anthropic-version: 2023-06-01" \
  -d "{
        \"model\": \"$MODEL\",
        \"max_tokens\": 256,
        \"output_config\": { \"format\": { \"type\": \"json_schema\", \"schema\": $SCHEMA } },
        \"messages\": [{ \"role\": \"user\", \"content\": \"What is Tokyo's population? Answer per schema.\" }]
      }"
echo; echo

# --- 2) Forced tool_use (portable fallback) ------------------------------------
echo "== forced tool_use =="
curl -sS "$BASE/messages" \
  -H "content-type: application/json" \
  -H "x-api-key: $KEY" \
  -H "authorization: Bearer $KEY" \
  -H "anthropic-version: 2023-06-01" \
  -d "{
        \"model\": \"$MODEL\",
        \"max_tokens\": 256,
        \"tools\": [{ \"name\": \"record_city\", \"description\": \"Record city data\", \"input_schema\": $SCHEMA }],
        \"tool_choice\": { \"type\": \"tool\", \"name\": \"record_city\" },
        \"messages\": [{ \"role\": \"user\", \"content\": \"What is Tokyo's population?\" }]
      }"
echo
