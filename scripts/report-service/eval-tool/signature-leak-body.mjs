#!/usr/bin/env node
// Inspect FULL response bodies of leaked 200s (empty signature accepted) vs a
// "正常合法" baseline (valid signature, no tampering). Compare structure,
// content quality, usage, model field, semantic correctness.

import { readFileSync, writeFileSync } from "node:fs";
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
const N = Number(process.env.N || 30);
const BATCH = Number(process.env.BATCH || 6);
const TAMPER = process.env.TAMPER || "empty"; // empty | tail8 | tail32 | mid16

const B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
function randB64(n) { return Array.from(cryptoRand(n), b => B64[b % 64]).join(""); }
function tamperSignature(sig, mode) {
  if (mode === "empty") return "";
  const padded = sig.endsWith("=");
  const padLen = padded ? 1 : 0;
  if (mode === "tail8")  return sig.slice(0, sig.length - 8  - padLen) + randB64(8)  + (padded ? "=" : "");
  if (mode === "tail32") return sig.slice(0, sig.length - 32 - padLen) + randB64(32) + (padded ? "=" : "");
  if (mode === "mid16") {
    const s = Math.floor(sig.length / 2) - 8;
    return sig.slice(0, s) + randB64(16) + sig.slice(s + 16);
  }
  throw new Error(`unknown TAMPER mode: ${mode}`);
}

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
  const headers = {};
  for (const [k, v] of res.headers.entries()) headers[k] = v;
  return { status: res.status, elapsed: Date.now() - t0, headers, body: json };
}

// Step 1: capture a real thinking block (no tampering, get baseline shape)
console.log(`endpoint=${BASE}  model=${MODEL}  TAMPER=${TAMPER}\n`);
console.log("=== Step 1: baseline (valid sig follow-up) ===");

const cap = await send({
  model: MODEL,
  max_tokens: 4096,
  thinking: { type: "enabled", budget_tokens: 1024 },
  messages: [{ role: "user", content: "What is 25 * 17? Think carefully and show your reasoning step by step." }],
});
if (cap.status !== 200) { console.log("capture failed"); process.exit(1); }
const blocks = cap.body.content;
const tb = blocks.find(b => b.type === "thinking");

// Baseline: valid signature follow-up
const validBody = {
  model: MODEL,
  max_tokens: 4096,
  thinking: { type: "enabled", budget_tokens: 1024 },
  messages: [
    { role: "user", content: "What is 25 * 17? Think carefully and show your reasoning step by step." },
    { role: "assistant", content: blocks },
    { role: "user", content: "Briefly confirm the number." },
  ],
};

const baseline = await send(validBody);
console.log(`  baseline status=${baseline.status} elapsed=${baseline.elapsed}ms`);
console.log(`  body.model = ${baseline.body?.model}`);
console.log(`  body.id    = ${baseline.body?.id}`);
console.log(`  blocks     = [${baseline.body?.content?.map(b => b.type).join(", ")}]`);
console.log(`  usage      = ${JSON.stringify(baseline.body?.usage)}`);
console.log(`  stop       = ${baseline.body?.stop_reason}`);
const baseText = baseline.body?.content?.find(b => b.type === "text")?.text || "";
console.log(`  text       = ${JSON.stringify(baseText.slice(0, 200))}`);

// Step 2: tampered-sig replays — collect 200s with full body
const tamperedSig = tamperSignature(tb.signature, TAMPER);
console.log(`  tampered sig (mode=${TAMPER}): ${tamperedSig.slice(0, 32)}…${tamperedSig.slice(-16)} (len=${tamperedSig.length})`);
const tampered = blocks.map(b => b.type === "thinking" ? { ...b, signature: tamperedSig } : b);
const leakBody = { ...validBody, messages: [
  validBody.messages[0],
  { role: "assistant", content: tampered },
  validBody.messages[2],
]};

console.log(`\n=== Step 2: ${N} tampered-sig replays (mode=${TAMPER}, batch=${BATCH}) ===`);
const all = [];
for (let i = 0; i < N; i += BATCH) {
  const batchN = Math.min(BATCH, N - i);
  const batch = await Promise.all(Array.from({ length: batchN }, (_, k) => send(leakBody).then(r => ({ idx: i+k, ...r }))));
  for (const r of batch) {
    const tag = r.status === 200 ? "★ LEAK" : "      ";
    console.log(`  #${String(r.idx).padStart(3,"0")} ${tag} status=${r.status} ${r.elapsed}ms`);
    all.push(r);
  }
}

const leaks = all.filter(r => r.status === 200);
const rejects = all.filter(r => r.status >= 400);
console.log(`\n=== ${leaks.length} leaks / ${rejects.length} rejects ===`);

if (leaks.length === 0) {
  console.log("no leaks captured this run");
  process.exit(0);
}

// Save full leaks for review (filename includes TAMPER mode)
const outFile = `leak-bodies-${TAMPER}.json`;
writeFileSync(
  join(__dirname, outFile),
  JSON.stringify({
    endpoint: BASE,
    model: MODEL,
    tamper_mode: TAMPER,
    baseline: { body: baseline.body, elapsed: baseline.elapsed, headers: baseline.headers },
    leaks: leaks.map(r => ({ idx: r.idx, elapsed: r.elapsed, headers: r.headers, body: r.body })),
    rejects: rejects.map(r => ({ idx: r.idx, elapsed: r.elapsed, headers: r.headers, body: r.body })),
  }, null, 2),
);

// Print each leak's body structure
console.log("\n=== Leaked 200 bodies ===");
for (const r of leaks) {
  console.log(`\n--- leak #${r.idx} (elapsed=${r.elapsed}ms) ---`);
  console.log(`  model = ${r.body?.model}`);
  console.log(`  id    = ${r.body?.id}`);
  console.log(`  type  = ${r.body?.type}`);
  console.log(`  role  = ${r.body?.role}`);
  console.log(`  blocks= [${r.body?.content?.map(b => b.type).join(", ")}]`);
  console.log(`  usage = ${JSON.stringify(r.body?.usage)}`);
  console.log(`  stop  = ${r.body?.stop_reason}`);
  for (const block of r.body?.content || []) {
    if (block.type === "text") {
      console.log(`  text  = ${JSON.stringify(block.text.slice(0, 400))}`);
    } else if (block.type === "thinking") {
      console.log(`  thinking = ${JSON.stringify((block.thinking || "").slice(0, 200))}`);
      console.log(`  sig len  = ${(block.signature || "").length}`);
    }
  }
}

// Structural diff baseline vs first leak
if (leaks.length > 0) {
  console.log("\n=== Structural diff: baseline vs leak[0] ===");
  const b = baseline.body;
  const l = leaks[0].body;
  const keys = new Set([...Object.keys(b || {}), ...Object.keys(l || {})]);
  for (const k of keys) {
    const bv = JSON.stringify(b?.[k]);
    const lv = JSON.stringify(l?.[k]);
    const eq = bv === lv;
    const marker = eq ? "  =" : "  ≠";
    console.log(`${marker} ${k}`);
    if (!eq) {
      console.log(`     baseline: ${bv?.slice(0, 120)}`);
      console.log(`     leak    : ${lv?.slice(0, 120)}`);
    }
  }
}

console.log(`\nfull data: tools/provider-eval/${outFile}`);
