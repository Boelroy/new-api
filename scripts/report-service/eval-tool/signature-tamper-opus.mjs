#!/usr/bin/env node
// Opus-4-7 variant of tampered-signature probe (adaptive thinking API).
// Tests BOTH non-tool_use and tool_use scenarios in one run.

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
const MODEL = process.env.MODEL_OVERRIDE || env.MODEL || "claude-opus-4-7";
const REPLAYS = Number(process.env.N || 5);
const SCENARIO = process.env.SCENARIO || "both"; // both | text | tooluse

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

const B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
function randB64(n) { return Array.from(cryptoRand(n), b => B64[b % 64]).join(""); }
function flipTail(sig, n) {
  const padded = sig.endsWith("=");
  return sig.slice(0, sig.length - n - (padded ? 1 : 0)) + randB64(n) + (padded ? "=" : "");
}
function flipMiddle(sig, n) {
  const s = Math.floor(sig.length / 2) - Math.floor(n / 2);
  return sig.slice(0, s) + randB64(n) + sig.slice(s + n);
}

function variantsOf(validSig) {
  return [
    { name: "valid (control)", sig: validSig },
    { name: "tail flip 8",     sig: flipTail(validSig, 8) },
    { name: "tail flip 32",    sig: flipTail(validSig, 32) },
    { name: "middle flip 16",  sig: flipMiddle(validSig, 16) },
    { name: "truncated (-20)", sig: validSig.slice(0, -20) + (validSig.endsWith("=") ? "=" : "") },
    { name: "all random b64",  sig: randB64(validSig.length - (validSig.endsWith("=") ? 1 : 0)) + (validSig.endsWith("=") ? "=" : "") },
    { name: "empty signature", sig: "" },
  ];
}

async function runScenario(label, captureBody, buildReplayBody) {
  console.log(`\n############ Scenario: ${label} ############`);

  const r1 = await send(captureBody);
  if (r1.status !== 200) {
    console.log("capture failed:", r1.raw?.slice(0, 400));
    return [];
  }

  console.log(`  capture stop_reason=${r1.body.stop_reason} blocks=[${r1.body.content.map(b => b.type).join(", ")}]`);
  const tb = r1.body.content.find(b => b.type === "thinking");
  if (!tb) {
    console.log("  no thinking block returned, skipping");
    return [];
  }
  console.log(`  sig: ${tb.signature.slice(0, 32)}…${tb.signature.slice(-12)} (len=${tb.signature.length})`);

  const variants = variantsOf(tb.signature);
  const summary = [];

  for (const v of variants) {
    const body = buildReplayBody(r1.body.content, v.sig);
    const t0 = Date.now();
    const results = await Promise.all(Array.from({ length: REPLAYS }, () => send(body)));
    const elapsed = Date.now() - t0;
    const dist = {};
    for (const r of results) dist[r.status] = (dist[r.status] || 0) + 1;

    const errSamples = new Map();
    for (const r of results.filter(r => r.status >= 400)) {
      const m = (r.body?.error?.message || r.raw || "").slice(0, 140).replace(/\s+/g, " ");
      errSamples.set(m, (errSamples.get(m) || 0) + 1);
    }

    console.log(`  --- ${v.name.padEnd(18)} ${elapsed}ms  status=${JSON.stringify(dist)}`);
    for (const [msg, count] of errSamples) {
      console.log(`      err [×${count}]: ${msg}`);
    }
    summary.push({ scenario: label, variant: v.name, dist });
  }

  return summary;
}

console.log(`endpoint=${BASE}  model=${MODEL}  replays-per-variant=${REPLAYS}  scenario=${SCENARIO}`);

const prompt = "Prove that there are infinitely many prime numbers. Reason carefully step by step.";
const followup = "Briefly restate the key step in one sentence.";

const tools = [
  {
    name: "lookup_definition",
    description: "Look up a math definition.",
    input_schema: {
      type: "object",
      properties: { term: { type: "string" } },
      required: ["term"],
    },
  },
];
const toolUserPrompt = "Define 'prime number'. Use the lookup_definition tool. Think carefully first about what term to look up.";

const allSummary = [];

if (SCENARIO === "text" || SCENARIO === "both") {
  const s = await runScenario("non-tool_use (text-only)",
    {
      model: MODEL,
      max_tokens: 4096,
      thinking: { type: "adaptive" },
      output_config: { effort: "max" },
      messages: [{ role: "user", content: prompt }],
    },
    (blocks, sig) => ({
      model: MODEL,
      max_tokens: 4096,
      thinking: { type: "adaptive" },
      output_config: { effort: "max" },
      messages: [
        { role: "user", content: prompt },
        { role: "assistant", content: blocks.map(b => b.type === "thinking" ? { ...b, signature: sig } : b) },
        { role: "user", content: followup },
      ],
    })
  );
  allSummary.push(...s);
}

if (SCENARIO === "tooluse" || SCENARIO === "both") {
  const s = await runScenario("tool_use loop",
    {
      model: MODEL,
      max_tokens: 4096,
      thinking: { type: "adaptive" },
      output_config: { effort: "max" },
      tools,
      messages: [{ role: "user", content: toolUserPrompt }],
    },
    (blocks, sig) => {
      const tu = blocks.find(b => b.type === "tool_use");
      return {
        model: MODEL,
        max_tokens: 4096,
        thinking: { type: "adaptive" },
        output_config: { effort: "max" },
        tools,
        messages: [
          { role: "user", content: toolUserPrompt },
          { role: "assistant", content: blocks.map(b => b.type === "thinking" ? { ...b, signature: sig } : b) },
          {
            role: "user",
            content: [{
              type: "tool_result",
              tool_use_id: tu.id,
              content: "A prime number is a natural number greater than 1 with exactly two divisors: 1 and itself.",
            }],
          },
        ],
      };
    }
  );
  allSummary.push(...s);
}

console.log("\n========== Combined Summary ==========");
console.log("scenario                | variant            | result");
console.log("-".repeat(80));
for (const s of allSummary) {
  const dist = Object.entries(s.dist).map(([k, v]) => `${k}×${v}`).join("  ");
  const isCtrl = s.variant === "valid (control)";
  let flag = "";
  if (!isCtrl) {
    if ((s.dist[200] || 0) > 0) flag = "  ⚠ accepted";
    if ((s.dist[400] || 0) > 0) flag = "  ✓ rejected";
  }
  console.log(`${s.scenario.padEnd(24)} | ${s.variant.padEnd(18)} | ${dist}${flag}`);
}
