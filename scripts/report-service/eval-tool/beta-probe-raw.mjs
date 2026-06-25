#!/usr/bin/env node
// Diagnostic: dump the raw `context_management` field + the full response keys
// so we can see whether the gateway is synthesizing an empty echo regardless
// of what we send.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const envFile = (() => {
  const text = readFileSync(join(__dirname, ".env"), "utf8");
  const e = {};
  for (const line of text.split("\n")) {
    const m = line.match(/^([A-Z_]+)\s*=\s*(.*)$/);
    if (m) e[m[1]] = m[2].replace(/^["']|["']$/g, "").trim();
  }
  return e;
})();

const BASE = envFile.URL.replace(/\/$/, "");
const KEY = envFile.KEY;
const MODEL = process.env.MODEL_OVERRIDE || envFile.MODEL || "claude-sonnet-4-6";

async function dump(label, body, betaHeader) {
  const headers = {
    "content-type": "application/json",
    "x-api-key": KEY,
    "anthropic-version": "2023-06-01",
  };
  if (betaHeader) headers["anthropic-beta"] = betaHeader;
  const r = await fetch(`${BASE}/v1/messages`, {
    method: "POST", headers, body: JSON.stringify(body),
  });
  const text = await r.text();
  let json; try { json = JSON.parse(text); } catch {}
  console.log(`\n=== ${label} (status ${r.status}) ===`);
  if (!json) { console.log(text.slice(0, 500)); return; }
  console.log("top-level keys:", Object.keys(json));
  console.log("context_management:", JSON.stringify(json.context_management, null, 2));
  console.log("usage:", JSON.stringify(json.usage, null, 2));
  console.log("stop_reason:", json.stop_reason, " id:", json.id?.slice(0, 40));
}

// Bare minimum request: NO ctx_mgmt, NO beta, NO tools.
await dump("Plain hello, no beta, no ctx_mgmt", {
  model: MODEL,
  max_tokens: 8,
  messages: [{ role: "user", content: "hi" }],
}, null);

// Same but explicit beta header
await dump("Plain hello + beta header only", {
  model: MODEL,
  max_tokens: 8,
  messages: [{ role: "user", content: "hi" }],
}, "context-management-2025-06-27");

// Direct probe to Anthropic to compare (skip if no env)
const ANTH_KEY = process.env.ANTHROPIC_API_KEY;
if (ANTH_KEY) {
  const headers = {
    "content-type": "application/json",
    "x-api-key": ANTH_KEY,
    "anthropic-version": "2023-06-01",
    "anthropic-beta": "context-management-2025-06-27",
  };
  const r = await fetch("https://api.anthropic.com/v1/messages", {
    method: "POST", headers,
    body: JSON.stringify({
      model: MODEL,
      max_tokens: 8,
      messages: [{ role: "user", content: "hi" }],
    }),
  });
  const text = await r.text();
  let json; try { json = JSON.parse(text); } catch {}
  console.log(`\n=== DIRECT Anthropic (status ${r.status}) ===`);
  console.log("top-level keys:", Object.keys(json || {}));
  console.log("context_management:", JSON.stringify(json?.context_management, null, 2));
}
