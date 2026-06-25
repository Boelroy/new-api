#!/usr/bin/env node
// Capture ALL response headers from leaked 200s vs strict 400s, find any
// header field that reliably distinguishes them. The goal is to give downstream
// clients a single-request fingerprint to detect "did I just hit the 逆向
// channel?" without needing latency thresholds.

import { readFileSync, writeFileSync } from "node:fs";
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
const BATCH = Number(process.env.BATCH || 10);

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
  const elapsed = Date.now() - t0;
  const headers = {};
  for (const [k, v] of res.headers.entries()) headers[k] = v;
  let json = null;
  try { json = JSON.parse(text); } catch {}
  return { status: res.status, elapsed, headers, body: json, raw: text };
}

// Capture valid thinking block first
console.log(`endpoint=${BASE}  model=${MODEL}  N=${N}  batch=${BATCH}\n=== capture ===`);
const r1 = await send({
  model: MODEL,
  max_tokens: 4096,
  thinking: { type: "enabled", budget_tokens: 1024 },
  messages: [{ role: "user", content: "What is 25 * 17? Think carefully and show your reasoning step by step." }],
});
if (r1.status !== 200) { console.log("capture failed:", r1.raw?.slice(0,300)); process.exit(1); }
const blocks = r1.body.content;
const tb = blocks.find(b => b.type === "thinking");
if (!tb) { console.log("no thinking"); process.exit(1); }
console.log(`  captured sig len=${tb.signature.length}`);

const tampered = blocks.map(b => b.type === "thinking" ? { ...b, signature: "" } : b);
const body = {
  model: MODEL,
  max_tokens: 4096,
  thinking: { type: "enabled", budget_tokens: 1024 },
  messages: [
    { role: "user", content: "What is 25 * 17? Think carefully and show your reasoning step by step." },
    { role: "assistant", content: tampered },
    { role: "user", content: "Briefly confirm the number." },
  ],
};

console.log(`\n=== ${N} empty-sig replays (batch=${BATCH}) ===`);
const all = [];
for (let i = 0; i < N; i += BATCH) {
  const batchN = Math.min(BATCH, N - i);
  const batch = await Promise.all(Array.from({ length: batchN }, (_, k) => send(body).then(r => ({ idx: i+k, ...r }))));
  for (const r of batch) {
    const tag = r.status === 200 ? "★ LEAK" : "      ";
    console.log(`  #${String(r.idx).padStart(3,"0")} ${tag} status=${r.status} ${r.elapsed}ms`);
    all.push(r);
  }
}

// Persist full data
const outPath = join(__dirname, "leak-fingerprint-data.json");
writeFileSync(outPath, JSON.stringify(all.map(r => ({ idx: r.idx, status: r.status, elapsed: r.elapsed, headers: r.headers, body_sample: (r.body?.error?.message || JSON.stringify(r.body)?.slice(0, 200)) })), null, 2));
console.log(`\nfull data written to ${outPath}`);

// Analyze
const leaks = all.filter(r => r.status === 200);
const rejects = all.filter(r => r.status >= 400);
console.log(`\n=== Distribution: ${leaks.length} leaks / ${rejects.length} rejects / ${N} total ===`);
if (leaks.length === 0) { console.log("no leaks observed, nothing to diff"); process.exit(0); }
if (rejects.length === 0) { console.log("no rejects observed, nothing to diff"); process.exit(0); }

// Find all header keys across all responses
const allKeys = new Set();
for (const r of all) for (const k of Object.keys(r.headers)) allKeys.add(k);

console.log("\n=== Per-header presence + value diversity ===");
console.log("header                          | 200 group                              | 400 group");
console.log("-".repeat(120));

function summarizeHeader(records, key) {
  const present = records.filter(r => r.headers[key] !== undefined);
  if (present.length === 0) return "—";
  const values = present.map(r => r.headers[key]);
  const uniq = [...new Set(values)];
  // shorten very long IDs
  const sample = uniq.slice(0, 2).map(v => v.length > 30 ? v.slice(0, 30) + "…" : v).join(" | ");
  return `${present.length}/${records.length} [${uniq.length} uniq] ${sample}`;
}

const keysOrdered = [...allKeys].sort();
const distinguishing = [];

for (const k of keysOrdered) {
  const okSum = summarizeHeader(leaks, k);
  const errSum = summarizeHeader(rejects, k);
  // skip if both sides identical (e.g. "server" same for all)
  const okPresence = leaks.filter(r => r.headers[k] !== undefined).length;
  const errPresence = rejects.filter(r => r.headers[k] !== undefined).length;
  const presenceDiffers = (okPresence / leaks.length > 0.5) !== (errPresence / rejects.length > 0.5);
  console.log(`${k.padEnd(32)}| ${okSum.padEnd(38)} | ${errSum}`);
  if (presenceDiffers) distinguishing.push({ header: k, ok: okSum, err: errSum });
}

if (distinguishing.length) {
  console.log("\n=== ★ Distinguishing headers (presence majority differs) ===");
  for (const d of distinguishing) {
    console.log(`  ${d.header}`);
    console.log(`    200: ${d.ok}`);
    console.log(`    400: ${d.err}`);
  }
} else {
  console.log("\nno header presence-majority diverges between 200 and 400 groups");
}

// Also check latency separation
const okLat = leaks.map(r => r.elapsed).sort((a,b)=>a-b);
const errLat = rejects.map(r => r.elapsed).sort((a,b)=>a-b);
console.log("\n=== Latency (ms) ===");
function stat(arr, label) {
  const mn = arr[0], mx = arr[arr.length-1];
  const med = arr[Math.floor(arr.length/2)];
  const p25 = arr[Math.floor(arr.length*0.25)];
  const p75 = arr[Math.floor(arr.length*0.75)];
  console.log(`  ${label.padEnd(12)} n=${arr.length}  min=${mn}  p25=${p25}  med=${med}  p75=${p75}  max=${mx}`);
}
stat(okLat, "200 leaks");
stat(errLat, "400 rejects");
