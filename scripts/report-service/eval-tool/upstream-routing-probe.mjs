#!/usr/bin/env node
// Probe OpenRouter (or any) upstream routing distribution.
// Sends N sequential tool_use requests, classifies each by tool_use_id prefix:
//   toolu_01*       → Anthropic 直供
//   toolu_bdrk_*    → AWS Bedrock
//   toolu_vrtx_*    → Google Vertex
// Also collects `provider` field from response body when present.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const env = (() => {
  const text = readFileSync(join(__dirname, ".env"), "utf8");
  const e = {};
  for (const line of text.split("\n")) {
    const m = line.match(/^([A-Z_]+)\s*=\s*(.*)$/);
    if (m) e[m[1]] = m[2].replace(/^["']|["']$/g, "").trim();
  }
  return e;
})();

const BASE = env.URL.replace(/\/$/, "");
const KEY = env.KEY;
const MODEL = process.env.MODEL_OVERRIDE || env.MODEL || "claude-sonnet-4-6";
const N = Number(process.env.N || 30);
const GAP_MS = Number(process.env.GAP_MS || 2500);

const body = {
  model: MODEL,
  max_tokens: 1024,
  tools: [{
    name: "get_weather",
    description: "Get the current weather for a given city.",
    input_schema: {
      type: "object",
      properties: { city: { type: "string" } },
      required: ["city"],
    },
  }],
  messages: [{
    role: "user",
    content: "What is the weather in Tokyo? Use the get_weather tool.",
  }],
};

async function send() {
  const t0 = Date.now();
  let res, text;
  try {
    res = await fetch(`${BASE}/v1/messages`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "x-api-key": KEY,
        "anthropic-version": "2023-06-01",
      },
      body: JSON.stringify(body),
    });
    text = await res.text();
  } catch (e) {
    return { status: -1, elapsed: Date.now() - t0, err: String(e) };
  }
  let json = null;
  try { json = JSON.parse(text); } catch {}
  return { status: res.status, elapsed: Date.now() - t0, body: json };
}

function classify(toolId) {
  if (!toolId) return "no-tool-id";
  if (toolId.startsWith("toolu_bdrk_")) return "bedrock";
  if (toolId.startsWith("toolu_vrtx_")) return "vertex";
  if (toolId.startsWith("toolu_")) return "anthropic-direct";
  return `unknown(${toolId.slice(0, 12)}...)`;
}

console.log(`endpoint=${BASE}  model=${MODEL}  N=${N}  gap=${GAP_MS}ms`);
console.log();

const results = [];
for (let i = 0; i < N; i++) {
  const r = await send();
  if (r.status === 200) {
    const tu = r.body?.content?.find(b => b.type === "tool_use");
    const cls = classify(tu?.id);
    const provider = r.body?.provider || "—";
    const inferenceGeo = r.body?.usage?.inference_geo ?? "—";
    results.push({ idx: i, status: 200, cls, toolId: tu?.id, provider, inferenceGeo, elapsed: r.elapsed });
    console.log(`#${String(i).padStart(2,"0")}  200  cls=${cls.padEnd(18)}  provider=${String(provider).padEnd(12)}  inference_geo=${String(inferenceGeo).padEnd(8)}  id=${tu?.id || "—"}  ${r.elapsed}ms`);
  } else {
    const errMsg = r.body?.error?.message || r.err || "?";
    results.push({ idx: i, status: r.status, err: errMsg.slice(0, 80), elapsed: r.elapsed });
    console.log(`#${String(i).padStart(2,"0")}  ${r.status}  err=${errMsg.slice(0, 80)}  ${r.elapsed}ms`);
  }
  if (i < N - 1) await new Promise(r => setTimeout(r, GAP_MS));
}

console.log("\n=== Distribution ===");
const dist = {};
for (const r of results) {
  if (r.status === 200) {
    dist[r.cls] = (dist[r.cls] || 0) + 1;
  } else {
    const key = `error-${r.status}`;
    dist[key] = (dist[key] || 0) + 1;
  }
}
for (const [k, v] of Object.entries(dist)) {
  const pct = (v / N * 100).toFixed(1);
  console.log(`  ${k.padEnd(20)} ${v}/${N}  (${pct}%)`);
}

console.log("\n=== Provider field distribution ===");
const provDist = {};
for (const r of results.filter(r => r.status === 200)) {
  provDist[r.provider] = (provDist[r.provider] || 0) + 1;
}
for (const [k, v] of Object.entries(provDist)) {
  console.log(`  provider=${k.padEnd(20)} ${v}/${N}`);
}

console.log("\n=== inference_geo field distribution ===");
const geoDist = {};
for (const r of results.filter(r => r.status === 200)) {
  geoDist[r.inferenceGeo] = (geoDist[r.inferenceGeo] || 0) + 1;
}
for (const [k, v] of Object.entries(geoDist)) {
  console.log(`  inference_geo=${String(k).padEnd(20)} ${v}/${N}`);
}
