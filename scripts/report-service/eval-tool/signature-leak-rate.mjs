#!/usr/bin/env node
// Estimate leak rate: send N concurrent requests with an OBVIOUSLY invalid
// signature (empty string), count how many slip through with 200. Each 200 =
// landed on a lenient backend (e.g. LiteLLM-fronted) that doesn't validate.

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
const MODEL = process.env.MODEL_OVERRIDE || "claude-sonnet-4-6";
const N = Number(process.env.N || 50);
const BATCH = Number(process.env.BATCH || 10); // concurrent batch size

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

const prompt = "What is 25 * 17? Think carefully and show your reasoning step by step.";
const followup = "Briefly confirm the number.";

// Step 1: capture a real thinking block (we need the real thinking text,
// just with a corrupted signature)
console.log(`endpoint=${BASE}  model=${MODEL}  N=${N}  batch=${BATCH}`);
console.log("\n=== capture ===");
const r1 = await send({
  model: MODEL,
  max_tokens: 4096,
  thinking: { type: "enabled", budget_tokens: 1024 },
  messages: [{ role: "user", content: prompt }],
});
if (r1.status !== 200) {
  console.log("capture failed:", r1.raw?.slice(0, 300));
  process.exit(1);
}
const blocks = r1.body.content;
const tb = blocks.find(b => b.type === "thinking");
if (!tb) {
  console.log("no thinking block");
  process.exit(1);
}
console.log(`  captured sig len=${tb.signature.length}, replacing with empty string`);

// Step 2: corrupt to empty signature, replay N times in batches
const tamperedBlocks = blocks.map(b => b.type === "thinking" ? { ...b, signature: "" } : b);
const body = {
  model: MODEL,
  max_tokens: 4096,
  thinking: { type: "enabled", budget_tokens: 1024 },
  messages: [
    { role: "user", content: prompt },
    { role: "assistant", content: tamperedBlocks },
    { role: "user", content: followup },
  ],
};

console.log(`\n=== ${N} requests with empty signature (batch=${BATCH}) ===`);
const t0 = Date.now();
const all = [];
for (let i = 0; i < N; i += BATCH) {
  const batchN = Math.min(BATCH, N - i);
  const batch = await Promise.all(Array.from({ length: batchN }, (_, k) => send(body).then(r => ({ idx: i + k, ...r }))));
  for (const r of batch) {
    const tag = r.status === 200 ? "★ LEAK" : "      ";
    console.log(`  #${String(r.idx).padStart(3, "0")}  ${tag}  status=${r.status}  ${r.elapsed}ms`);
    all.push(r);
  }
}
const elapsed = Date.now() - t0;

const dist = {};
for (const r of all) dist[r.status] = (dist[r.status] || 0) + 1;
const leaks = all.filter(r => r.status === 200).length;
const rate = (leaks / N * 100).toFixed(1);

console.log(`\n=== Result ===`);
console.log(`total wall: ${elapsed}ms`);
console.log(`distribution: ${JSON.stringify(dist)}`);
console.log(`LEAK RATE: ${leaks}/${N} = ${rate}%`);
