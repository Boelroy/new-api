#!/usr/bin/env node
// Minimal probe: does the upstream gateway honor anthropic-beta headers and
// the GA Memory / Tool Search server tools? Each capability fires multiple
// variants (with/without beta header) so you can spot:
//   - Gateway STRIPS beta → "with-beta" variant errors as if no beta was sent
//   - Gateway INJECTS beta → "without-beta" variant succeeds anyway
//   - Upstream provider unsupported (e.g. Bedrock Converse) → all variants 4xx
//
// Reads URL / KEY / MODEL from .env. Usage:
//   node tools/provider-eval/beta-probe.mjs
//   MODEL_OVERRIDE=claude-opus-4-7 node tools/provider-eval/beta-probe.mjs

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

async function fire(body, betaHeader) {
  const headers = {
    "content-type": "application/json",
    "x-api-key": KEY,
    "anthropic-version": "2023-06-01",
  };
  if (betaHeader) headers["anthropic-beta"] = betaHeader;
  const t0 = Date.now();
  let res, text;
  try {
    res = await fetch(`${BASE}/v1/messages`, {
      method: "POST",
      headers,
      body: JSON.stringify(body),
    });
    text = await res.text();
  } catch (e) {
    return { status: -1, elapsed: Date.now() - t0, err: String(e) };
  }
  let json = null;
  try { json = JSON.parse(text); } catch {}
  return { status: res.status, elapsed: Date.now() - t0, json, raw: text };
}

function shortErr(json, raw) {
  const m = json?.error?.message || json?.error?.type || raw || "?";
  return String(m).replace(/\s+/g, " ").slice(0, 140);
}

const PROBES = [
  {
    name: "context-management-2025-06-27 (Context Editing, beta)",
    body: {
      model: MODEL,
      max_tokens: 16,
      context_management: {
        edits: [{
          type: "clear_tool_uses_20250919",
          trigger: { type: "input_tokens", value: 100000 },
        }],
      },
      messages: [{ role: "user", content: "ok" }],
    },
    variants: [
      { label: "with-beta",    beta: "context-management-2025-06-27" },
      { label: "without-beta", beta: null },
    ],
    signals: (j) => ({
      ctxmgmt_echo: !!j?.context_management,
      applied:      j?.context_management?.applied_edits?.length ?? null,
      input_tokens: j?.usage?.input_tokens ?? null,
    }),
  },
  {
    name: "compact-2026-01-12 (Compaction, beta)",
    body: {
      model: MODEL,
      max_tokens: 16,
      context_management: {
        edits: [{
          type: "compact_20260112",
          trigger: { type: "input_tokens", value: 100000 },
        }],
      },
      messages: [{ role: "user", content: "ok" }],
    },
    variants: [
      { label: "with-beta",    beta: "compact-2026-01-12" },
      { label: "without-beta", beta: null },
    ],
    signals: (j) => ({
      ctxmgmt_echo: !!j?.context_management,
      iterations:   j?.usage?.iterations?.length ?? null,
      input_tokens: j?.usage?.input_tokens ?? null,
    }),
  },
  {
    name: "tool_search_tool_regex_20251119 (Tool Search, GA)",
    body: {
      model: MODEL,
      max_tokens: 128,
      tools: [
        { type: "tool_search_tool_regex_20251119", name: "tool_search_tool_regex" },
        {
          name: "get_weather",
          description: "Get the current weather for a given city",
          input_schema: {
            type: "object",
            properties: { city: { type: "string" } },
            required: ["city"],
          },
          defer_loading: true,
        },
      ],
      messages: [{ role: "user", content: "What is the weather in Tokyo? Search your tools first." }],
    },
    variants: [
      { label: "no-beta (GA path)",       beta: null },
      { label: "legacy advanced-tool-use", beta: "advanced-tool-use-2025-11-20" },
    ],
    signals: (j) => {
      const c = j?.content || [];
      const stUse = c.find(b => b.type === "server_tool_use");
      const stRes = c.find(b => b.type === "tool_search_tool_result");
      const tu    = c.find(b => b.type === "tool_use");
      return {
        server_tool_use:     !!stUse,
        tool_search_result:  !!stRes,
        tool_search_requests: j?.usage?.server_tool_use?.tool_search_requests ?? null,
        downstream_tool_use:  tu?.name ?? null,
        stop_reason:          j?.stop_reason ?? null,
      };
    },
  },
  {
    name: "memory_20250818 (Memory Tool, GA)",
    body: {
      model: MODEL,
      max_tokens: 128,
      tools: [{ type: "memory_20250818", name: "memory" }],
      system: "Always check /memories first using the memory tool's view command.",
      messages: [{ role: "user", content: "What do you remember about me?" }],
    },
    variants: [
      { label: "no-beta (GA path)",  beta: null },
      { label: "with ctx-mgmt beta", beta: "context-management-2025-06-27" },
    ],
    signals: (j) => {
      const c = j?.content || [];
      const memUse = c.find(b => b.type === "tool_use" && b.name === "memory");
      return {
        memory_tool_use:   !!memUse,
        memory_command:    memUse?.input?.command ?? null,
        stop_reason:       j?.stop_reason ?? null,
      };
    },
  },
];

console.log(`endpoint=${BASE}  model=${MODEL}`);
console.log();

for (const p of PROBES) {
  console.log(`=== ${p.name} ===`);
  for (const v of p.variants) {
    const r = await fire(p.body, v.beta);
    const tag = `  [${v.label.padEnd(28)}] beta=${(v.beta || "—").padEnd(36)} → ${r.status}`;
    if (r.status === 200) {
      const sig = p.signals(r.json);
      const sigStr = Object.entries(sig).map(([k, vv]) => `${k}=${vv}`).join("  ");
      console.log(`${tag}  ${r.elapsed}ms  ${sigStr}`);
    } else {
      console.log(`${tag}  ${r.elapsed}ms  err: ${shortErr(r.json, r.raw)}`);
    }
    await new Promise(rs => setTimeout(rs, 400));
  }
  console.log();
}

console.log("Interpretation:");
console.log("  with-beta fails / no-beta succeeds         → gateway is stripping/auto-injecting headers");
console.log("  both variants same 4xx                     → upstream provider doesn't support the feature");
console.log("  Tool Search 200 + tool_search_requests>=1  → server-side tool search executed");
console.log("  Memory 200 + memory_tool_use=true          → model issued client-side memory tool_use");
