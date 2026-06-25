#!/usr/bin/env node
// Tampered-signature probe: capture a valid (thinking, signature) bundle,
// produce N corrupted variants, replay each K times. ANY 200 on a corrupted
// signature = the receiving backend is not validating signatures (i.e. it's
// not the real Anthropic / Bedrock / Vertex — it's something else upstream
// that silently accepts garbage signatures).

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
  const reqId =
    res.headers.get("request-id") ||
    res.headers.get("x-request-id") ||
    res.headers.get("anthropic-request-id") ||
    null;
  return { status: res.status, reqId, elapsed: Date.now() - t0, body: json, raw: text };
}

const prompt = "What is 25 * 17? Think carefully and show your reasoning step by step.";
const followup = "Briefly confirm the number.";

console.log(`endpoint=${BASE}  model=${MODEL}  replays-per-variant=${REPLAYS}`);

// Step 1: capture a valid signature
console.log("\n=== capturing valid signature ===");
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
const tb = blocks.find((b) => b.type === "thinking");
if (!tb) {
  console.log("no thinking block returned");
  process.exit(1);
}

const validSig = tb.signature;
console.log(`captured sig (len=${validSig.length}): ${validSig.slice(0, 32)}…${validSig.slice(-16)}`);

// Helper: produce variants. base64 alphabet is A-Z a-z 0-9 + / and = padding.
const B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
function randB64Chars(n) {
  const buf = cryptoRand(n);
  return Array.from(buf, (b) => B64[b % 64]).join("");
}
function flipTail(sig, n) {
  return sig.slice(0, sig.length - n - (sig.endsWith("=") ? 1 : 0)) + randB64Chars(n) + (sig.endsWith("=") ? "=" : "");
}
function flipMiddle(sig, n) {
  const start = Math.floor(sig.length / 2) - Math.floor(n / 2);
  return sig.slice(0, start) + randB64Chars(n) + sig.slice(start + n);
}

const variants = [
  { name: "valid (control)", sig: validSig },
  { name: "tail flip 8", sig: flipTail(validSig, 8) },
  { name: "tail flip 32", sig: flipTail(validSig, 32) },
  { name: "middle flip 16", sig: flipMiddle(validSig, 16) },
  { name: "truncated (-20)", sig: validSig.slice(0, -20) + (validSig.endsWith("=") ? "=" : "") },
  { name: "all random b64", sig: randB64Chars(validSig.length - (validSig.endsWith("=") ? 1 : 0)) + (validSig.endsWith("=") ? "=" : "") },
  { name: "empty signature", sig: "" },
];

console.log(`\n=== ${REPLAYS} concurrent replays per variant ===`);

const summary = [];

for (const v of variants) {
  const tamperedBlocks = blocks.map((b) =>
    b.type === "thinking" ? { ...b, signature: v.sig } : b
  );

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

  const t0 = Date.now();
  const results = await Promise.all(Array.from({ length: REPLAYS }, () => send(body)));
  const elapsed = Date.now() - t0;

  const dist = {};
  for (const r of results) dist[r.status] = (dist[r.status] || 0) + 1;

  console.log(`\n--- variant: ${v.name} ---`);
  console.log(`  sig: ${v.sig.slice(0, 32)}…${v.sig.slice(-16)} (len=${v.sig.length})`);
  console.log(`  ${elapsed}ms  status=${JSON.stringify(dist)}`);

  const errSamples = new Map();
  for (const r of results.filter(r => r.status >= 400)) {
    const m = (r.body?.error?.message || r.raw || "").slice(0, 160).replace(/\s+/g, " ");
    errSamples.set(m, (errSamples.get(m) || 0) + 1);
  }
  for (const [msg, count] of errSamples) {
    console.log(`    err [×${count}]: ${msg}`);
  }
  const oks = results.filter(r => r.status === 200);
  if (oks.length > 0 && v.name !== "valid (control)") {
    console.log(`  !!! ${oks.length} unexpected 200(s):`);
    for (const r of oks.slice(0, 3)) {
      console.log(`    reqId=${r.reqId}  elapsed=${r.elapsed}ms`);
    }
  }
  summary.push({ name: v.name, dist });
}

console.log("\n=== Summary ===");
console.log("variant            | result");
console.log("-".repeat(60));
for (const s of summary) {
  const dist = Object.entries(s.dist).map(([k, v]) => `${k}×${v}`).join("  ");
  const flag = s.name !== "valid (control)" && (s.dist[200] || 0) > 0 ? "  ⚠ UNEXPECTED 200" : "";
  console.log(`${s.name.padEnd(18)} | ${dist}${flag}`);
}
