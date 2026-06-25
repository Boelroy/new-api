#!/usr/bin/env node
// Tampered-signature probe in TOOL_USE LOOP context, where Anthropic docs say
// signature validation is strict (thinking → tool_use → tool_result must
// preserve the signature exactly).
//
// Flow:
//   1. Capture (thinking, tool_use) bundle by asking a question that requires
//      a tool call.
//   2. For each signature variant (control + 6 tamperings), send tool_result
//      back with the assistant turn containing the (possibly tampered)
//      thinking block. Replay K concurrent.
//   3. If validation is strict, tampered → 400; if all 200, backend really
//      doesn't validate.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { randomBytes as cryptoRand } from "node:crypto";

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
const MODEL = process.env.MODEL_OVERRIDE || "claude-sonnet-4-6";
const REPLAYS = Number(process.env.N || 5);

async function send(body) {
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
  return { status: res.status, elapsed: Date.now() - t0, body: json, raw: text };
}

const tools = [
  {
    name: "get_weather",
    description: "Get the current weather for a given city.",
    input_schema: {
      type: "object",
      properties: { city: { type: "string", description: "City name." } },
      required: ["city"],
    },
  },
];

const userPrompt = "What's the current weather in Tokyo? Use the get_weather tool. Think step by step about what tool to call.";

console.log(`endpoint=${BASE}  model=${MODEL}  replays-per-variant=${REPLAYS}`);

// Step 1: capture thinking + tool_use
console.log("\n=== capturing thinking + tool_use ===");
const r1 = await send({
  model: MODEL,
  max_tokens: 4096,
  thinking: { type: "enabled", budget_tokens: 1024 },
  tools,
  messages: [{ role: "user", content: userPrompt }],
});

if (r1.status !== 200) {
  console.log("capture failed:", r1.raw?.slice(0, 400));
  process.exit(1);
}

console.log(`  stop_reason=${r1.body.stop_reason}`);
console.log(`  blocks=[${r1.body.content.map(b => b.type).join(", ")}]`);

const blocks = r1.body.content;
const tb = blocks.find(b => b.type === "thinking");
const tu = blocks.find(b => b.type === "tool_use");

if (!tb || !tu) {
  console.log("  missing thinking or tool_use block; got:", JSON.stringify(blocks, null, 2).slice(0, 500));
  process.exit(1);
}

console.log(`  thinking sig: ${tb.signature.slice(0, 32)}…${tb.signature.slice(-12)} (len=${tb.signature.length})`);
console.log(`  tool_use id: ${tu.id}  name=${tu.name}  input=${JSON.stringify(tu.input)}`);

const validSig = tb.signature;

const B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
function randB64(n) {
  return Array.from(cryptoRand(n), b => B64[b % 64]).join("");
}
function flipTail(sig, n) {
  const padded = sig.endsWith("=");
  return sig.slice(0, sig.length - n - (padded ? 1 : 0)) + randB64(n) + (padded ? "=" : "");
}
function flipMiddle(sig, n) {
  const s = Math.floor(sig.length / 2) - Math.floor(n / 2);
  return sig.slice(0, s) + randB64(n) + sig.slice(s + n);
}

const variants = [
  { name: "valid (control)", sig: validSig },
  { name: "tail flip 8", sig: flipTail(validSig, 8) },
  { name: "tail flip 32", sig: flipTail(validSig, 32) },
  { name: "middle flip 16", sig: flipMiddle(validSig, 16) },
  { name: "truncated (-20)", sig: validSig.slice(0, -20) + (validSig.endsWith("=") ? "=" : "") },
  { name: "all random b64", sig: randB64(validSig.length - (validSig.endsWith("=") ? 1 : 0)) + (validSig.endsWith("=") ? "=" : "") },
  { name: "empty signature", sig: "" },
];

console.log(`\n=== ${REPLAYS} concurrent replays per variant (tool_use loop) ===`);

const summary = [];

for (const v of variants) {
  const tamperedAssistant = blocks.map(b =>
    b.type === "thinking" ? { ...b, signature: v.sig } : b
  );

  const body = {
    model: MODEL,
    max_tokens: 4096,
    thinking: { type: "enabled", budget_tokens: 1024 },
    tools,
    messages: [
      { role: "user", content: userPrompt },
      { role: "assistant", content: tamperedAssistant },
      {
        role: "user",
        content: [
          {
            type: "tool_result",
            tool_use_id: tu.id,
            content: "Weather in Tokyo: 22°C, partly cloudy, light wind from the east.",
          },
        ],
      },
    ],
  };

  const t0 = Date.now();
  const results = await Promise.all(Array.from({ length: REPLAYS }, () => send(body)));
  const elapsed = Date.now() - t0;

  const dist = {};
  for (const r of results) dist[r.status] = (dist[r.status] || 0) + 1;

  console.log(`\n--- variant: ${v.name} ---`);
  console.log(`  sig: ${v.sig.slice(0, 32)}…${v.sig.slice(-12)} (len=${v.sig.length})`);
  console.log(`  ${elapsed}ms  status=${JSON.stringify(dist)}`);

  const errSamples = new Map();
  for (const r of results.filter(r => r.status >= 400)) {
    const m = (r.body?.error?.message || r.raw || "").slice(0, 200).replace(/\s+/g, " ");
    errSamples.set(m, (errSamples.get(m) || 0) + 1);
  }
  for (const [msg, count] of errSamples) {
    console.log(`    err [×${count}]: ${msg}`);
  }

  summary.push({ name: v.name, dist });
}

console.log("\n=== Summary ===");
console.log("variant            | result");
console.log("-".repeat(60));
for (const s of summary) {
  const dist = Object.entries(s.dist).map(([k, v]) => `${k}×${v}`).join("  ");
  const isCtrl = s.name === "valid (control)";
  let flag = "";
  if (!isCtrl) {
    if ((s.dist[200] || 0) > 0) flag = "  ⚠ accepted (not validated)";
    if ((s.dist[400] || 0) > 0) flag = "  ✓ rejected (validated)";
  }
  console.log(`${s.name.padEnd(18)} | ${dist}${flag}`);
}
