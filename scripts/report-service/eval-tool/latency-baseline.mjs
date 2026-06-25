#!/usr/bin/env node
// Measure baseline latency across several request shapes to characterize
// the endpoint's overall response time (not just signature-leak scenarios).

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
const N = Number(process.env.N || 10);

async function send(body) {
  const t0 = Date.now();
  const ttfbStart = t0;
  let res, text, ttfb = null;
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
    ttfb = Date.now() - ttfbStart;
    text = await res.text();
  } catch (e) {
    return { status: -1, elapsed: Date.now() - t0, err: String(e) };
  }
  let json = null;
  try { json = JSON.parse(text); } catch {}
  const headers = {};
  for (const [k, v] of res.headers.entries()) headers[k] = v;
  return { status: res.status, elapsed: Date.now() - t0, ttfb, body: json, headers };
}

const shapes = [
  {
    name: "1. trivial ping (max_tokens=20)",
    body: {
      model: MODEL,
      max_tokens: 20,
      messages: [{ role: "user", content: "Say 'pong'." }],
    },
  },
  {
    name: "2. short math (no thinking)",
    body: {
      model: MODEL,
      max_tokens: 256,
      messages: [{ role: "user", content: "What is 25 * 17? Just the number." }],
    },
  },
  {
    name: "3. short math (with thinking 1024)",
    body: {
      model: MODEL,
      max_tokens: 4096,
      thinking: { type: "enabled", budget_tokens: 1024 },
      messages: [{ role: "user", content: "What is 25 * 17? Think carefully." }],
    },
  },
  {
    name: "4. medium reasoning (with thinking 1024)",
    body: {
      model: MODEL,
      max_tokens: 4096,
      thinking: { type: "enabled", budget_tokens: 1024 },
      messages: [{ role: "user", content: "If today is Wednesday, what day of the week will it be 100 days from now? Think step by step." }],
    },
  },
];

function stat(arr, label) {
  if (arr.length === 0) return `${label}: no data`;
  const s = [...arr].sort((a, b) => a - b);
  const mn = s[0], mx = s[s.length - 1];
  const med = s[Math.floor(s.length / 2)];
  const p25 = s[Math.floor(s.length * 0.25)];
  const p75 = s[Math.floor(s.length * 0.75)];
  const avg = Math.round(s.reduce((a, b) => a + b, 0) / s.length);
  return `n=${s.length}  min=${mn}  p25=${p25}  med=${med}  avg=${avg}  p75=${p75}  max=${mx}`;
}

console.log(`endpoint=${BASE}  model=${MODEL}  N=${N} per shape\n`);

for (const sh of shapes) {
  console.log(`=== ${sh.name} ===`);
  const results = [];
  // Sequential to avoid concurrent-load skew
  for (let i = 0; i < N; i++) {
    const r = await send(sh.body);
    results.push(r);
    const thinking = r.body?.content?.some(b => b.type === "thinking") ? " +thinking" : "";
    const xnav = r.headers?.["x-new-api-version"] || "—";
    const setcookie = r.headers?.["set-cookie"] ? "★" : " ";
    console.log(`  #${String(i).padStart(2,"0")}  status=${r.status}  elapsed=${r.elapsed}ms  ttfb=${r.ttfb}ms  x-new-api=${xnav}  ${setcookie}leak-fp${thinking}`);
  }
  const oks = results.filter(r => r.status === 200);
  console.log(`  full latency:  ${stat(oks.map(r => r.elapsed))}`);
  console.log(`  ttfb:          ${stat(oks.map(r => r.ttfb))}`);
  // Identify whether returned by leak fingerprint
  const leakCount = oks.filter(r => r.headers?.["set-cookie"]).length;
  console.log(`  leak-fp share: ${leakCount}/${oks.length} (set-cookie present)\n`);
}
